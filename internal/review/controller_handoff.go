package review

import (
	"fmt"
	"github.com/livetemplate/livetemplate"
	"time"
)

// flushHandoff re-anchors every commented file, persists the CSV, and (in
// stream mode) emits a handoff event carrying the full actionable snapshot.
// Shared by HandOff and EndSession. The CSV only becomes a contract at
// handoff, so re-anchoring here gives the consumer accurate line numbers (and
// an explicit anchor_status=outdated where it cannot be trusted); the stream
// snapshot is filtered to actionable rows and the consumer dedupes by id.
func (c *PrereviewController) flushHandoff(state *PrereviewState) error {
	c.relocateAll(state)
	if err := c.persist(state.Comments); err != nil {
		return fmt.Errorf("final csv write: %w", err)
	}
	if c.Emitter != nil {
		if err := c.Emitter.EmitHandoff(state.Comments, time.Now()); err != nil {
			return fmt.Errorf("emit handoff event: %w", err)
		}
	}
	return nil
}

// HandOff is the skill-mode "I'm finished reviewing" handoff. Flushes the CSV
// (+ a stream handoff event in stream mode), then writes the DONE marker AFTER
// the CSV is fsynced + renamed. The skill polls for the marker, so writing
// DONE before the CSV is durable would let it race and read a half-written
// file. Server keeps running afterwards so the user can keep editing.
func (c *PrereviewController) HandOff(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if err := c.flushHandoff(&state); err != nil {
		return state, err
	}
	if err := writeDoneMarker(c.DonePath, c.CSVPath); err != nil {
		return state, fmt.Errorf("write done marker: %w", err)
	}
	state.DoneWritten = true
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// EndSession is the stream-mode terminator. It first flushes a final handoff
// snapshot — so comments left since the last Hand off still reach the consumer
// (dedup-by-id makes a redundant snapshot harmless, and the alternative would
// silently strand them in the CSV but never the stream) — then emits the single
// session_end event (the only event the consumer loop treats as "stop") and
// shuts the server down on the same delay as Quit, so the framework renders the
// "session ended" banner before the WebSocket is torn down. The LLM's
// background job completing is the second, redundant terminator.
func (c *PrereviewController) EndSession(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
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
