package review

import (
	"errors"
	"log/slog"
	"os"
	"strings"
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
// per-comment processed-markers file, the LLM's suggestions file (#98), the
// thread replies, the applied/reverted acks, and the quiz file (#191). Any one
// changing flips the key, so a single watcher covers them all without extra
// goroutines — a new suggestion or quiz appears live with no server restart.
func (c *PrereviewController) agentSignalFingerprint() string {
	return statusFingerprint(c.statusPath()) + "|" +
		statusFingerprint(c.processedPath()) + "|" +
		statusFingerprint(c.suggestionsPath()) + "|" +
		statusFingerprint(AgentRepliesPath(c.CSVPath)) + "|" +
		statusFingerprint(AppliedPath(c.CSVPath)) + "|" +
		statusFingerprint(RevertedPath(c.CSVPath)) + "|" +
		statusFingerprint(c.quizPath())
}

// reviewedFilesFingerprint is a cheap mtime+size key over the files under REVIEW
// (versionScope) — the deterministic "the reviewed file was edited" signal,
// independent of any agent command. An empty scope (external mode, or no version
// store) yields "", so the watcher simply never fires on a reviewed-file change
// there. It's the same shape as agentSignalFingerprint, just over review content
// instead of the agent's sidecar files.
//
// Cost: a stat per scoped file each poll. In single-file / NoGit mode (how muster
// reviews a plan) that is one stat; recomputing versionScope every tick keeps a
// newly-touched file's first edit detectable (it enters scope only once touched).
// In git mode versionScope runs `git` name-status per tick — a fine background
// cost for a local tool, and the sole reason to revisit if a huge repo ever drags.
func (c *PrereviewController) reviewedFilesFingerprint() string {
	var b strings.Builder
	for _, f := range c.versionScope() {
		b.WriteString(statusFingerprint(f.AbsPath))
		b.WriteByte('|')
	}
	return b.String()
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
	// ...and on an agent-replies.jsonl change (the agent posted a thread reply, #149):
	// reload the merged threads so the note appears under its card with no reload.
	state.ThreadEntries = loadThreads(c.CSVPath)
	// ...and on an applied.jsonl change (#159): reload the applied set so an accepted
	// suggestion flips to "applied" live.
	state.Applied = loadAppliedSet(c.CSVPath)
	// ...and on a quiz.jsonl change (#191): reload the quizzes and re-ground them
	// against the live diff, so a freshly generated quiz appears with no reload.
	c.applyQuiz(&state)
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
	// A reviewed file was edited on disk since this tab last rebuilt its diff
	// (deterministic, no agent command). Offer the same non-intrusive refresh the
	// working→done path does — the view stays frozen (scroll/drafts survive) until
	// the reviewer refreshes, which rebuilds and re-anchors. Placed after the
	// switch so a raw edit surfaces even when the agent never wrote a status.
	if c.reviewedGen.Load() != state.SeenReviewedGen {
		state.PendingRefresh = true
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
	// Also fingerprint the REVIEWED files, so a raw edit to the plan (no agent
	// command) is a first-class trigger alongside the sidecar signals — the agent
	// forgetting to write a status can no longer hide the fact that it edited.
	lastReviewed := c.reviewedFilesFingerprint()
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
			reviewed := c.reviewedFilesFingerprint()
			if fp == last && reviewed == lastReviewed {
				continue
			}
			reviewedChanged := reviewed != lastReviewed
			last, lastReviewed = fp, reviewed
			if reviewedChanged {
				// A reviewed file changed on disk — deterministic, no agent command.
				// Bump the gen so open tabs surface it (the refresh nudge) in
				// LLMStatusChanged. The re-anchor/rebuild happens on refresh, like the
				// working→done path, so the view stays frozen until the reviewer acts.
				c.reviewedGen.Add(1)
			}
			// Checkpoint an artifact version when the agent finishes a batch
			// (working→done): its edits are now on disk, so this is the natural
			// "one version per work-cycle" boundary. Done in this single per-server
			// goroutine, BEFORE the session/fan-out guard below, so a version is
			// recorded exactly once even if no tab is open — unlike the per-tab
			// LLMStatusChanged handler, which would checkpoint once per open tab.
			// Read the FULL status (not just state) so the agent's done-message rides
			// into the checkpoint as the version's changelog (#155). A torn/missing
			// read yields an empty status (State "") — same as idle.
			cur, _ := readLLMStatus(c.statusPath())
			if prevState == LLMStateWorking && cur.State == LLMStateDone {
				c.checkpointVersions(VersionTriggerLLMDone, cur.Message)
			}
			// A reviewed file was edited outside a status batch — the agent never
			// went "working", or wrote no status at all. Checkpoint a version so the
			// reviewer still gets the diff of what changed, deterministically. Ordered
			// AFTER the llm-done checkpoint so an edit+done in the same tick keeps the
			// richer done-changelog version (this becomes a SHA no-op). Suppressed
			// while working: the pending done will checkpoint it with its message.
			if reviewedChanged && cur.State != LLMStateWorking {
				c.checkpointVersions(VersionTriggerFileEdit, "")
			}
			prevState = cur.State
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
