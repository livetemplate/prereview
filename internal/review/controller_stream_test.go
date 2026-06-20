package review

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
)

// newStreamController returns a stream-mode controller whose emitter writes to
// an in-memory buffer (no file mirror), wired to a temp CSV store. External
// mode keeps region comments diff-free so HandOff's relocateAll is a no-op.
func newStreamController(t *testing.T) (*PrereviewController, *bytes.Buffer, chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "comments.csv")
	buf := &bytes.Buffer{}
	shutdown := make(chan struct{}, 1)
	c := &PrereviewController{
		ExternalMode: true,
		CSVPath:      csvPath,
		DonePath:     filepath.Join(dir, "DONE"),
		CSVWriter:    csv.NewWriter(csvPath),
		SkillMode:    true,
		StreamMode:   true,
		Emitter:      NewEventStream(buf, ""),
		ShutdownReq:  shutdown,
	}
	return c, buf, shutdown
}

func regionComment(id, body string) Comment {
	return Comment{ID: id, Kind: commentKindRegion, URL: "/p", Area: Area{X: 0.1, Y: 0.1, W: 0.2, H: 0.1}, Body: body, Created: fixedTS}
}

// TestHandOff_StreamEmitsActionableSnapshot pins that a Hand off click emits
// exactly one handoff event whose snapshot is the actionable comments only —
// resolved and anchor-outdated rows are pruned.
func TestHandOff_StreamEmitsActionableSnapshot(t *testing.T) {
	c, buf, _ := newStreamController(t)
	st := PrereviewState{Comments: []Comment{
		regionComment("keep", "fix this"),
		func() Comment { c := regionComment("resolved", "done"); c.Resolved = true; return c }(),
		func() Comment { c := regionComment("outdated", "gone"); c.AnchorStatus = anchorOutdated; return c }(),
	}}

	if _, err := c.HandOff(st, regionCtx("handOff", nil)); err != nil {
		t.Fatalf("HandOff: %v", err)
	}

	evs := decodeEvents(t, buf.Bytes())
	if len(evs) != 1 || evs[0].Event != "handoff" {
		t.Fatalf("want 1 handoff event, got %+v", evs)
	}
	if evs[0].Seq != 0 {
		t.Errorf("first handoff seq = %d, want 0", evs[0].Seq)
	}
	if got := evs[0].CommentList(); len(got) != 1 || got[0].ID != "keep" {
		t.Fatalf("snapshot = %+v, want only [keep]", got)
	}
}

// TestHandOff_StreamSeqIncrements pins that successive Hand off clicks emit
// distinguishable rounds (monotonic seq) — the fix for the idempotent-DONE
// blind spot, and that resolving between rounds prunes the snapshot.
func TestHandOff_StreamSeqIncrements(t *testing.T) {
	c, buf, _ := newStreamController(t)
	st := PrereviewState{Comments: []Comment{regionComment("a", "one"), regionComment("b", "two")}}

	st, err := c.HandOff(st, regionCtx("handOff", nil))
	if err != nil {
		t.Fatalf("first HandOff: %v", err)
	}
	// User resolves "a" in the UI, then hands off again.
	st.Comments[0].Resolved = true
	if _, err := c.HandOff(st, regionCtx("handOff", nil)); err != nil {
		t.Fatalf("second HandOff: %v", err)
	}

	evs := decodeEvents(t, buf.Bytes())
	if len(evs) != 2 {
		t.Fatalf("want 2 handoff events, got %d", len(evs))
	}
	if evs[0].Seq != 0 || evs[1].Seq != 1 {
		t.Errorf("seqs = (%d,%d), want (0,1)", evs[0].Seq, evs[1].Seq)
	}
	if got := evs[0].CommentList(); len(got) != 2 {
		t.Errorf("round 1 = %d comments, want 2", len(got))
	}
	if got := evs[1].CommentList(); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("round 2 = %+v, want only [b] after resolving a", got)
	}
}

// TestEndSession_FlushesThenTerminatesAndShutsDown pins the terminator
// sequence: End session flushes a final handoff snapshot (so comments left
// since the last Hand off still reach the consumer — the footgun fix), then
// emits session_end, sets the SessionEnded banner flag, and requests a
// graceful shutdown.
func TestEndSession_FlushesThenTerminatesAndShutsDown(t *testing.T) {
	c, buf, shutdown := newStreamController(t)

	// A comment exists that was never explicitly handed off.
	st, err := c.EndSession(PrereviewState{Comments: []Comment{regionComment("late", "left at the end")}}, regionCtx("endSession", nil))
	if err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if !st.SessionEnded {
		t.Error("EndSession should set SessionEnded")
	}

	evs := decodeEvents(t, buf.Bytes())
	if len(evs) != 2 {
		t.Fatalf("want [handoff, session_end], got %+v", evs)
	}
	if h := evs[0].CommentList(); evs[0].Event != "handoff" || len(h) != 1 || h[0].ID != "late" {
		t.Errorf("End session should flush the pending comment first, got %+v", evs[0])
	}
	if evs[1].Event != "session_end" {
		t.Errorf("terminator must be session_end, got %q", evs[1].Event)
	}

	select {
	case <-shutdown:
	case <-time.After(2 * time.Second):
		t.Fatal("EndSession did not request shutdown")
	}
}

// TestHandOff_NonStreamEmitsNothing pins the default (non-stream) regression:
// a nil emitter is safe and HandOff still writes the DONE marker.
func TestHandOff_NonStreamEmitsNothing(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "comments.csv")
	c := &PrereviewController{
		ExternalMode: true,
		CSVPath:      csvPath,
		DonePath:     filepath.Join(dir, "DONE"),
		CSVWriter:    csv.NewWriter(csvPath),
		SkillMode:    true,
		// Emitter nil, StreamMode false.
	}
	st, err := c.HandOff(PrereviewState{Comments: []Comment{regionComment("a", "x")}}, regionCtx("handOff", nil))
	if err != nil {
		t.Fatalf("HandOff with nil emitter: %v", err)
	}
	if !st.DoneWritten {
		t.Error("HandOff should still write DONE in non-stream mode")
	}
}
