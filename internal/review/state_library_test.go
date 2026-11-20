package review

import (
	"slices"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/library"
)

func TestLearnPage_FiltersAndEmpty(t *testing.T) {
	s := PrereviewState{ShowLibrary: true}
	if p := s.LearnPage(); p == nil || !strings.Contains(p.Empty, "No learnings yet") {
		t.Fatalf("empty library: %+v", p)
	}
	s.Learnings = []library.Learning{
		{ID: "a", Text: "A", Scope: "Docs", Status: library.StatusAccepted},
		{ID: "b", Text: "B", Scope: "Email", Status: library.StatusAccepted},
		{ID: "c", Text: "C", Scope: "Docs", Status: library.StatusProposed, Replaces: "gone"},
	}
	if p := s.LearnPage(); len(p.Cards) != 1 || p.Cards[0].ID != "c" || p.Cards[0].Conflict != "" {
		t.Fatalf("default = proposed, and a missing replaced learning has no note: %+v", p.Cards)
	}
	s.LearnStatus, s.LearnScope = library.StatusAccepted, "Docs"
	p := s.LearnPage()
	if len(p.Cards) != 1 || p.Cards[0].ID != "a" || !slices.Contains(p.Cards[0].Actions, actEdit) || slices.Contains(p.Cards[0].Actions, actAccept) {
		t.Fatalf("accepted in Docs: %+v", p.Cards)
	}
	if len(p.Scopes) != 2 || !p.Scopes[0].Selected {
		t.Fatalf("scopes = %+v", p.Scopes)
	}
	s.LearnStatus = library.StatusRejected
	if p := s.LearnPage(); p.Empty != "No rejected learnings in this scope." {
		t.Fatalf("empty filter text = %q", p.Empty)
	}
	s.LibraryTab = libTabChecks
	if s.LearnPage() != nil || s.CheckPage() == nil {
		t.Fatal("the Checks tab shows the Checks page, not Learnings")
	}
}

func TestStoreLabel(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/notes/.prereview":            "notes",
		"/home/u/notes/.prereview/plan.md-1a": "notes/plan.md-1a",
	} {
		if got := storeLabel(in); got != want {
			t.Errorf("storeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBootCard_WhenItShows(t *testing.T) {
	stores := []library.StoreInfo{{Store: "/h/a/.prereview", Label: "~/a", Comments: 2}, {Store: "/h/b/.prereview", Label: "~/b", Comments: 1}}
	s := PrereviewState{BootStores: stores}
	if s.BootCard() != nil {
		t.Fatal("shown while the scan is still running")
	}
	s.BootScanned = true
	c := s.BootCard()
	if c == nil || c.Picked != 2 || c.CanRead {
		t.Fatalf("offer = %+v, want both ticked and no agent", c)
	}
	s.BootSkip, s.AgentMode = []string{"/h/b/.prereview"}, true
	if c := s.BootCard(); c.Picked != 1 || !c.CanRead || s.pickedBootStores()[0] != "/h/a/.prereview" || len(s.pickedBootStores()) != 1 {
		t.Fatalf("unticked b: %+v %v", c, s.pickedBootStores())
	}
	s.Bootstrapped = true
	if s.BootCard() != nil {
		t.Fatal("offered again after it was answered")
	}
	// Progress shows only on the Learnings tab, never above the file.
	s.LearnReading = true
	if c := s.BootCard(); c != nil {
		t.Fatalf("an open learning job showed above the file: %+v", c)
	}
	s.ShowLibrary, s.LibraryTab = true, libTabLearnings
	if c := s.LibraryBootCard(); c == nil || !c.Reading || c.Stalled {
		t.Fatalf("an open learning job shows progress on Learnings: %+v", c)
	}
	s.LearnStalled = true
	if c := s.LibraryBootCard(); c == nil || !c.Stalled {
		t.Fatalf("a job with no learner says so: %+v", c)
	}
	s.LearnReading, s.LearnStalled = false, false
	if s.LibraryBootCard() != nil {
		t.Fatal("progress kept showing after the job finished")
	}
	// A request comment queued before learning moved to jobs still shows progress.
	s.Comments = []Comment{{ID: "x", Kind: commentKindFile, Request: csv.RequestExtraction, Body: "Read past reviews…"}}
	if c := s.LibraryBootCard(); c == nil || !c.Reading {
		t.Fatalf("an open extraction request shows progress: %+v", c)
	}
	s.ShowLibrary = false
	if s.BootCard() != nil {
		t.Fatal("an open extraction request showed above the file")
	}
	s.Comments[0].Processed = true
	// An unanswered offer still shows above the file.
	s.Bootstrapped = false
	if c := s.BootCard(); c == nil || c.Reading || len(c.Stores) != 2 {
		t.Fatalf("the unanswered offer is gone from above the file: %+v", c)
	}
	s.Bootstrapped = true
	s.ShowLibrary, s.BootOpen = true, true
	if c := s.LibraryBootCard(); c == nil || len(c.Stores) != 2 {
		t.Fatalf("Read past reviews reopens the offer on the Learnings tab: %+v", c)
	}
}
