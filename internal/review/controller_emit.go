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
	"slices"
	"time"
)

// emitDebounce coalesces a burst of mutations (typing then save, multi-select,
// rapid accept/reject) into a single snapshot emit. ~400ms feels immediate
// without emitting on every keystroke-save. A var (not const) so tests can
// shrink it.
var emitDebounce = 400 * time.Millisecond

// emitSnapshotHook, when non-nil, runs inside emitSnapshot right after it reads the CSV.
// A test seam (nil in production): it lets a test hold a REAL emit in flight and land a
// concurrent save in exactly the window #228 is about.
var emitSnapshotHook func()

// scheduleEmit (re)arms the debounced snapshot emit after a mutation. Stream
// mode only. The single resettable timer coalesces a burst into one emit. While an
// emit is in flight it only records emitPending, and the emit re-arms on its way out
// (#228). Arming now would let two emits overlap and put an older full snapshot on the
// wire after a newer one; doing nothing would leave the save unsent until the next one.
func (c *PrereviewController) scheduleEmit() {
	if c.Emitter == nil || c.emitDisabled.Load() {
		return
	}
	c.emitMu.Lock()
	defer c.emitMu.Unlock()
	if c.inEmit {
		c.emitPending = true
		return
	}
	c.armLocked()
}

// armLocked starts (or restarts) the debounce timer. The caller holds emitMu.
func (c *PrereviewController) armLocked() {
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
	c.emitPending = false
	if c.emitTimer != nil {
		c.emitTimer.Stop()
		c.emitTimer = nil
	}
}

// emitSnapshot re-reads the source-of-truth files, re-anchors, and emits the
// full actionable snapshot. It rebuilds state from disk (exactly like Mount), so
// it is decoupled from any per-tab request state and runs safely in the debounce
// timer goroutine. Gated on: no emitter (non-stream), session ended, or paused
// (the reviewer is re-steering — hold the batch until resume). A mutation landing
// while it runs is re-armed when it finishes (see scheduleEmit).
func (c *PrereviewController) emitSnapshot() {
	if c.Emitter == nil || c.emitDisabled.Load() || c.isPaused() {
		return
	}
	c.emitMu.Lock()
	c.inEmit = true
	c.emitMu.Unlock()
	// Clearing inEmit and re-arming happen in ONE critical section: a re-arm placed
	// anywhere after it would race a scheduleEmit that saw inEmit still set.
	defer func() {
		c.emitMu.Lock()
		defer c.emitMu.Unlock()
		c.inEmit = false
		if c.emitPending && !c.emitDisabled.Load() {
			c.armLocked()
		}
		c.emitPending = false
	}()

	// This state is built here, NOT via Mount, so the review scope has to be carried
	// over explicitly (#171) — without it the agent's snapshot would still stream the
	// previous file's work even though the UI no longer shows it.
	st := &PrereviewState{Base: c.Base, SingleFile: c.SingleFile}
	st.Comments = c.loadCommentsFromDisk()
	loaded := slices.Clone(st.Comments) // what persistAnchors compares against
	if emitSnapshotHook != nil {
		emitSnapshotHook()
	}
	// #203: without this, Processed is always false on the emit state and the settled
	// check in actionableComments can never fire — the agent keeps its own finished work.
	c.applyProcessed(st)
	c.applySuggestions(st)
	c.applyDecisions(st)
	st.ThreadEntries = loadThreads(c.CSVPath) // #149: the conversation on each target
	st.Applied = loadAppliedSet(c.CSVPath)    // #159: applied acks drop from the snapshot
	// Re-anchor comments against fresh disk (base-safe, #121). The self-heal MERGES into
	// the CSV: st.Comments is what was on disk at the top, not a write-back buffer.
	if c.relocateAll(st) {
		if err := c.persistAnchors(loaded, st.Comments); err != nil {
			slog.Warn("self-heal persist (emit)", "err", err)
		}
	}
	c.relocateSuggestionsAll(st) // re-anchor suggestions likewise
	c.applyQuiz(st)              // #191: advisory quiz results (never gates)
	if err := c.Emitter.EmitSnapshot(st.scopedComments(), st.scopedSuggestions(), st.DecisionsBySuggestion(), st.Threads(), st.Applied, st.QuizResults(), c.isPaused(), time.Now()); err != nil {
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
