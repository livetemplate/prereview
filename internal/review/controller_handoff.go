package review

import (
	"fmt"
	"github.com/livetemplate/livetemplate"
	"time"
)

// flushHandoff re-anchors every commented file, persists the CSV, and (in agent
// mode) emits a handoff event carrying the full actionable snapshot. Shared by
// the emit path and EndSession. The CSV only becomes a contract at handoff, so
// re-anchoring here gives the consumer accurate line numbers (and an explicit
// anchor_status=outdated where it cannot be trusted); the stream snapshot is
// filtered to actionable rows and the consumer dedupes by id.
func (c *PrereviewController) flushHandoff(state *PrereviewState) error {
	c.relocateAll(state)
	if err := c.persist(state.Comments); err != nil {
		return fmt.Errorf("final csv write: %w", err)
	}
	// Re-anchor suggestions across all files too, so the decisions we emit carry
	// accurate line numbers + anchor_status: an accepted edit the LLM already
	// applied re-anchors as outdated and drops from the snapshot (#98 Phase 3).
	c.relocateSuggestionsAll(state)
	if c.Emitter != nil {
		if err := c.Emitter.EmitHandoff(state.Comments, state.Suggestions, state.DecisionsBySuggestion(), c.isPaused(), time.Now()); err != nil {
			return fmt.Errorf("emit handoff event: %w", err)
		}
	}
	return nil
}

// EndSession is the agent-mode terminator. It first flushes a final handoff
// snapshot — so comments left since the last emitted snapshot still reach the
// consumer (dedup-by-id makes a redundant snapshot harmless, and the alternative would
// silently strand them in the CSV but never the stream) — then emits the single
// session_end event (the only event the consumer loop treats as "stop") and
// shuts the server down on the same delay as Quit, so the framework renders the
// "session ended" banner before the WebSocket is torn down. The LLM's
// background job completing is the second, redundant terminator.
func (c *PrereviewController) EndSession(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	// Stop the debounced emitter and block new emits BEFORE the final flush, so a
	// queued snapshot can't fire AFTER session_end — the skill's only stop signal.
	// The synchronous flushHandoff below is the last, authoritative snapshot.
	c.emitDisabled.Store(true)
	c.stopPendingEmit()
	if err := c.flushHandoff(&state); err != nil {
		return state, err
	}
	if c.Emitter != nil {
		if err := c.Emitter.EmitSessionEnd(time.Now()); err != nil {
			return state, fmt.Errorf("emit session_end event: %w", err)
		}
	}
	state.SessionEnded = true
	state.LastDeletedComment = nil
	c.requestShutdown()
	return state, nil
}

// Quit gracefully shuts the HTTP server down. The actual shutdown is
// dispatched on a delay so the framework gets to render `Quitting=true`
// back to the client before the WebSocket is torn down — otherwise the
// browser sees a sudden disconnect with no UI feedback.
func (c *PrereviewController) Quit(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	// Stop emitting on the way out (same terminal reasoning as EndSession).
	c.emitDisabled.Store(true)
	c.stopPendingEmit()
	state.Quitting = true
	c.requestShutdown()
	return state, nil
}

// requestShutdown dispatches the graceful shutdown on a delay so the framework
// can render the final state (Quitting / SessionEnded) back to the client
// before the WebSocket is torn down. No-op when ShutdownReq is unset (tests).
func (c *PrereviewController) requestShutdown() {
	if c.ShutdownReq == nil {
		return
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		select {
		case c.ShutdownReq <- struct{}{}:
		default:
		}
	}()
}
