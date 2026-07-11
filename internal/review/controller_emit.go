package review

// controller_emit.go is the continuous-enqueue emission engine (#119). Instead
// of shipping the actionable set only on an explicit "Hand off" click, every
// comment/suggestion mutation (debounced) emits a fresh full snapshot, so the
// agent picks work up as the reviewer goes. The wire event is unchanged (a full
// actionable snapshot, deduped by id on the consumer side); it just fires more
// often. Emission is gated on pause (held while the reviewer re-steers) and
// stopped at session end.

import (
	"log/slog"
	"os"
	"time"
)

// emitDebounce coalesces a burst of mutations (typing then save, multi-select,
// rapid accept/reject) into a single snapshot emit. ~400ms feels immediate
// without emitting on every keystroke-save. A var (not const) so tests can
// shrink it.
var emitDebounce = 400 * time.Millisecond

// scheduleEmit (re)arms the debounced snapshot emit after a mutation. Stream
// mode only. The single resettable timer coalesces a burst into one emit. It is
// a no-op while an emit is in flight — so the self-heal persist inside
// emitSnapshot can't reschedule and spin — and after the session has ended.
func (c *PrereviewController) scheduleEmit() {
	if c.Emitter == nil || c.inEmit.Load() || c.emitDisabled.Load() {
		return
	}
	c.emitMu.Lock()
	defer c.emitMu.Unlock()
	if c.emitTimer != nil {
		c.emitTimer.Stop()
	}
	c.emitTimer = time.AfterFunc(emitDebounce, c.emitSnapshot)
}

// stopPendingEmit cancels any armed debounce timer. EndSession calls it (and
// sets emitDisabled) before its synchronous final flush, so a queued emit can't
// fire a snapshot AFTER end — the skill's only stop signal.
func (c *PrereviewController) stopPendingEmit() {
	c.emitMu.Lock()
	defer c.emitMu.Unlock()
	if c.emitTimer != nil {
		c.emitTimer.Stop()
		c.emitTimer = nil
	}
}

// emitSnapshot re-reads the source-of-truth files, re-anchors, and emits the
// full actionable snapshot. It rebuilds state from disk (exactly like Mount), so
// it is decoupled from any per-tab request state and runs safely in the debounce
// timer goroutine. Gated on: no emitter (non-stream), session ended, or paused
// (the reviewer is re-steering — hold the batch until resume). inEmit guards the
// self-heal persist that relocateAll may do from re-arming the timer.
func (c *PrereviewController) emitSnapshot() {
	if c.Emitter == nil || c.emitDisabled.Load() || c.isPaused() {
		return
	}
	c.inEmit.Store(true)
	defer c.inEmit.Store(false)

	st := &PrereviewState{Base: c.Base}
	st.Comments = c.loadCommentsFromDisk()
	c.applySuggestions(st)
	c.applyDecisions(st)
	st.ThreadEntries = loadThreads(c.CSVPath) // #149: the conversation on each target
	st.Applied = loadAppliedSet(c.CSVPath)    // #159: applied acks drop from the snapshot
	c.relocateAll(st)                         // re-anchor comments against fresh disk (base-safe, #121)
	c.relocateSuggestionsAll(st)              // re-anchor suggestions likewise
	if err := c.Emitter.EmitSnapshot(st.Comments, st.Suggestions, st.DecisionsBySuggestion(), st.Threads(), st.Applied, c.isPaused(), time.Now()); err != nil {
		slog.Warn("emit snapshot", "err", err)
	}
}

// isPaused reports whether the agent is paused — the .prereview/paused marker
// exists. Set by a rollback (#90) or the pause/resume toggle (#119). While
// paused, mutations still persist (nothing is lost) but no snapshot is emitted;
// resume re-arms an emit so the accumulated batch ships in one go.
func (c *PrereviewController) isPaused() bool {
	_, err := os.Stat(c.pausedMarkerPath())
	return err == nil
}
