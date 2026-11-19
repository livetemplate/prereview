package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLearnJobs: queue → claim → finish; a second learner gets nothing while the
// claim holds, and a stale claim (its learner died) can be taken again.
func TestLearnJobs(t *testing.T) {
	dir := t.TempDir()
	if _, ok, _ := ClaimJob(dir); ok {
		t.Fatal("claimed a job from an empty log")
	}
	q, ok, err := QueueJob(dir, Job{Stores: []string{"/a/.prereview"}, AutoAccept: true, Server: "/a/.prereview"}, true)
	if err != nil || !ok || !strings.HasPrefix(q.ID, "j") {
		t.Fatalf("queue: %+v %v %v", q, ok, err)
	}
	if _, ok, _ := QueueJob(dir, Job{Stores: []string{"/a/.prereview"}}, true); ok {
		t.Fatal("queued a second job for a store an open job already covers")
	}
	if _, ok, _ := QueueJob(dir, Job{Stores: []string{"/a/.prereview", "/b/.prereview"}}, true); !ok {
		t.Fatal("a job adding a store no open job covers wasn't queued")
	}

	c, ok, err := ClaimJob(dir)
	if err != nil || !ok || c.ID != q.ID || c.Op != JobClaimed || !c.AutoAccept || c.Stores[0] != "/a/.prereview" {
		t.Fatalf("claim took %+v (%v %v), want the oldest job %s with its fields", c, ok, err, q.ID)
	}
	second, ok, _ := ClaimJob(dir)
	if !ok || second.ID == q.ID {
		t.Fatalf("the second claim took %+v, want the other queued job", second)
	}
	if _, ok, _ := ClaimJob(dir); ok {
		t.Fatal("claimed a job that is already claimed")
	}

	must(t, FinishJob(dir, q.ID))
	must(t, FinishJob(dir, q.ID)) // twice is harmless
	if err := FinishJob(dir, "jnope"); err == nil {
		t.Fatal("finished a job that doesn't exist")
	}
	if open := OpenJobs(dir); len(open) != 1 || open[0].ID != second.ID {
		t.Fatalf("open jobs = %+v, want only %s", open, second.ID)
	}

	// The second learner died: once its claim is older than the TTL, it's taken again.
	stale := second
	stale.At = time.Now().Add(-JobClaimTTL - time.Minute).UTC().Format(time.RFC3339Nano)
	must(t, Append(filepath.Join(dir, LearnJobsFile), stale))
	if again, ok, _ := ClaimJob(dir); !ok || again.ID != second.ID {
		t.Fatalf("a stale claim wasn't retaken: %+v %v", again, ok)
	}
}

// TestPropose_AutoAccept: a learning answering an auto-accept job is accepted on
// arrival (superseding what it replaces); with a job queued without auto-accept,
// or no job, it arrives proposed. A job's learning may only cite its stores.
func TestPropose_AutoAccept(t *testing.T) {
	dir, store := t.TempDir(), seedStore(t, t.TempDir())
	other := seedStore(t, t.TempDir())
	auto, _, err := QueueJob(dir, Job{Stores: []string{store}, AutoAccept: true}, false)
	must(t, err)
	manual, _, err := QueueJob(dir, Job{Stores: []string{store}}, false)
	must(t, err)

	plain, err := Propose(dir, Proposal{Text: "Cut filler.", Evidence: []string{store + ":c1"}})
	must(t, err)
	if plain.Status != StatusProposed || plain.Auto {
		t.Fatalf("no job: %s auto=%v, want proposed", plain.Status, plain.Auto)
	}
	must(t, Act(dir, plain.ID, ActAccept))

	l, err := Propose(dir, Proposal{Text: "Cut filler words.", Replaces: plain.ID, Evidence: []string{store + ":c1"}, Job: auto.ID})
	must(t, err)
	byID := map[string]Learning{}
	for _, x := range LoadLearnings(dir) {
		byID[x.ID] = x
	}
	if got := byID[l.ID]; got.Status != StatusAccepted || !got.Auto {
		t.Fatalf("auto job: %s auto=%v, want accepted + auto", got.Status, got.Auto)
	}
	if got := byID[plain.ID]; got.Status != StatusSuperseded || got.SupersededBy != l.ID {
		t.Fatalf("the replaced learning is %s (by %q), want superseded by %s", got.Status, got.SupersededBy, l.ID)
	}

	m, err := Propose(dir, Proposal{Text: "Lead with the point.", Evidence: []string{store + ":c1"}, Job: manual.ID})
	must(t, err)
	if m.Status != StatusProposed || m.Auto {
		t.Fatalf("job without auto-accept: %s auto=%v, want proposed", m.Status, m.Auto)
	}

	if _, err := Propose(dir, Proposal{Text: "x", Evidence: []string{other + ":c1"}, Job: auto.ID}); err == nil || !strings.Contains(err.Error(), "not a store job") {
		t.Fatalf("cited a store the job doesn't list: %v", err)
	}
	must(t, FinishJob(dir, auto.ID))
	if _, err := Propose(dir, Proposal{Text: "x", Evidence: []string{store + ":c1"}, Job: auto.ID}); err == nil {
		t.Fatal("proposed against a finished job")
	}
	if _, err := Propose(dir, Proposal{Text: "x", Evidence: []string{store + ":c1"}, Job: "jnope"}); err == nil {
		t.Fatal("proposed against a job that doesn't exist")
	}
}

func TestLearnerAlive(t *testing.T) {
	dir := t.TempDir()
	if LearnerAlive(dir) {
		t.Fatal("alive before any learner touched the library")
	}
	must(t, TouchLearner(dir))
	must(t, TouchLearner(dir)) // the second touch only moves the mtime
	if !LearnerAlive(dir) {
		t.Fatal("not alive right after a touch")
	}
	old := time.Now().Add(-LearnerStale - time.Second)
	must(t, os.Chtimes(filepath.Join(dir, LearnerFile), old, old))
	if LearnerAlive(dir) {
		t.Fatal("alive after the learner went quiet")
	}
}

// TestUndo: undoing an auto-accepted replacement rejects it and puts the
// learning it superseded back in effect, in one write.
func TestUndo(t *testing.T) {
	dir, store := t.TempDir(), seedStore(t, t.TempDir())
	old, err := Propose(dir, Proposal{Text: "Cut filler.", Evidence: []string{store + ":c1"}})
	must(t, err)
	must(t, Act(dir, old.ID, ActAccept))
	j, _, err := QueueJob(dir, Job{Stores: []string{store}, AutoAccept: true}, false)
	must(t, err)
	repl, err := Propose(dir, Proposal{Text: "Cut filler words.", Replaces: old.ID, Evidence: []string{store + ":c1"}, Job: j.ID})
	must(t, err)
	plain, err := Propose(dir, Proposal{Text: "Lead with the point.", Evidence: []string{store + ":c1"}, Job: j.ID})
	must(t, err)

	must(t, Undo(dir, []string{repl.ID, plain.ID}))
	got := map[string]Learning{}
	for _, l := range LoadLearnings(dir) {
		got[l.ID] = l
	}
	if got[repl.ID].Status != StatusRejected || got[plain.ID].Status != StatusRejected {
		t.Fatalf("undone: %s, %s; want both rejected", got[repl.ID].Status, got[plain.ID].Status)
	}
	if o := got[old.ID]; o.Status != StatusAccepted || o.SupersededBy != "" {
		t.Fatalf("the learning the undone one replaced is %s (by %q), want accepted again", o.Status, o.SupersededBy)
	}
	if err := Undo(dir, []string{"lnope"}); err == nil {
		t.Fatal("undid a learning that doesn't exist")
	}
}

func TestLearnSeen(t *testing.T) {
	dir := t.TempDir()
	if LearnSeenAt(dir) != "" {
		t.Fatal("seen before anything was marked")
	}
	at, err := MarkLearnSeen(dir)
	must(t, err)
	if got := LearnSeenAt(dir); got != at || at == "" {
		t.Fatalf("seen = %q, want %q", got, at)
	}
}
