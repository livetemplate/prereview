package review

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/livetemplate/prereview/csv"
)

// snapshotIDs is the set of comment ids the agent would be handed right now —
// the same derivation EmitSnapshot uses (marks overlay, then the thread filter).
func snapshotIDs(t *testing.T, c *PrereviewController, comments []Comment) []string {
	t.Helper()
	cs := slices.Clone(comments)
	for i := range cs {
		cs[i].Processed, cs[i].Answered = false, false
	}
	applyProcessedMarks(cs, c.CSVPath)
	return ids(actionableComments(cs, groupThreads(loadThreads(c.CSVPath))))
}

// TestReenqueueAfterAgentReply: the normal done comment also carries the agent's
// reply (SKILL asks for both), so its thread ends with the agent. ↺ must still
// hand it back: the tombstone alone flips Processed, but threadActionable ignores
// "settled" once a thread exists, so without a reviewer entry it never returns.
func TestReenqueueAfterAgentReply(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "comments.csv")
	c := &PrereviewController{CSVPath: csvPath, CSVWriter: csv.NewWriter(csvPath)}
	comments := []Comment{{ID: "c1", Body: "n"}}

	if err := appendMark(c.processedPath(), "c1"); err != nil {
		t.Fatal(err)
	}
	writeLines(t, AgentRepliesPath(csvPath), `{"target_id":"c1","author":"agent","body":"renamed it","at":100}`)
	if ids := snapshotIDs(t, c, comments); len(ids) != 0 {
		t.Fatalf("done + replied should be out of the snapshot, got %v", ids)
	}

	if _, err := c.ReenqueueComment(PrereviewState{Comments: slices.Clone(comments)}, reqCtx("c1")); err != nil {
		t.Fatalf("ReenqueueComment: %v", err)
	}
	if ids := snapshotIDs(t, c, comments); !slices.Equal(ids, []string{"c1"}) {
		t.Fatalf("after ↺ the agent must be handed c1 again, got %v", ids)
	}
}

// TestReenqueueWithoutThreadAddsNoEntry: with no thread, the tombstone alone already
// re-queues the comment, so ↺ must not invent a reviewer reply.
func TestReenqueueWithoutThreadAddsNoEntry(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "comments.csv")
	c := &PrereviewController{CSVPath: csvPath, CSVWriter: csv.NewWriter(csvPath)}
	comments := []Comment{{ID: "c1", Body: "n"}}
	if err := appendMark(c.processedPath(), "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReenqueueComment(PrereviewState{Comments: slices.Clone(comments)}, reqCtx("c1")); err != nil {
		t.Fatal(err)
	}
	if th := loadThreads(csvPath); len(th) != 0 {
		t.Fatalf("no thread existed, so ↺ must not add one; got %+v", th)
	}
	if ids := snapshotIDs(t, c, comments); !slices.Equal(ids, []string{"c1"}) {
		t.Fatalf("the tombstone alone must re-queue it, got %v", ids)
	}
}

// answeredFixture is a controller + one comment the agent answered with no edit:
// its reply (agent-last thread) and the answered mark — what `reply --answered` writes.
func answeredFixture(t *testing.T) (*PrereviewController, []Comment) {
	t.Helper()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "comments.csv")
	c := &PrereviewController{CSVPath: csvPath, CSVWriter: csv.NewWriter(csvPath)}
	writeLines(t, AgentRepliesPath(csvPath), `{"target_id":"c1","author":"agent","body":"kept on purpose","at":100}`)
	if err := appendMark(AnsweredPath(csvPath), "c1"); err != nil {
		t.Fatal(err)
	}
	return c, []Comment{{ID: "c1", Body: "why?"}}
}

// queueStateOf is the state of id's queue-panel row, after the marks overlay.
func queueStateOf(t *testing.T, c *PrereviewController, comments []Comment, id string) string {
	t.Helper()
	st := PrereviewState{Comments: slices.Clone(comments), ThreadEntries: loadThreads(c.CSVPath), QueueGlobal: true}
	applyProcessedMarks(st.Comments, c.CSVPath)
	for _, it := range st.QueueItems() {
		if it.ID == id {
			return it.State
		}
	}
	t.Fatalf("no queue row for %s", id)
	return ""
}

// TestAnsweredLifecycle (#205): answered → reviewer pushes back (queued, handed to
// the agent again) → agent answers again (answered) → agent edits instead (done).
// The store holds NO processed marks at the start, which is the early-return case
// applyProcessedMarks must not short-circuit.
func TestAnsweredLifecycle(t *testing.T) {
	c, comments := answeredFixture(t)

	if got := queueStateOf(t, c, comments, "c1"); got != queueAnswered {
		t.Fatalf("after reply --answered: state %q, want answered", got)
	}
	if ids := snapshotIDs(t, c, comments); len(ids) != 0 {
		t.Fatalf("an answered comment is not handed to the agent, got %v", ids)
	}

	if err := appendReviewerReply(c.CSVPath, "c1", "no — please change it", 200); err != nil {
		t.Fatal(err)
	}
	if got := queueStateOf(t, c, comments, "c1"); got != queueQueued {
		t.Fatalf("after the reviewer replies: state %q, want queued", got)
	}
	if ids := snapshotIDs(t, c, comments); !slices.Equal(ids, []string{"c1"}) {
		t.Fatalf("the pushback must reach the agent, got %v", ids)
	}

	writeLines(t, AgentRepliesPath(c.CSVPath),
		`{"target_id":"c1","author":"agent","body":"kept on purpose","at":100}`,
		`{"target_id":"c1","author":"agent","body":"still no: it is load-bearing","at":300}`)
	if got := queueStateOf(t, c, comments, "c1"); got != queueAnswered {
		t.Fatalf("after answering again: state %q, want answered", got)
	}

	if err := appendMark(c.processedPath(), "c1"); err != nil {
		t.Fatal(err)
	}
	if got := queueStateOf(t, c, comments, "c1"); got != queueDone {
		t.Fatalf("after a later done: state %q, want done (an edit beats an answer)", got)
	}
}

// TestAnsweredDoesNotPoisonDoneNetting (#205): answered.jsonl must never share the
// re-enqueue tombstones. A ↺ elsewhere, then `done` on the answered comment, still
// counts as done; and the ordinary done → ↺ → done cycle is untouched.
func TestAnsweredDoesNotPoisonDoneNetting(t *testing.T) {
	c, _ := answeredFixture(t)
	comments := []Comment{{ID: "c1", Body: "why?"}, {ID: "c2", Body: "fix"}}
	if err := appendMark(c.processedPath(), "c2"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReenqueueComment(PrereviewState{Comments: slices.Clone(comments)}, reqCtx("c2")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		if err := appendMark(c.processedPath(), id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"c1", "c2"} {
		if got := queueStateOf(t, c, comments, id); got != queueDone {
			t.Errorf("%s: state %q, want done", id, got)
		}
	}
}

// TestStallIgnoresAgentLastThreads (#205): the stall banner counts only the work the
// agent is actually being handed. An answered comment and a plain reply-only answer
// (a client whose skill predates --answered) both have an empty snapshot entry, so
// neither is a stall; a fresh queued comment still is.
func TestStallIgnoresAgentLastThreads(t *testing.T) {
	agentReply := func(id string) ThreadEntry {
		return ThreadEntry{TargetID: id, Author: AuthorAgent, Body: "answer", At: 1}
	}
	quiet := AgentPresence{PickedUp: true, Quiet: true, QuietSince: "2:05 PM"}
	for _, tc := range []struct {
		name    string
		comment Comment
		thread  []ThreadEntry
		want    int
	}{
		{"answered", Comment{ID: "c1", Answered: true}, []ThreadEntry{agentReply("c1")}, 0},
		{"plain reply-only", Comment{ID: "c1"}, []ThreadEntry{agentReply("c1")}, 0},
		{"fresh queued", Comment{ID: "c1"}, nil, 1},
		{"reviewer replied last", Comment{ID: "c1", Answered: true},
			[]ThreadEntry{agentReply("c1"), {TargetID: "c1", Author: AuthorReviewer, Body: "no", At: 2}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := PrereviewState{AgentMode: true, Agent: quiet, Comments: []Comment{tc.comment}, ThreadEntries: tc.thread}
			if got := st.outstandingAgentWork(); got != tc.want {
				t.Errorf("outstandingAgentWork = %d, want %d", got, tc.want)
			}
			if got := st.AgentStalled(); got != (tc.want > 0) {
				t.Errorf("AgentStalled = %v, want %v", got, tc.want > 0)
			}
		})
	}
}

// TestHasQueueWithOnlyAnswered (#205): a file whose only item is answered still shows
// the panel — it is the reviewer's turn on it (the #203 Blocked lesson).
func TestHasQueueWithOnlyAnswered(t *testing.T) {
	st := PrereviewState{Comments: []Comment{{ID: "c1", Answered: true}}}
	if !st.HasQueue() || st.AnsweredCount() != 1 {
		t.Fatalf("HasQueue=%v AnsweredCount=%d, want true/1", st.HasQueue(), st.AnsweredCount())
	}
	items := st.QueueItems()
	if len(items) != 1 || items[0].State != queueAnswered {
		t.Fatalf("QueueItems = %+v, want one answered row", items)
	}
}
