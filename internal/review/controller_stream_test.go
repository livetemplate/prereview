package review

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
)

// newStreamController returns an agent-mode controller whose emitter writes to
// an in-memory buffer (no file mirror), wired to a temp CSV store. External
// mode keeps region comments diff-free so flushHandoff's relocateAll is a no-op.
func newStreamController(t *testing.T) (*PrereviewController, *bytes.Buffer, chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "comments.csv")
	buf := &bytes.Buffer{}
	shutdown := make(chan struct{}, 1)
	c := &PrereviewController{
		ExternalMode: true,
		CSVPath:      csvPath,
		CSVWriter:    csv.NewWriter(csvPath),
		AgentMode:    true,
		Emitter:      NewEventStream(buf, ""),
		ShutdownReq:  shutdown,
	}
	return c, buf, shutdown
}

func regionComment(id, body string) Comment {
	return Comment{ID: id, Kind: commentKindRegion, URL: "/p", Area: Area{X: 0.1, Y: 0.1, W: 0.2, H: 0.1}, Body: body, Created: fixedTS}
}

// TestFlushHandoff_EmitsActionableSnapshot pins that a handoff flush emits
// exactly one handoff event whose snapshot is the actionable comments only —
// resolved and anchor-outdated rows are pruned.
func TestFlushHandoff_EmitsActionableSnapshot(t *testing.T) {
	c, buf, _ := newStreamController(t)
	st := PrereviewState{Comments: []Comment{
		regionComment("keep", "fix this"),
		func() Comment { c := regionComment("resolved", "done"); c.Resolved = true; return c }(),
		func() Comment { c := regionComment("outdated", "gone"); c.AnchorStatus = anchorOutdated; return c }(),
	}}

	if err := c.flushHandoff(&st); err != nil {
		t.Fatalf("flushHandoff: %v", err)
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

// TestFlushHandoff_SeqIncrements pins that successive handoff flushes emit
// distinguishable rounds (monotonic seq), and that resolving between rounds
// prunes the snapshot.
func TestFlushHandoff_SeqIncrements(t *testing.T) {
	c, buf, _ := newStreamController(t)
	st := PrereviewState{Comments: []Comment{regionComment("a", "one"), regionComment("b", "two")}}

	if err := c.flushHandoff(&st); err != nil {
		t.Fatalf("first flushHandoff: %v", err)
	}
	// User resolves "a" in the UI, then hands off again.
	st.Comments[0].Resolved = true
	if err := c.flushHandoff(&st); err != nil {
		t.Fatalf("second flushHandoff: %v", err)
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

// TestFlushHandoff_NilEmitterSafe pins the default (non-agent) regression: a nil
// emitter is safe — flushHandoff persists the CSV and emits nothing rather than
// panicking.
func TestFlushHandoff_NilEmitterSafe(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "comments.csv")
	c := &PrereviewController{
		ExternalMode: true,
		CSVPath:      csvPath,
		CSVWriter:    csv.NewWriter(csvPath),
		// Emitter nil, AgentMode false.
	}
	st := PrereviewState{Comments: []Comment{regionComment("a", "x")}}
	if err := c.flushHandoff(&st); err != nil {
		t.Fatalf("flushHandoff with nil emitter: %v", err)
	}
}
