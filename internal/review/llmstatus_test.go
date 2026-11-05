package review

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newStatusController returns a controller whose store lives under a fresh temp
// dir, plus the resolved llm-status.json path.
func newStatusController(t *testing.T) (*PrereviewController, string) {
	t.Helper()
	dir := t.TempDir()
	csvPath := filepath.Join(dir, ".prereview", "comments.csv")
	if err := os.MkdirAll(filepath.Dir(csvPath), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &PrereviewController{CSVPath: csvPath}
	return c, c.statusPath()
}

func TestReadLLMStatus(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, LLMStatusFileName)

	// Missing → an IsNotExist error (caller resets to idle).
	if _, err := readLLMStatus(p); !os.IsNotExist(err) {
		t.Fatalf("missing file: want IsNotExist, got %v", err)
	}

	// Valid → parsed, no error.
	if err := os.WriteFile(p, []byte(`{"state":"done","message":"m","updated_at":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := readLLMStatus(p)
	if err != nil {
		t.Fatalf("valid file: %v", err)
	}
	if s.State != LLMStateDone || s.Message != "m" || s.UpdatedAt != "x" {
		t.Fatalf("valid file: got %+v", s)
	}

	// Malformed → a non-nil, non-IsNotExist error (caller keeps prior value).
	if err := os.WriteFile(p, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readLLMStatus(p); err == nil || os.IsNotExist(err) {
		t.Fatalf("malformed file: want a parse error, got %v", err)
	}
}

func TestApplyLLMStatus(t *testing.T) {
	c, sp := newStatusController(t)
	var st PrereviewState

	// Missing file → idle.
	c.applyLLMStatus(&st)
	if st.LLMState != "" || st.LLMMessage != "" {
		t.Fatalf("missing: want idle, got %+v", st)
	}

	// Valid working → mirrored onto state.
	if err := os.WriteFile(sp, []byte(`{"state":"working","message":"applying 3"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c.applyLLMStatus(&st)
	if st.LLMState != LLMStateWorking || st.LLMMessage != "applying 3" {
		t.Fatalf("valid: got %+v", st)
	}

	// Malformed → keep the previous good value (no flicker to idle).
	if err := os.WriteFile(sp, []byte(`{torn`), 0o644); err != nil {
		t.Fatal(err)
	}
	c.applyLLMStatus(&st)
	if st.LLMState != LLMStateWorking {
		t.Fatalf("malformed: want prior kept, got %+v", st)
	}

	// Removed → back to idle.
	if err := os.Remove(sp); err != nil {
		t.Fatal(err)
	}
	c.applyLLMStatus(&st)
	if st.LLMState != "" || st.LLMMessage != "" {
		t.Fatalf("removed: want idle, got %+v", st)
	}
}

func TestStatusFingerprintChangesOnWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, LLMStatusFileName)

	if fp := statusFingerprint(p); fp != "" {
		t.Fatalf("missing file: want empty fingerprint, got %q", fp)
	}
	if err := os.WriteFile(p, []byte(`{"state":"working"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	first := statusFingerprint(p)
	if first == "" {
		t.Fatal("present file: want non-empty fingerprint")
	}
	// A larger rewrite changes size (and mtime), so the fingerprint differs.
	if err := os.WriteFile(p, []byte(`{"state":"done","message":"finished the batch"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if second := statusFingerprint(p); second == first {
		t.Fatalf("rewrite: fingerprint unchanged (%q)", second)
	}
}

func TestLLMStatusPath(t *testing.T) {
	got := LLMStatusPath(filepath.Join("/x", ".prereview", "comments.csv"))
	want := filepath.Join("/x", ".prereview", LLMStatusFileName)
	if got != want {
		t.Fatalf("LLMStatusPath = %q, want %q", got, want)
	}
}

// fakeSession records the actions TriggerAction is called with, over a channel
// so the watcher test can await fan-out without sleeping on a fixed budget.
type fakeSession struct{ actions chan string }

func (f *fakeSession) TriggerAction(action string, _ map[string]interface{}) error {
	f.actions <- action
	return nil
}

func TestLLMStatusChangedPendingRefresh(t *testing.T) {
	c, sp := newStatusController(t)
	write := func(body string) {
		if err := os.WriteFile(sp, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// →working: no refresh nudge (nothing edited yet).
	write(`{"state":"working"}`)
	st, _ := c.LLMStatusChanged(PrereviewState{}, nil)
	if st.LLMState != LLMStateWorking || st.PendingRefresh {
		t.Fatalf("→working: got state=%q pending=%v, want working/false", st.LLMState, st.PendingRefresh)
	}

	// working→done: the agent finished a batch and edited files → offer a refresh.
	write(`{"state":"done"}`)
	st, _ = c.LLMStatusChanged(st, nil)
	if st.LLMState != LLMStateDone || !st.PendingRefresh {
		t.Fatalf("working→done: got state=%q pending=%v, want done/true", st.LLMState, st.PendingRefresh)
	}

	// done→working (the user handed off again while working, agent now on the
	// queued batch): the stale refresh nudge is superseded and the pill returns —
	// they must never show at once.
	write(`{"state":"working","message":"batch 2"}`)
	st, _ = c.LLMStatusChanged(st, nil)
	if st.LLMState != LLMStateWorking || st.PendingRefresh {
		t.Fatalf("done→working: got state=%q pending=%v, want working/false (nudge cleared)", st.LLMState, st.PendingRefresh)
	}

	// working→done again re-arms it (each finished batch offers a fresh refresh).
	write(`{"state":"done"}`)
	st, _ = c.LLMStatusChanged(st, nil)
	if !st.PendingRefresh {
		t.Fatal("second working→done must re-arm PendingRefresh")
	}

	// done→done is not a transition: once the nudge was cleared it stays cleared.
	st.PendingRefresh = false
	st, _ = c.LLMStatusChanged(st, nil)
	if st.PendingRefresh {
		t.Fatal("done→done must not re-arm PendingRefresh")
	}
}

// TestLLMStatusChangedFileEditNudge is the deterministic half of the feature: a
// reviewed-file edit (the watcher bumped reviewedGen) nudges PendingRefresh with
// NO agent status written — the agent forgetting `status`/`done` can no longer
// hide that it edited.
func TestLLMStatusChangedFileEditNudge(t *testing.T) {
	c, sp := newStatusController(t)
	if err := os.WriteFile(sp, []byte(`{}`), 0o644); err != nil { // idle status, no working→done
		t.Fatal(err)
	}
	c.reviewedGen.Store(1) // the watcher saw a raw file edit and bumped the gen

	// A tab that last rebuilt at gen 0 gets the nudge — deterministically.
	st, _ := c.LLMStatusChanged(PrereviewState{SeenReviewedGen: 0}, nil)
	if !st.PendingRefresh {
		t.Fatal("reviewedGen>SeenReviewedGen must set PendingRefresh (raw file edit, no agent command)")
	}
	// A tab already caught up (it just rebuilt via Mount) must not nudge.
	st2, _ := c.LLMStatusChanged(PrereviewState{SeenReviewedGen: 1}, nil)
	if st2.PendingRefresh {
		t.Fatal("a caught-up tab (SeenReviewedGen==reviewedGen) must not nudge")
	}
}

// TestReviewedFilesFingerprint pins the trigger: the key changes iff a file in
// the review SCOPE changes, so the watcher fires on a plan edit but ignores
// unrelated files.
func TestReviewedFilesFingerprint(t *testing.T) {
	repo := t.TempDir()
	doc := filepath.Join(repo, "doc.md")
	if err := os.WriteFile(doc, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &PrereviewController{RepoPath: repo, NoGit: true, SingleFile: "doc.md", Versions: &VersionStore{}}

	fp1 := c.reviewedFilesFingerprint()
	if fp1 == "" {
		t.Fatal("fingerprint is empty — the reviewed file is not in scope")
	}
	if err := os.WriteFile(doc, []byte("v2 is longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fp2 := c.reviewedFilesFingerprint(); fp2 == fp1 {
		t.Fatalf("fingerprint unchanged after editing the reviewed file: %q", fp2)
	}

	// A sibling file outside the review scope must NOT move the fingerprint.
	before := c.reviewedFilesFingerprint()
	if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("noise"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := c.reviewedFilesFingerprint(); after != before {
		t.Fatalf("fingerprint moved on a non-scoped file: %q -> %q", before, after)
	}
}

func TestWatchLLMStatusFiresOnChangeOnly(t *testing.T) {
	c, sp := newStatusController(t)
	fs := &fakeSession{actions: make(chan string, 8)}
	c.sessionMu.Lock()
	c.session = fs
	c.sessionMu.Unlock()

	stop := make(chan struct{})
	defer close(stop)
	go c.WatchLLMStatus(stop, 10*time.Millisecond)

	// No status file yet → the watcher must stay quiet (idle server does no
	// work). This also guarantees the watcher has started and captured an empty
	// baseline before the first write below, so that write is a real change.
	assertNoFire(t, fs, 80*time.Millisecond, "before any write")

	// First write → exactly one fan-out.
	writeStatus(t, sp, `{"state":"working"}`)
	assertFire(t, fs, "first write")

	// Unchanged file → no refire (fingerprint de-dupe).
	assertNoFire(t, fs, 80*time.Millisecond, "unchanged file")

	// Second, different write → fans out again.
	writeStatus(t, sp, `{"state":"done","message":"finished"}`)
	assertFire(t, fs, "second write")
}

func TestWatchLLMStatusSkipsWhenNoSession(t *testing.T) {
	c, sp := newStatusController(t)
	// session left nil (no tab ever connected).
	stop := make(chan struct{})
	defer close(stop)
	go c.WatchLLMStatus(stop, 10*time.Millisecond)
	// A change with no session must not panic and simply does nothing.
	writeStatus(t, sp, `{"state":"working"}`)
	time.Sleep(60 * time.Millisecond)
}

func writeStatus(t *testing.T, path, body string) {
	t.Helper()
	// Write atomically (temp + rename), exactly as the agent's skill does. A plain
	// os.WriteFile truncates-then-writes, briefly exposing a size-0 file; the
	// watcher's mtime+size fingerprint can catch that intermediate and fan out
	// twice — which flaked assertNoFire ("unchanged file") intermittently. rename is
	// atomic, so the poller only ever observes the final content.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func assertFire(t *testing.T, fs *fakeSession, when string) {
	t.Helper()
	select {
	case a := <-fs.actions:
		if a != "LLMStatusChanged" {
			t.Fatalf("%s: action = %q, want LLMStatusChanged", when, a)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: watcher did not fan out", when)
	}
}

func assertNoFire(t *testing.T, fs *fakeSession, within time.Duration, when string) {
	t.Helper()
	select {
	case a := <-fs.actions:
		t.Fatalf("%s: unexpected fan-out %q", when, a)
	case <-time.After(within):
	}
}
