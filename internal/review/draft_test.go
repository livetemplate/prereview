package review

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
)

func draftController(t *testing.T) *PrereviewController {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comments.csv")
	return &PrereviewController{CSVPath: path, CSVWriter: csv.NewWriter(path)}
}

func draftCtx(action, id string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), action, map[string]interface{}{"id": id})
}

// TestDraftLifecycle: MoveToDraft holds a comment out of the actionable snapshot;
// EnqueueComment puts it back. The state round-trips through the CSV (Draft
// persists as the inverted `enqueued` column).
func TestDraftLifecycle(t *testing.T) {
	c := draftController(t)
	st := PrereviewState{Comments: []Comment{{ID: "x", File: "a.go", FromLine: 1, ToLine: 1, Side: "new", Body: "note"}}}

	// A freshly-saved comment is enqueued (Draft=false) → in the snapshot.
	if st.Comments[0].Draft {
		t.Fatal("a new comment must default to enqueued (Draft=false)")
	}
	if got := actionableComments(st.Comments); len(got) != 1 {
		t.Fatalf("enqueued comment should be actionable, got %d", len(got))
	}

	// Move to draft → excluded from the snapshot, and persisted.
	st, err := c.MoveToDraft(st, draftCtx("moveToDraft", "x"))
	if err != nil {
		t.Fatalf("MoveToDraft: %v", err)
	}
	if !st.Comments[0].Draft {
		t.Error("MoveToDraft should set Draft=true")
	}
	if got := actionableComments(st.Comments); len(got) != 0 {
		t.Errorf("a draft must be excluded from the actionable snapshot, got %d", len(got))
	}
	if reloaded := c.loadCommentsFromDisk(); len(reloaded) != 1 || !reloaded[0].Draft {
		t.Errorf("draft must persist across reload: %+v", reloaded)
	}

	// Enqueue → back in the snapshot.
	st, err = c.EnqueueComment(st, draftCtx("enqueueComment", "x"))
	if err != nil {
		t.Fatalf("EnqueueComment: %v", err)
	}
	if st.Comments[0].Draft {
		t.Error("EnqueueComment should set Draft=false")
	}
	if got := actionableComments(st.Comments); len(got) != 1 {
		t.Errorf("re-enqueued comment should be actionable again, got %d", len(got))
	}
	if reloaded := c.loadCommentsFromDisk(); len(reloaded) != 1 || reloaded[0].Draft {
		t.Errorf("enqueued state must persist across reload: %+v", reloaded)
	}
}

// TestDraftToggle_IdempotentAndMissing: toggling to the current state is a no-op;
// an unknown id errors.
func TestDraftToggle_IdempotentAndMissing(t *testing.T) {
	c := draftController(t)
	st := PrereviewState{Comments: []Comment{{ID: "x", Body: "n"}}}

	// Already enqueued → EnqueueComment is a no-op (no error).
	if _, err := c.EnqueueComment(st, draftCtx("enqueueComment", "x")); err != nil {
		t.Errorf("enqueue of an already-enqueued comment should be a no-op: %v", err)
	}
	// Unknown id errors.
	if _, err := c.MoveToDraft(st, draftCtx("moveToDraft", "nope")); err == nil {
		t.Error("MoveToDraft on an unknown id should error")
	}
}
