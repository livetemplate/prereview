package review

import (
	"strings"
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

// navFixture is two files, in drawer order b.go then a.go, holding one of each kind.
func navFixture() PrereviewState {
	return PrereviewState{
		SelectedFile: "b.go",
		Files:        []gitdiff.FileEntry{{Path: "b.go", Status: "M"}, {Path: "a.go", Status: "M"}},
		Comments: []Comment{
			{ID: "c-a9", File: "a.go", ToLine: 9},
			{ID: "c-b5", File: "b.go", ToLine: 5},
			{ID: "c-bfile", File: "b.go", Kind: "file"},
			{ID: "c-done", File: "b.go", ToLine: 1, Resolved: true},
			{ID: "c-req", File: "b.go", Kind: "file", Request: "judge"},
		},
		Suggestions: []Suggestion{
			{ID: "s-b5", File: "b.go", ToLine: 5, ProposedText: "x"},
			{ID: "s-a2", File: "a.go", ToLine: 2, ProposedText: "y"},
		},
		Quizzes: []Quiz{{ID: "z", File: "a.go", Questions: []Question{
			{ID: "q-a4", Kind: QuestionKindLine, FromLine: 4, ToLine: 4, Options: []string{"x", "y"}},
		}}},
	}
}

// decide records a verdict the way the store does: pinned to the suggestion's content.
func decide(st *PrereviewState, id, verdict string) {
	for _, sg := range st.Suggestions {
		if sg.ID == id {
			st.Decisions = append(st.Decisions, SuggestionDecision{SuggestionID: id, Verdict: verdict, Fingerprint: suggestionFingerprint(sg)})
		}
	}
}

func navIDs(items []NavItem) string {
	var ids []string
	for _, it := range items {
		ids = append(ids, it.Pos.ID)
	}
	return strings.Join(ids, " ")
}

// Order: drawer order of files; in a file, the head first, then by line, then
// comment before suggestion before question. Resolved comments and requests to the
// agent (Run judges, Quiz me) aren't stops.
func TestNavItems_DocumentOrderAcrossFiles(t *testing.T) {
	if got, want := navIDs(navFixture().NavItems()), "c-bfile c-b5 s-b5 s-a2 q-a4 c-a9"; got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

// Decided, applied and hidden suggestions are the agent's or set aside, not the
// reviewer's open work; turning Suggestions off in a file's filter drops that
// file's, and only that file's.
func TestNavItems_SuggestionsLeaveWhenDecidedOrHidden(t *testing.T) {
	st := navFixture()
	decide(&st, "s-b5", verdictAccept)
	st.Hidden = []HiddenSuggestion{NewHidden(st.Suggestions[1])}
	if got := navIDs(st.NavItems()); strings.Contains(got, "s-") {
		t.Errorf("decided and hidden suggestions should be gone, got %q", got)
	}
	st = navFixture()
	for _, sg := range st.Suggestions {
		st.setAnnFilter(sg.File, AnnFilter{Off: []string{navSuggestion}})
	}
	if got := navIDs(st.NavItems()); strings.Contains(got, "s-") {
		t.Errorf("Suggestions off everywhere: got %q", got)
	}
}

// Each item stops only where its own file's filter shows it: Suggestions off
// in one file leaves another file's suggestions as stops, and None drops every
// stop in that file while the row stays.
func TestNavItems_EachFilesOwnFilter(t *testing.T) {
	st := navFixture()
	all := navIDs(st.NavItems())
	first := st.Suggestions[0]
	st.setAnnFilter(first.File, AnnFilter{Off: []string{navSuggestion}})
	got := navIDs(st.NavItems())
	if strings.Contains(got, first.ID) {
		t.Errorf("%s's suggestion should not be a stop with Suggestions off there: %q", first.File, got)
	}
	for _, sg := range st.Suggestions[1:] {
		if sg.File != first.File && !strings.Contains(got, sg.ID) {
			t.Errorf("%s in another file should still be a stop: %q (all: %q)", sg.ID, got, all)
		}
	}
	st = navFixture()
	st.SelectedFile = first.File
	st.setAnnFilter(first.File, AnnFilter{Show: annShowNone})
	for _, it := range st.NavItems() {
		if it.File == first.File {
			t.Errorf("None on %s should drop its stops, got %s", first.File, it.Pos.ID)
		}
	}
	if !st.ShowNav() && !st.NavCollapsed() {
		t.Error("the row or its chip must stay while the file has annotations, whatever its filter")
	}
}

// The cursor is a position: once the current item is decided and drops out, next
// continues after it, not from item 1, and the label stops claiming a place.
func TestNavStep_ContinuesAfterTheCurrentItemLeaves(t *testing.T) {
	st := navFixture()
	items := st.NavItems()
	st.NavAt = &items[2].Pos // s-b5
	if got := st.NavLabel(); got != "3 / 6 open" {
		t.Errorf("label on s-b5 = %q", got)
	}
	decide(&st, "s-b5", verdictReject)
	if got := st.NavLabel(); got != "5 open" {
		t.Errorf("label after s-b5 left = %q, want \"5 open\"", got)
	}
	if next := st.navStep(+1); next == nil || next.Pos.ID != "s-a2" {
		t.Errorf("next after deciding s-b5 = %+v, want s-a2", next)
	}
	if prev := st.navStep(-1); prev == nil || prev.Pos.ID != "c-b5" {
		t.Errorf("prev after deciding s-b5 = %v, want c-b5", prev)
	}
}

// Stepping to an item opens the row the reviewer had collapsed, so the scroll lands
// on a visible card; other rows keep their state.
func TestRevealItem_OpensItsCollapsedRow(t *testing.T) {
	st := navFixture()
	st.ToggledRows = map[string]string{"5-old": "open", "5-new": "open", "9-new": "open", FileHeadRowKey: "open"}
	revealItem(&st, NavItem{Pos: NavPos{Body: true, Line: 5, Kind: navComment, ID: "c-b5"}})
	if _, ok := st.ToggledRows["5-new"]; ok {
		t.Error("row 5 should be opened")
	}
	if _, ok := st.ToggledRows["9-new"]; !ok {
		t.Error("row 9 isn't the target and should stay collapsed")
	}
	revealItem(&st, NavItem{Pos: NavPos{Kind: navComment, ID: "c-bfile"}})
	if _, ok := st.ToggledRows[FileHeadRowKey]; ok {
		t.Error("a head item should open the file-head group")
	}
}
