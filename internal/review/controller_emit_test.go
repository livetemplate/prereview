package review

import (
	"testing"
	"time"
)

func fastDebounce(t *testing.T) {
	t.Helper()
	old := emitDebounce
	emitDebounce = 15 * time.Millisecond
	t.Cleanup(func() { emitDebounce = old })
}

// TestEmit_DebounceCoalesces: a burst of mutations coalesces into ONE snapshot.
func TestEmit_DebounceCoalesces(t *testing.T) {
	fastDebounce(t)
	c, buf, _ := newStreamController(t)
	if err := c.persist([]Comment{regionComment("a", "one")}); err != nil {
		t.Fatal(err)
	}
	// Simulate a rapid burst of further mutations within the debounce window.
	for i := 0; i < 5; i++ {
		c.scheduleEmit()
	}
	time.Sleep(60 * time.Millisecond)

	evs := decodeEvents(t, buf.Bytes())
	if len(evs) != 1 {
		t.Fatalf("a burst should coalesce to 1 snapshot, got %d: %+v", len(evs), evs)
	}
	if got := evs[0].CommentList(); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("snapshot = %+v, want [a]", got)
	}
}

// TestEmit_PausedSuppressesThenResumeFlushes: while paused nothing is emitted;
// resume ships the accumulated batch in one snapshot.
func TestEmit_PausedSuppressesThenResumeFlushes(t *testing.T) {
	fastDebounce(t)
	c, buf, _ := newStreamController(t)
	c.setAgentPaused(true)

	if err := c.persist([]Comment{regionComment("a", "one"), regionComment("b", "two")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if evs := decodeEvents(t, buf.Bytes()); len(evs) != 0 {
		t.Fatalf("paused agent must not emit, got %d events", len(evs))
	}

	// Resume → the held batch ships.
	if _, err := c.ResumeAgent(PrereviewState{}, regionCtx("resumeAgent", nil)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	evs := decodeEvents(t, buf.Bytes())
	if len(evs) != 1 {
		t.Fatalf("resume should flush one snapshot, got %d", len(evs))
	}
	if got := evs[0].CommentList(); len(got) != 2 {
		t.Errorf("resumed snapshot should carry both comments, got %d", len(got))
	}
}

// TestEmit_NoSnapshotAfterSessionEnd: a debounced emit armed just before End
// session must NOT fire a snapshot after session_end (the skill's terminator).
func TestEmit_NoSnapshotAfterSessionEnd(t *testing.T) {
	fastDebounce(t)
	c, buf, _ := newStreamController(t)

	// Arm a debounced emit, then end the session immediately (within the window).
	if err := c.persist([]Comment{regionComment("a", "one")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EndSession(PrereviewState{Comments: []Comment{regionComment("a", "one")}}, regionCtx("endSession", nil)); err != nil {
		t.Fatal(err)
	}
	// Wait well past the debounce window: the pending timer must have been stopped.
	time.Sleep(60 * time.Millisecond)

	evs := decodeEvents(t, buf.Bytes())
	if len(evs) == 0 {
		t.Fatal("expected a final handoff + session_end")
	}
	// The LAST event must be session_end — nothing may follow it.
	last := evs[len(evs)-1]
	if last.Event != "session_end" {
		t.Errorf("last event must be session_end, got %q (a late snapshot leaked)", last.Event)
	}
	// And there must be exactly one session_end.
	nEnd := 0
	for _, e := range evs {
		if e.Event == "session_end" {
			nEnd++
		}
	}
	if nEnd != 1 {
		t.Errorf("want exactly 1 session_end, got %d", nEnd)
	}
}

// TestEmit_DraftExcludedFromSnapshot: a draft comment is held out of the emitted
// snapshot until enqueued.
func TestEmit_DraftExcludedFromSnapshot(t *testing.T) {
	fastDebounce(t)
	c, buf, _ := newStreamController(t)
	draft := regionComment("d", "held")
	draft.Draft = true
	if err := c.persist([]Comment{regionComment("a", "queued"), draft}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)

	evs := decodeEvents(t, buf.Bytes())
	if len(evs) != 1 {
		t.Fatalf("want 1 snapshot, got %d", len(evs))
	}
	got := evs[0].CommentList()
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("draft must be excluded; snapshot = %+v, want only [a]", got)
	}
}
