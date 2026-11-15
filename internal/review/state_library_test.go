package review

import (
	"strings"
	"testing"

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
	if len(p.Cards) != 1 || p.Cards[0].ID != "a" || !p.Cards[0].CanEdit || p.Cards[0].CanAccept {
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
