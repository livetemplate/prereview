package review

import (
	"context"
	"path/filepath"
	"slices"
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

// TestDraftLifecycle: a held draft (from materializeDraft — the only draft
// source now) is excluded from the actionable snapshot; EnqueueComment clears the
// Draft flag and puts it back. The state round-trips through the CSV (Draft
// persists as the inverted `enqueued` column).
func TestDraftLifecycle(t *testing.T) {
	c := draftController(t)
	// A held draft (Draft=true), as materializeDraft would leave it.
	st := PrereviewState{Comments: []Comment{{ID: "x", File: "a.go", FromLine: 1, ToLine: 1, Side: "new", Body: "note", Draft: true}}}
	if err := c.persist(st.Comments); err != nil {
		t.Fatalf("seed persist: %v", err)
	}
	if got := actionableComments(st.Comments); len(got) != 0 {
		t.Fatalf("a draft must be excluded from the actionable snapshot, got %d", len(got))
	}

	// Enqueue → Draft cleared, back in the snapshot, persisted.
	st, err := c.EnqueueComment(st, draftCtx("enqueueComment", "x"))
	if err != nil {
		t.Fatalf("EnqueueComment: %v", err)
	}
	if st.Comments[0].Draft {
		t.Error("EnqueueComment should set Draft=false")
	}
	if got := actionableComments(st.Comments); len(got) != 1 {
		t.Errorf("enqueued comment should be actionable, got %d", len(got))
	}
	if reloaded := c.loadCommentsFromDisk(); len(reloaded) != 1 || reloaded[0].Draft {
		t.Errorf("enqueued state must persist across reload: %+v", reloaded)
	}
}

// TestEnqueue_RequeuesWorkedOn is the regression guard for the reported bug
// (#126 follow-up): the per-card "Enqueue" on a WORKED-ON comment must actually
// return it to "queued" (clear the processed mark), not leave it in "done". The
// un-mark is GUARDED — enqueuing a never-processed comment must NOT corrupt the
// count-based Processed derivation the next time the agent marks it.
func TestEnqueue_RequeuesWorkedOn(t *testing.T) {
	c := draftController(t)
	st := PrereviewState{Comments: []Comment{{ID: "done", Body: "redo me"}, {ID: "fresh", Body: "never touched"}}}
	if err := c.persist(st.Comments); err != nil {
		t.Fatalf("seed persist: %v", err)
	}
	// The agent marks "done" as worked-on.
	if err := appendMark(c.processedPath(), "done"); err != nil {
		t.Fatalf("processed mark: %v", err)
	}
	c.applyProcessed(&st)
	if st.Comments[0].QueueState() != queueDone {
		t.Fatalf("setup: 'done' should be worked-on, got %q", st.Comments[0].QueueState())
	}

	// Enqueue the worked-on comment → it must leave "done" for "queued".
	st, err := c.EnqueueComment(st, draftCtx("enqueueComment", "done"))
	if err != nil {
		t.Fatalf("EnqueueComment(done): %v", err)
	}
	c.applyProcessed(&st)
	if got := st.Comments[0].QueueState(); got != queueQueued {
		t.Errorf("re-enqueued comment must be back in %q, got %q", queueQueued, got)
	}

	// GUARD: enqueue the never-processed "fresh" comment (a no-op re-queue), then
	// the agent marks it worked-on for the FIRST time — it must show "done". An
	// unconditional tombstone would have made pc==rc here and hidden the badge.
	if _, err := c.EnqueueComment(st, draftCtx("enqueueComment", "fresh")); err != nil {
		t.Fatalf("EnqueueComment(fresh): %v", err)
	}
	if err := appendMark(c.processedPath(), "fresh"); err != nil {
		t.Fatalf("processed mark fresh: %v", err)
	}
	c.applyProcessed(&st)
	fresh := st.Comments[slices.IndexFunc(st.Comments, func(cm Comment) bool { return cm.ID == "fresh" })]
	if fresh.QueueState() != queueDone {
		t.Errorf("a never-processed comment that was enqueued must still show worked-on when the agent marks it (tombstone must be guarded), got %q", fresh.QueueState())
	}
}

// TestSaveEnqueuesDraft: saving (re-saving) a held draft enqueues it — the reason
// drafts need no separate "enqueue" button. Simulates Edit→Save: EditingCommentID
// set, addCommentBody clears Draft on the edited comment.
func TestSaveEnqueuesDraft(t *testing.T) {
	c := draftController(t)
	// A file-level held draft (as materializeDraft leaves it).
	st := c.materializeDraft(PrereviewState{SelectedFile: "a.go", CommentMode: commentKindFile, DraftBody: "hold this"})
	if len(st.Comments) != 1 || !st.Comments[0].Draft {
		t.Fatalf("setup: want 1 draft, got %+v", st.Comments)
	}
	id := st.Comments[0].ID

	// Re-open (Edit) and Save → saving enqueues it (Draft=false).
	st.EditingCommentID = id
	st.CommentMode = commentKindFile
	st.SelectedFile = "a.go"
	st, err := c.addCommentBody(st, "hold this, refined")
	if err != nil {
		t.Fatalf("save edited draft: %v", err)
	}
	idx := slices.IndexFunc(st.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 || st.Comments[idx].Draft {
		t.Errorf("saving a draft must enqueue it (Draft=false), got %+v", st.Comments[idx])
	}
	if got := actionableComments(st.Comments); len(got) != 1 {
		t.Errorf("saved (enqueued) comment should be actionable, got %d", len(got))
	}
	if reloaded := c.loadCommentsFromDisk(); len(reloaded) != 1 || reloaded[0].Draft {
		t.Errorf("enqueued state must persist across reload: %+v", reloaded)
	}
}

// TestMaterializeDraft_KeepsUnsavedText: navigating away with unsaved composer
// text turns it into a held draft (not lost), rather than requiring a Save click.
func TestMaterializeDraft_KeepsUnsavedText(t *testing.T) {
	c := draftController(t)
	// A file-level composer with unsaved text (DraftBody), no Save clicked.
	st := PrereviewState{SelectedFile: "a.go", CommentMode: commentKindFile, DraftBody: "hold this thought"}

	st = c.materializeDraft(st)
	if len(st.Comments) != 1 {
		t.Fatalf("want 1 draft comment, got %d", len(st.Comments))
	}
	if !st.Comments[0].Draft {
		t.Error("materialized comment should be a Draft")
	}
	if got := actionableComments(st.Comments); len(got) != 0 {
		t.Errorf("a draft must not be actionable, got %d", len(got))
	}
	if reloaded := c.loadCommentsFromDisk(); len(reloaded) != 1 || !reloaded[0].Draft {
		t.Errorf("draft must persist: %+v", reloaded)
	}

	// No pending text → no-op.
	if got := c.materializeDraft(PrereviewState{SelectedFile: "a.go", CommentMode: commentKindFile}); len(got.Comments) != 0 {
		t.Error("materializeDraft with empty DraftBody should be a no-op")
	}
	// Editing an existing comment → no-op (abandoning an edit reverts, not drafts).
	editing := PrereviewState{SelectedFile: "a.go", CommentMode: commentKindFile, DraftBody: "x", EditingCommentID: "e1"}
	if got := c.materializeDraft(editing); len(got.Comments) != 0 {
		t.Error("materializeDraft while editing should be a no-op")
	}
}

// TestEnqueue_IdempotentAndMissing: enqueuing an already-queued comment is a
// no-op; an unknown id errors.
func TestEnqueue_IdempotentAndMissing(t *testing.T) {
	c := draftController(t)
	st := PrereviewState{Comments: []Comment{{ID: "x", Body: "n"}}}

	// Already enqueued (not draft, not processed) → EnqueueComment is a no-op.
	if _, err := c.EnqueueComment(st, draftCtx("enqueueComment", "x")); err != nil {
		t.Errorf("enqueue of an already-enqueued comment should be a no-op: %v", err)
	}
	// Unknown id errors.
	if _, err := c.EnqueueComment(st, draftCtx("enqueueComment", "nope")); err == nil {
		t.Error("enqueue of an unknown id should error")
	}
}
