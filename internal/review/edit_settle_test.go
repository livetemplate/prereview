package review

// #198: the watcher holds reviewed-file edits until they settle, so a burst of
// writes is ONE version and ONE refresh-nudge gen bump. These drive the real
// WatchLLMStatus goroutine against a temp NoGit review and assert on observable
// state (gen, versions, the settling flag) rather than counting fan-outs.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type settleHarness struct {
	t    *testing.T
	c    *PrereviewController
	vs   *VersionStore
	doc  string
	stat string
}

// newSettleHarness boots a watcher on a one-file NoGit review with a baseline
// version, EditSettle shrunk to settle, and a session that never blocks.
func newSettleHarness(t *testing.T, settle time.Duration) *settleHarness {
	t.Helper()
	old := EditSettle
	EditSettle = settle
	t.Cleanup(func() { EditSettle = old })

	repo := t.TempDir()
	doc := filepath.Join(repo, "doc.md")
	if err := os.WriteFile(doc, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}
	vs, err := NewVersionStore(filepath.Join(repo, ".prereview", "versions"))
	if err != nil {
		t.Fatal(err)
	}
	c := &PrereviewController{RepoPath: repo, NoGit: true, SingleFile: "doc.md", Versions: vs,
		CSVPath: filepath.Join(repo, ".prereview", "comments.csv")}
	c.CheckpointBaseline()

	fs := &fakeSession{actions: make(chan string, 1)}
	stop := make(chan struct{})
	go func() { // drain: fan-out counts aren't the contract here
		for {
			select {
			case <-fs.actions:
			case <-stop:
				return
			}
		}
	}()
	c.session = fs
	go c.WatchLLMStatus(stop, 10*time.Millisecond)
	t.Cleanup(func() { close(stop) })
	time.Sleep(40 * time.Millisecond) // let the watcher take its baseline fingerprints
	return &settleHarness{t: t, c: c, vs: vs, doc: doc, stat: c.statusPath()}
}

func (h *settleHarness) edit(body string) { h.t.Helper(); writeStatus(h.t, h.doc, body) }
func (h *settleHarness) status(body string) { h.t.Helper(); writeStatus(h.t, h.stat, body) }

func (h *settleHarness) versions() []Checkpoint {
	h.t.Helper()
	cps, err := h.vs.Checkpoints()
	if err != nil {
		h.t.Fatal(err)
	}
	return cps
}

func (h *settleHarness) waitFor(what string, within time.Duration, ok func() bool) {
	h.t.Helper()
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	h.t.Fatalf("timed out waiting for %s (gen=%d settling=%v versions=%d)",
		what, h.c.reviewedGen.Load(), h.c.editsSettling.Load(), len(h.versions()))
}

func TestEditBurstSettlesOnce(t *testing.T) {
	h := newSettleHarness(t, 200*time.Millisecond)
	for i := range 5 {
		h.edit("v1 step " + string(rune('a'+i)))
		time.Sleep(40 * time.Millisecond)
		if g := h.c.reviewedGen.Load(); g != 0 {
			t.Fatalf("gen bumped mid-burst (after write %d): %d", i+1, g)
		}
	}
	h.waitFor("settling flag during the burst", time.Second, h.c.editsSettling.Load)
	h.waitFor("the burst to settle", 2*time.Second, func() bool { return h.c.reviewedGen.Load() == 1 })
	if h.c.editsSettling.Load() {
		t.Fatal("still settling after the boundary closed")
	}
	cps := h.versions()
	if len(cps) != 2 || cps[1].Trigger != VersionTriggerFileEdit {
		t.Fatalf("want baseline + one file-edit version, got %+v", cps)
	}
	time.Sleep(400 * time.Millisecond)
	if g := h.c.reviewedGen.Load(); g != 1 {
		t.Fatalf("gen moved again with no further edits: %d", g)
	}
}

// A done write closes the window at once — no prior "working" needed — and its
// message becomes the version's changelog (lost before: llm-done required working).
func TestEditDoneClosesWithChangelog(t *testing.T) {
	h := newSettleHarness(t, time.Hour)
	h.edit("v1 by the agent")
	h.waitFor("pending edits", time.Second, h.c.editsSettling.Load)
	h.status(`{"state":"done","message":"reworked the intro"}`)
	h.waitFor("done to close the window", time.Second, func() bool { return h.c.reviewedGen.Load() == 1 })
	cps := h.versions()
	if last := cps[len(cps)-1]; len(cps) != 2 || last.Trigger != VersionTriggerLLMDone || last.Changelog != "reworked the intro" {
		t.Fatalf("want one llm-done version with the changelog, got %+v", cps)
	}
}

// While the agent is "working" the window never closes on time; the done does.
func TestEditWhileWorkingWaitsForDone(t *testing.T) {
	h := newSettleHarness(t, 50*time.Millisecond)
	h.status(`{"state":"working"}`)
	time.Sleep(40 * time.Millisecond)
	h.edit("v1 while working")
	time.Sleep(300 * time.Millisecond) // 6× EditSettle
	if g, n := h.c.reviewedGen.Load(), len(h.versions()); g != 0 || n != 1 {
		t.Fatalf("closed on time while working: gen=%d versions=%d", g, n)
	}
	h.status(`{"state":"done","message":"applied"}`)
	h.waitFor("done to close the window", time.Second, func() bool { return h.c.reviewedGen.Load() == 1 })
	if last := h.versions()[len(h.versions())-1]; last.Trigger != VersionTriggerLLMDone || last.Changelog != "applied" {
		t.Fatalf("final version %+v, want llm-done/applied", last)
	}
}

// Content that matches the newest version opens no window (an mtime-only touch,
// or the reviewer's own rollback); a burst that ends where it began bumps nothing.
func TestEditNoOpNeverNudges(t *testing.T) {
	h := newSettleHarness(t, 100*time.Millisecond)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(h.doc, future, future); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if h.c.editsSettling.Load() {
		t.Fatal("an mtime-only touch opened a settle window")
	}

	h.edit("v1 temporary")
	h.waitFor("pending edits", time.Second, h.c.editsSettling.Load)
	h.edit("v0") // reverted before settling
	h.waitFor("the window to close", 2*time.Second, func() bool { return !h.c.editsSettling.Load() })
	if g, n := h.c.reviewedGen.Load(), len(h.versions()); g != 0 || n != 1 {
		t.Fatalf("a burst back to the original nudged: gen=%d versions=%d", g, n)
	}
}

// The "working" suppression in LLMStatusChanged is real again: a settled gen bump
// can't re-arm the nudge while the agent is mid-batch (it ran after the switch and
// overrode it). The editing notice also stays out of the working pill's way.
func TestLLMStatusChangedWorkingSuppressesNudge(t *testing.T) {
	c, sp := newStatusController(t)
	c.reviewedGen.Store(1)
	c.editsSettling.Store(true)
	writeStatus(t, sp, `{"state":"working"}`)
	st, _ := c.LLMStatusChanged(PrereviewState{PendingRefresh: true}, nil)
	if st.PendingRefresh || st.EditsSettling {
		t.Fatalf("while working: PendingRefresh=%v EditsSettling=%v, want both false", st.PendingRefresh, st.EditsSettling)
	}
	writeStatus(t, sp, `{"state":"done"}`)
	st, _ = c.LLMStatusChanged(st, nil)
	if !st.PendingRefresh || !st.EditsSettling {
		t.Fatalf("after done: PendingRefresh=%v EditsSettling=%v, want both true", st.PendingRefresh, st.EditsSettling)
	}
}

// A new version is the one nudge rule: a rollback reaches EVERY tab (via the gen),
// not only the restoring one, while the launch baseline and an unchanged scope
// nudge nobody.
func TestCheckpointNewVersionBumpsGen(t *testing.T) {
	h := newSettleHarness(t, time.Hour)
	if g := h.c.reviewedGen.Load(); g != 0 {
		t.Fatalf("baseline bumped the gen: %d", g)
	}
	h.c.checkpointVersions(VersionTriggerRollback, "") // unchanged scope
	if g := h.c.reviewedGen.Load(); g != 0 {
		t.Fatalf("an unchanged-scope checkpoint bumped the gen: %d", g)
	}
	if err := os.WriteFile(h.doc, []byte("restored"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.c.checkpointVersions(VersionTriggerRollback, "")
	if g := h.c.reviewedGen.Load(); g != 1 {
		t.Fatalf("a rollback version: gen=%d, want 1", g)
	}
}
