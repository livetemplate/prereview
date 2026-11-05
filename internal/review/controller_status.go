package review

import (
	"errors"
	"fmt"
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
//
// agent-cursor.json is deliberately NOT one of them: a blocked watch rewrites it
// every heartbeat, so its mtime would make this a PERIODIC re-render of every tab.
// The watcher keys on AgentPresence instead, which changes only on a state
// transition (#194).
func (c *PrereviewController) agentSignalFingerprint() string {
	return signalFingerprint(statusFingerprint(c.statusPath()), c)
}

// signalFingerprint joins the status file's fingerprint (computed by the caller,
// which the watcher also needs on its own) with every other agent sidecar's.
func signalFingerprint(statusFP string, c *PrereviewController) string {
	return statusFP + "|" +
		statusFingerprint(c.processedPath()) + "|" +
		statusFingerprint(c.suggestionsPath()) + "|" +
		statusFingerprint(AgentRepliesPath(c.CSVPath)) + "|" +
		statusFingerprint(AppliedPath(c.CSVPath)) + "|" +
		statusFingerprint(RevertedPath(c.CSVPath)) + "|" +
		statusFingerprint(c.quizPath())
}

// AgentPresence is what the server can OBSERVE about the queue's consumer
// (#194), as opposed to what the agent volunteers. It is server-wide (the
// watcher computes it once per tick and every tab copies it) and doubles as a
// fan-out key, so every field changes only on a state transition or at minute
// resolution — never per heartbeat.
type AgentPresence struct {
	Attached   bool   `json:"attached"`    // a blocking watch is on the stream right now
	PickedUp   bool   `json:"picked_up"`   // the queue has been read at least once
	Polled     bool   `json:"polled"`      // the last read was a one-shot `comments --json`
	Behind     bool   `json:"behind"`      // events were emitted after the last delivery
	PickupAt   string `json:"pickup_at"`   // when it was read, while not attached
	Quiet      bool   `json:"quiet"`       // not attached, and no agent signal for AgentStallAfter
	QuietSince string `json:"quiet_since"` // the last agent signal, while not attached
}

func (p AgentPresence) key() string { return fmt.Sprintf("%+v", p) }

// agentPresence derives the presence from agent-cursor.json. lastSignal is the
// watcher's record of the agent's last other signal (a sidecar or reviewed-file
// change), so the stall clock never re-scans the disk. A missing cursor (no
// reader yet, or an older agent binary) is simply "never picked up".
func (c *PrereviewController) agentPresence(now, lastSignal time.Time) AgentPresence {
	cur, mtime, err := readAgentCursor(AgentCursorPath(c.CSVPath))
	if err != nil {
		return AgentPresence{}
	}
	p := AgentPresence{PickedUp: cur.PickedUp(), Polled: cur.Mode == AgentCursorPoll}
	if p.Attached = cur.Attached(mtime, now); p.Attached {
		return p
	}
	p.Behind = !p.Polled && cur.DeliveredSeq < c.Emitter.LastSeq()
	p.PickupAt = clockMinute(mtime)
	last := mtime
	if lastSignal.After(last) {
		last = lastSignal
	}
	p.QuietSince = clockMinute(last)
	p.Quiet = now.Sub(last) >= AgentStallAfter
	return p
}

// clockMinute is the minute-resolution wall-clock label, in the thread
// timestamps' layout — minute resolution also bounds how often it can change.
func clockMinute(t time.Time) string { return t.Local().Format(clockLayout) }

// applyAgentCursor copies the watcher's cached presence onto state for the
// zero-arg phase methods (state_queue.go); it computes one only before the
// watcher's first tick (or outside agent mode, where nothing ticks).
func (c *PrereviewController) applyAgentCursor(state *PrereviewState) {
	if p := c.presence.Load(); p != nil {
		state.Agent = *p
		return
	}
	state.Agent = c.agentPresence(time.Now(), time.Time{})
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
	state.EditsSettling = c.editsSettling.Load() && state.LLMState != LLMStateWorking
	// Every caller (Mount, OnConnect, the watcher's fan-out) wants the observed
	// consumer state alongside the agent's own echo.
	c.applyAgentCursor(state)
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
	// A reviewed-file edit SETTLED since this tab last rebuilt its diff (the
	// watcher bumps the gen once per settled run, #198). Offer the same
	// non-intrusive refresh the working→done path does — the view stays frozen
	// (scroll/drafts survive) until the reviewer refreshes, which rebuilds and
	// re-anchors. Placed after the switch so a raw edit surfaces even when the
	// agent never wrote a status — but never while working, so the suppression
	// above holds (the done that ends the batch re-arms it).
	if c.reviewedGen.Load() != state.SeenReviewedGen && state.LLMState != LLMStateWorking {
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
	// #194: the agent's last signal, for the stall clock — stamped here, where every
	// sidecar / reviewed-file change is already seen, instead of re-scanning mtimes.
	var lastSignal time.Time
	startPresence := c.agentPresence(time.Now(), lastSignal)
	c.presence.Store(&startPresence)
	lastPresence := startPresence.key()
	// #198: reviewed-file edits are held as PENDING until they settle, so a burst
	// of writes is one version and one refresh nudge, not one per write.
	var editsPending bool
	var lastEditAt time.Time
	lastStatusFP := statusFingerprint(c.statusPath())
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			// The status file's own fingerprint too: "done arrived" must mean a status
			// WRITE, not another sidecar (processed.jsonl…) moving under an old done.
			statusFP := statusFingerprint(c.statusPath())
			fp := signalFingerprint(statusFP, c)
			reviewed := c.reviewedFilesFingerprint()
			signalChanged, reviewedChanged := fp != last, reviewed != lastReviewed
			if signalChanged || reviewedChanged {
				lastSignal = now
			}
			pres := c.agentPresence(now, lastSignal)
			c.presence.Store(&pres)
			presenceKey := pres.key()
			last, lastReviewed = fp, reviewed

			// Read the FULL status (not just state) so the agent's done-message rides
			// into the checkpoint as the version's changelog (#155). Only on a change:
			// otherwise the state is what the previous tick saw. A torn/missing read
			// yields an empty status (State "") — same as idle.
			statusWritten := statusFP != lastStatusFP
			lastStatusFP = statusFP
			cur := LLMStatus{State: prevState}
			if statusWritten {
				cur, _ = readLLMStatus(c.statusPath())
			}

			// A reviewed file changed on disk — deterministic, no agent command. Open
			// (or extend) the pending window, unless the content already matches the
			// newest version: an mtime-only touch, or the reviewer's own rollback
			// (RestoreVersion checkpoints before the watcher sees its write).
			if reviewedChanged && (editsPending || c.Versions == nil || !c.Versions.MatchesLatest(c.versionScope())) {
				editsPending, lastEditAt = true, now
			}

			// Close the window — ONE checkpoint (which bumps the nudge gen only if it
			// recorded new content) — on whichever comes first: a "done" write (the
			// agent says the batch is finished; no prior "working" required), or
			// EditSettle of quiet while the agent isn't "working" (a human edit, or an
			// agent that writes no status). While working, only the done closes it.
			// The working→done boundary with nothing pending (out-of-scope edits)
			// still records its version, as before. This runs in the single
			// per-server goroutine, BEFORE the session guard below, so a version is
			// recorded exactly once even with no tab open.
			switch {
			case statusWritten && cur.State == LLMStateDone && (editsPending || prevState == LLMStateWorking):
				c.checkpointVersions(VersionTriggerLLMDone, cur.Message)
				editsPending = false
			case editsPending && cur.State != LLMStateWorking && now.Sub(lastEditAt) >= EditSettle:
				c.checkpointVersions(VersionTriggerFileEdit, "")
				editsPending = false
			}
			settlingChanged := c.editsSettling.Swap(editsPending) != editsPending
			prevState = cur.State

			if !signalChanged && !reviewedChanged && !settlingChanged && presenceKey == lastPresence {
				continue
			}
			lastPresence = presenceKey
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
