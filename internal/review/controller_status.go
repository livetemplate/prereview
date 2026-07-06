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

// agentSignalFingerprint is a cheap combined change key for the inbound
// agent-written files the watcher fans out on: the global status file, the
// per-comment processed-markers file, and the LLM's suggestions file (#98). Any
// one changing flips the key, so a single watcher covers all three without extra
// goroutines — a new suggestion appears live with no server restart.
func (c *PrereviewController) agentSignalFingerprint() string {
	return statusFingerprint(c.statusPath()) + "|" +
		statusFingerprint(c.processedPath()) + "|" +
		statusFingerprint(c.suggestionsPath())
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
	// Same reasoning as applyLLMStatus above: the view prefs are no longer
	// lvt:"persist", so the restored connect-time state has them zeroed — reload
	// them from the per-user file so the connect render matches the initial
	// GET/SSR (the durable choice, not the default).
	c.applyUIPrefs(&state)
	return state, nil
}

// LLMStatusChanged is dispatched by the watcher (via Session.TriggerAction) to
// every open tab when the status file changes. Fan-out re-runs this handler
// against each tab's OWN state, so it must re-read the file itself rather than
// rely on any payload.
func (c *PrereviewController) LLMStatusChanged(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	prev := state.LLMState
	// Capture the suggestion IDs already loaded BEFORE applySuggestions overwrites
	// state.Suggestions, so we can tell a genuinely-new proposal from an unrelated
	// status/processed tick (this handler fans out on all three). Valid because
	// state.Suggestions is retained in the live session across dispatches (it's
	// re-zeroed only on reconnect, where Mount reloads it — hence this check lives
	// here, never in Mount, where the prior set is always empty).
	prevSuggestionIDs := suggestionIDSet(state.Suggestions)
	c.applyLLMStatus(&state)
	// The watcher also fires this on a processed.jsonl change (the agent marked a
	// comment "worked on"), so refresh the per-comment badges here too — a cheap
	// by-ID flag flip on the existing comments (no reload/re-anchor).
	c.applyProcessed(&state)
	// ...and on a suggestions.jsonl change (the LLM proposed an edit): reload the
	// suggestions and re-anchor the selected file's against the live in-memory
	// CurrentDiff, so a new suggestion box appears inline with no reload.
	c.applySuggestions(&state)
	c.relocateSuggestionsSelected(&state)
	// #116: if the agent submitted a brand-new suggestion while the inline boxes
	// were toggled off, reveal them — a hidden toggle must never silently swallow
	// fresh proposals. Gated on a genuinely-new ID (a revision re-appends the same
	// id and a status/processed tick adds none), so the reviewer can still keep
	// suggestions hidden across unrelated updates.
	if state.HideSuggestions && hasNewSuggestion(prevSuggestionIDs, state.Suggestions) {
		state.HideSuggestions = false
	}
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
	// Watch BOTH inbound agent signals with one goroutine: the global status file
	// (llm-status.json) and the per-comment markers (processed.jsonl). Either
	// changing fans out LLMStatusChanged, which refreshes status AND the "worked
	// on" badges. Combining the fingerprints keeps this a single cheap stat pair.
	last := c.agentSignalFingerprint() // don't broadcast the pre-existing state at startup
	// Track the agent's status state so we can checkpoint a version on the
	// working→done transition. openStore removes llm-status.json on launch, so a
	// fresh session starts idle and the first real "done" is a genuine transition.
	prevState := c.currentLLMState()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			fp := c.agentSignalFingerprint()
			if fp == last {
				continue
			}
			last = fp
			// Checkpoint an artifact version when the agent finishes a batch
			// (working→done): its edits are now on disk, so this is the natural
			// "one version per work-cycle" boundary. Done in this single per-server
			// goroutine, BEFORE the session/fan-out guard below, so a version is
			// recorded exactly once even if no tab is open — unlike the per-tab
			// LLMStatusChanged handler, which would checkpoint once per open tab.
			curState := c.currentLLMState()
			if prevState == LLMStateWorking && curState == LLMStateDone {
				c.checkpointVersions(VersionTriggerLLMDone)
			}
			prevState = curState
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
