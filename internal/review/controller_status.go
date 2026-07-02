package review

import (
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/livetemplate/livetemplate"
)

// controller_status.go wires the INBOUND agent-status signal
// (.prereview/llm-status.json) into the live UI. The agent (via the skill)
// writes what it is doing; a single background watcher fans each change out to
// every open tab via Session.TriggerAction, and Mount reads the file on connect
// so late/reconnecting tabs render current status. See llmstatus.go for the
// file format and controller.go for the guarded session handle.

// statusPath is the .prereview/llm-status.json path for this session's store.
func (c *PrereviewController) statusPath() string {
	return LLMStatusPath(c.CSVPath)
}

// applyLLMStatus refreshes the LLM-status fields on state from the status file.
// A missing file resets to idle; a malformed/torn read leaves the previous good
// value intact so the UI doesn't flicker (the agent writes atomically and the
// next poll self-corrects regardless).
func (c *PrereviewController) applyLLMStatus(state *PrereviewState) {
	s, err := readLLMStatus(c.statusPath())
	switch {
	case err == nil:
		state.LLMState, state.LLMMessage = s.State, s.Message
	case os.IsNotExist(err):
		state.LLMState, state.LLMMessage = "", ""
	default:
		slog.Debug("llm-status: keeping previous value on unreadable file", "err", err)
	}
}

// OnConnect captures the connecting group's Session so the background llm-status
// watcher can fan updates out to every open tab. It also refreshes the status
// fields from the file: LLMState is not persisted, so the restored connect-time
// state has it blank, and the connect render must reflect the current file (the
// same value the initial GET/SSR rendered) or a tab that opened mid-work would
// desync from its own server-rendered DOM.
func (c *PrereviewController) OnConnect(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.sessionMu.Lock()
	c.session = ctx.Session()
	c.sessionMu.Unlock()
	c.applyLLMStatus(&state)
	return state, nil
}

// LLMStatusChanged is dispatched by the watcher (via Session.TriggerAction) to
// every open tab when the status file changes. Fan-out re-runs this handler
// against each tab's OWN state, so it must re-read the file itself rather than
// rely on any payload.
func (c *PrereviewController) LLMStatusChanged(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	prev := state.LLMState
	c.applyLLMStatus(&state)
	switch {
	case prev == LLMStateWorking && state.LLMState == LLMStateDone:
		// The agent just finished a batch and edited files, so this tab's diff is
		// now stale. Offer a non-intrusive refresh (per-tab; cleared by RefreshDiff)
		// rather than auto-reloading, so the user keeps scroll + any draft.
		state.PendingRefresh = true
	case state.LLMState == LLMStateWorking:
		// A new batch started (e.g. the user handed off again while the agent was
		// working, and it's now processing the queued batch). Supersede any stale
		// refresh nudge — the diff is mid-edit again, so refreshing now would show a
		// half-applied state; the nudge re-appears when THIS batch finishes. Keeps
		// the working pill and the refresh bar mutually exclusive.
		state.PendingRefresh = false
	}
	return state, nil
}

// RefreshDiff rebuilds the file list + diff (picking up the agent's edits, since
// loadDiffCached invalidates on mtime) and re-anchors comments, then clears the
// pending-refresh nudge. It reuses Mount so the rebuild logic lives in exactly
// one place.
func (c *PrereviewController) RefreshDiff(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.PendingRefresh = false
	return c.Mount(state, ctx)
}

// WatchLLMStatus polls the status file and, whenever it changes, fans a
// LLMStatusChanged action out to every connected tab of the current session.
// One goroutine per server (not per connection): the status file is a single
// global signal and TriggerAction already fans out to all tabs, so a
// per-connection watcher would multiply re-renders by the number of tabs. It
// runs only in skill/stream mode (the only modes with an agent writing status)
// and returns when stop is closed (server shutdown).
func (c *PrereviewController) WatchLLMStatus(stop <-chan struct{}, poll time.Duration) {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	path := c.statusPath()
	last := statusFingerprint(path) // don't broadcast the pre-existing state at startup
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			fp := statusFingerprint(path)
			if fp == last {
				continue
			}
			last = fp
			c.sessionMu.Lock()
			session := c.session
			c.sessionMu.Unlock()
			if session == nil {
				continue // no tab has ever connected yet
			}
			if err := session.TriggerAction("LLMStatusChanged", nil); err != nil &&
				!errors.Is(err, livetemplate.ErrSessionDisconnected) {
				// ErrSessionDisconnected just means no tab is open right now; keep
				// watching so a later-opened tab still gets live updates.
				slog.Warn("llm-status: TriggerAction failed", "err", err)
			}
		}
	}
}
