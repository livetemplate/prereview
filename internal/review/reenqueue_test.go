package review

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
)

// TestReenqueue_DoneBackToQueued: a re-enqueue tombstone moves a done comment
// back to queued (Processed=false), and the process↔re-enqueue counts resolve a
// full cycle correctly (redo → done again).
func TestReenqueue_DoneBackToQueued(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "comments.csv")
	c := &PrereviewController{CSVPath: csvPath, CSVWriter: csv.NewWriter(csvPath)}

	// Agent marked c1 done.
	if err := appendMark(c.processedPath(), "c1"); err != nil {
		t.Fatal(err)
	}
	st := &PrereviewState{Comments: []Comment{{ID: "c1", Body: "n"}}}
	c.applyProcessed(st)
	if !st.Comments[0].Processed || st.Comments[0].QueueState() != queueDone {
		t.Fatalf("c1 should be done, got processed=%v state=%q", st.Comments[0].Processed, st.Comments[0].QueueState())
	}

	// Reviewer re-enqueues it → back to queued.
	if _, err := c.ReenqueueComment(PrereviewState{Comments: st.Comments}, reqCtx("c1")); err != nil {
		t.Fatalf("ReenqueueComment: %v", err)
	}
	st.Comments[0].Processed = false // re-derive
	c.applyProcessed(st)
	if st.Comments[0].Processed || st.Comments[0].QueueState() != queueQueued {
		t.Errorf("after re-enqueue c1 should be queued, got processed=%v state=%q", st.Comments[0].Processed, st.Comments[0].QueueState())
	}

	// Agent redoes it (another process mark) → done again (2 process > 1 re-enqueue).
	if err := appendMark(c.processedPath(), "c1"); err != nil {
		t.Fatal(err)
	}
	st.Comments[0].Processed = false
	c.applyProcessed(st)
	if !st.Comments[0].Processed {
		t.Error("after re-process, c1 should be done again (2 process > 1 re-enqueue)")
	}
}

func reqCtx(id string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), "reenqueueComment", map[string]interface{}{"id": id})
}
