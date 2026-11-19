package review

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/library"
)

// TestAutoLearn: once this review holds AutoLearnMinSignals new reviewer
// signals, an idle agent's review queues ONE learning job for the background
// learner — never a comment (comments.csv is untouched, no tab or open file is
// needed) — carrying the auto-accept pref; queueing resets the count.
func TestAutoLearn(t *testing.T) {
	c := checksController(t)
	c.LibraryDir = t.TempDir()
	store := filepath.Dir(c.CSVPath)
	seed := func(n int) {
		var rows []csv.Row
		for i := 0; i < n; i++ {
			rows = append(rows, csv.Row{ID: fmt.Sprint("c", i), File: "doc.md", FromLine: 1, ToLine: 1, Body: "tighten", CreatedAt: time.Now()})
		}
		if err := c.CSVWriter.Write(rows); err != nil {
			t.Fatal(err)
		}
	}
	seed(AutoLearnMinSignals - 1)
	if c.maybeAutoLearn(); len(library.OpenJobs(c.LibraryDir)) != 0 {
		t.Fatal("queued a job with too few signals")
	}
	seed(AutoLearnMinSignals)
	before, err := os.ReadFile(c.CSVPath)
	if err != nil {
		t.Fatal(err)
	}
	c.maybeAutoLearn()
	jobs := library.OpenJobs(c.LibraryDir)
	if len(jobs) != 1 || !slices.Equal(jobs[0].Stores, []string{store}) || jobs[0].Server != store || !jobs[0].AutoAccept {
		t.Fatalf("jobs = %+v, want one auto-accept job for this store", jobs)
	}
	if after, _ := os.ReadFile(c.CSVPath); string(after) != string(before) {
		t.Fatal("queueing a learning job changed comments.csv")
	}
	if e := library.LastExtractions(c.LibraryDir)[store]; e.Signals != AutoLearnMinSignals {
		t.Fatalf("extraction recorded %d signals, want %d", e.Signals, AutoLearnMinSignals)
	}
	var st PrereviewState
	if c.applyLibrary(&st); !st.LearnReading || st.BootCard() == nil || !st.BootCard().Reading {
		t.Fatal("the open job doesn't show as being read")
	}

	seed(2 * AutoLearnMinSignals) // enough again, but the open job covers this store
	if c.maybeAutoLearn(); len(library.OpenJobs(c.LibraryDir)) != 1 {
		t.Fatal("queued a second job while one is open for this store")
	}
	if err := library.FinishJob(c.LibraryDir, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	saveUIPrefs(c.UIPrefsPath, UIPrefs{AutoAcceptOff: true})
	if c.maybeAutoLearn(); len(library.OpenJobs(c.LibraryDir)) != 1 || library.OpenJobs(c.LibraryDir)[0].AutoAccept {
		t.Fatalf("with auto-accept off: %+v, want one job without it", library.OpenJobs(c.LibraryDir))
	}

	seed(3 * AutoLearnMinSignals)
	library.FinishJob(c.LibraryDir, library.OpenJobs(c.LibraryDir)[0].ID)
	saveUIPrefs(c.UIPrefsPath, UIPrefs{AutoLearnOff: true})
	if c.maybeAutoLearn(); len(library.OpenJobs(c.LibraryDir)) != 0 {
		t.Fatal("queued a job with Learn after each review off")
	}
}

// TestReadPastReviews_NoFileOpen: "Read past reviews" queues one job naming the
// ticked stores, with no file open and nothing written to comments.csv.
func TestReadPastReviews_NoFileOpen(t *testing.T) {
	c := checksController(t)
	c.LibraryDir = t.TempDir()
	a, b := t.TempDir(), t.TempDir()
	st := PrereviewState{AgentMode: true, BootStores: []library.StoreInfo{{Store: a}, {Store: b}}, BootSkip: []string{b}}
	got, err := c.ReadPastReviews(st, checksCtx("ReadPastReviews", nil))
	if err != nil {
		t.Fatalf("read past reviews with no file open: %v", err)
	}
	jobs := library.OpenJobs(c.LibraryDir)
	if len(jobs) != 1 || !slices.Equal(jobs[0].Stores, []string{a}) {
		t.Fatalf("jobs = %+v, want one naming only the ticked store", jobs)
	}
	if !library.Bootstrapped(c.LibraryDir) || got.BootOpen {
		t.Fatal("the offer wasn't answered")
	}
	if rows := c.loadCommentsFromDisk(); len(rows) != 0 {
		t.Fatalf("wrote %d comments", len(rows))
	}
}

// TestLearnAdded: a learning accepted on arrival shows in the notice (and the
// badge) until it's seen — closed, or the Learnings tab opened — in any tab;
// Undo rejects what it names; building a tab's state never marks them seen.
func TestLearnAdded(t *testing.T) {
	c := checksController(t)
	c.LibraryDir = t.TempDir()
	store := filepath.Dir(c.CSVPath)
	if err := c.CSVWriter.Write([]csv.Row{{ID: "c1", File: "doc.md", FromLine: 1, ToLine: 1, Body: "tighten", CreatedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	learn := func(text string) string {
		t.Helper()
		time.Sleep(2 * time.Millisecond) // created after any earlier seen time
		j, _, err := library.QueueJob(c.LibraryDir, library.Job{Stores: []string{store}, AutoAccept: true}, false)
		if err != nil {
			t.Fatal(err)
		}
		l, err := library.Propose(c.LibraryDir, library.Proposal{Text: text, Evidence: []string{store + ":c1"}, Job: j.ID})
		if err != nil {
			t.Fatal(err)
		}
		return l.ID
	}
	id := learn("Cut filler words.")
	var st PrereviewState
	c.applyLibrary(&st)
	if len(st.LearnAdded) != 1 || st.LearnAdded[0].ID != id || st.LearnNew != 1 || st.LearnAddedLabel() != "Learned “Cut filler words.”" {
		t.Fatalf("notice = %+v badge %d label %q", st.LearnAdded, st.LearnNew, st.LearnAddedLabel())
	}
	if c.applyLibrary(&st); len(st.LearnAdded) != 1 {
		t.Fatal("building the state marked the notice seen")
	}
	if got, _ := c.DismissLearned(st, checksCtx("DismissLearned", nil)); len(got.LearnAdded) != 0 || got.LearnNew != 0 {
		t.Fatalf("dismissed, still %+v", got.LearnAdded)
	}
	var other PrereviewState // another tab: its next render agrees
	if c.applyLibrary(&other); len(other.LearnAdded) != 0 {
		t.Fatal("another tab still shows a dismissed notice")
	}

	learn("Lead with the point.")
	learn("Say it once.")
	if c.applyLibrary(&st); len(st.LearnAdded) != 2 || st.LearnAddedLabel() != "2 learnings added" {
		t.Fatalf("notice = %+v", st.LearnAdded)
	}
	got, err := c.UndoLearned(st, checksCtx("UndoLearned", nil))
	if err != nil || len(got.LearnAdded) != 0 {
		t.Fatalf("undo: %v, notice %+v", err, got.LearnAdded)
	}
	for _, l := range library.LoadLearnings(c.LibraryDir) {
		if want := map[bool]string{true: library.StatusAccepted, false: library.StatusRejected}[l.ID == id]; l.Status != want {
			t.Errorf("%s (%s) is %s, want %s", l.ID, l.Text, l.Status, want)
		}
	}

	learn("Prefer plain words.")
	lib := PrereviewState{ShowLibrary: true, LibraryTab: libTabChecks}
	got, _ = c.SetLibraryTab(lib, checksCtx("SetLibraryTab", map[string]any{"tab": libTabLearnings}))
	if len(got.LearnAdded) != 0 || len(addedSince(c.learnings(), library.LearnSeenAt(c.LibraryDir))) != 0 {
		t.Fatal("opening Learnings didn't mark the added learnings seen")
	}
	learn("Cut the hedges.")
	got, _ = c.ReviewLearned(PrereviewState{}, checksCtx("ReviewLearned", nil))
	if !got.ShowLibrary || got.LearnStatus != library.StatusAccepted || len(addedSince(c.learnings(), library.LearnSeenAt(c.LibraryDir))) != 0 {
		t.Fatalf("Review: library %v status %q", got.ShowLibrary, got.LearnStatus)
	}
}

// TestLearnJobStalled: a job queued for this review shows as stalled once it
// has waited past LearnerStale with no learner running, and not while one is.
func TestLearnJobStalled(t *testing.T) {
	c := checksController(t)
	c.LibraryDir = t.TempDir()
	old := library.LearnerStale
	library.LearnerStale = 20 * time.Millisecond
	t.Cleanup(func() { library.LearnerStale = old })
	if _, _, err := library.QueueJob(c.LibraryDir, library.Job{Stores: []string{"/x/.prereview"}, Server: filepath.Dir(c.CSVPath)}, false); err != nil {
		t.Fatal(err)
	}
	if open, stalled := c.learnJob(); !open || stalled {
		t.Fatalf("fresh job: open %v stalled %v, want open and not stalled", open, stalled)
	}
	c.learnViewChanged() // baseline
	time.Sleep(40 * time.Millisecond)
	if open, stalled := c.learnJob(); !open || !stalled {
		t.Fatalf("waited with no learner: open %v stalled %v", open, stalled)
	}
	if !c.learnViewChanged() {
		t.Fatal("the tabs aren't told when a job stalls")
	}
	if err := library.TouchLearner(c.LibraryDir); err != nil {
		t.Fatal(err)
	}
	if _, stalled := c.learnJob(); stalled {
		t.Fatal("stalled with a learner running")
	}
}

// TestToggleAutoAccept: the switch persists, and the next job carries it.
func TestToggleAutoAccept(t *testing.T) {
	c := checksController(t)
	got, _ := c.ToggleAutoAccept(PrereviewState{}, checksCtx("ToggleAutoAccept", nil))
	if !got.AutoAcceptOff || !loadUIPrefs(c.UIPrefsPath).AutoAcceptOff {
		t.Fatal("turning auto-accept off didn't persist")
	}
	if got, _ = c.ToggleAutoAccept(got, checksCtx("ToggleAutoAccept", nil)); got.AutoAcceptOff || loadUIPrefs(c.UIPrefsPath).AutoAcceptOff {
		t.Fatal("turning it back on didn't persist")
	}
}
