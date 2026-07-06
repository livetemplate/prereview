package review

import (
	"path/filepath"
	"testing"
)

// visIDs returns the IDs the render surfaces would show (through the single
// visibleSuggestions seam).
func visIDs(s PrereviewState) []string {
	var ids []string
	for _, sg := range s.visibleSuggestions() {
		ids = append(ids, sg.ID)
	}
	return ids
}

func hasID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestHidden_RoundTripAndTolerant: the store round-trips and dedupes by id (last
// write wins), and a missing file is nil — mirrors the decisions store.
func TestHidden_RoundTripAndTolerant(t *testing.T) {
	path := filepath.Join(t.TempDir(), HiddenSuggestionFileName)
	if got := loadHidden(path); got != nil {
		t.Fatalf("missing file: want nil, got %v", got)
	}
	if err := writeHidden(path, []HiddenSuggestion{{SuggestionID: "a", Fingerprint: "fp1"}}); err != nil {
		t.Fatalf("writeHidden: %v", err)
	}
	got := loadHidden(path)
	if len(got) != 1 || got[0].SuggestionID != "a" || got[0].Fingerprint != "fp1" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

// TestHideSuggestion: hiding one suggestion drops just it from every render
// surface; the others stay.
func TestHideSuggestion(t *testing.T) {
	c := decisionController(t)
	st := PrereviewState{
		SelectedFile: "a.md",
		Suggestions: []Suggestion{
			{ID: "a", File: "a.md", ProposedText: "x"},
			{ID: "b", File: "a.md", ProposedText: "y"},
		},
	}
	st, _ = c.HideSuggestion(st, decisionCtx("hideSuggestion", "a", ""))
	ids := visIDs(st)
	if hasID(ids, "a") {
		t.Errorf("hidden suggestion a still visible: %v", ids)
	}
	if !hasID(ids, "b") {
		t.Errorf("un-hidden suggestion b should stay visible: %v", ids)
	}
	if st.HiddenSuggestionCount() != 1 {
		t.Errorf("HiddenSuggestionCount = %d, want 1", st.HiddenSuggestionCount())
	}
	// Persisted to disk (durable).
	if got := loadHidden(c.hiddenPath()); len(got) != 1 || got[0].SuggestionID != "a" {
		t.Errorf("hide should persist to disk, got %+v", got)
	}
}

// TestHideSuggestionGroup: hiding a group hides every alternative in the same
// area (#117), in one write.
func TestHideSuggestionGroup(t *testing.T) {
	c := decisionController(t)
	st := PrereviewState{
		SelectedFile: "a.md",
		Suggestions: []Suggestion{
			groupAlt("a", "x"), groupAlt("b", "y"), // same area → group
			{ID: "lone", File: "a.md", Side: "new", FromLine: 9, ToLine: 9, OriginalText: "foo", ProposedText: "bar"},
		},
	}
	st, _ = c.HideSuggestionGroup(st, decisionCtx("hideSuggestionGroup", "a", ""))
	ids := visIDs(st)
	if hasID(ids, "a") || hasID(ids, "b") {
		t.Errorf("whole group should be hidden, still visible: %v", ids)
	}
	if !hasID(ids, "lone") {
		t.Errorf("a suggestion outside the group must stay visible: %v", ids)
	}
}

// TestHide_RevealsOnRevision: hiding pins the content fingerprint, so a same-id
// revision (new proposed text) un-hides automatically — fresh work is never
// silently swallowed (the same guarantee decisions and #116 make).
func TestHide_RevealsOnRevision(t *testing.T) {
	c := decisionController(t)
	st := PrereviewState{
		SelectedFile: "a.md",
		Suggestions:  []Suggestion{{ID: "a", File: "a.md", OriginalText: "o", ProposedText: "v1"}},
	}
	st, _ = c.HideSuggestion(st, decisionCtx("hideSuggestion", "a", ""))
	if hasID(visIDs(st), "a") {
		t.Fatal("a should be hidden after HideSuggestion")
	}
	// The LLM revises a (same id, new content). The stored fingerprint no longer
	// matches → a reappears.
	st.Suggestions = []Suggestion{{ID: "a", File: "a.md", OriginalText: "o", ProposedText: "v2-revised"}}
	if !hasID(visIDs(st), "a") {
		t.Error("a revised suggestion must re-appear (fingerprint no longer matches the hide)")
	}
}

// TestShowHiddenSuggestions: the recovery clears the selected file's hides and
// leaves other files' hides intact.
func TestShowHiddenSuggestions(t *testing.T) {
	c := decisionController(t)
	st := PrereviewState{
		SelectedFile: "a.md",
		Suggestions: []Suggestion{
			{ID: "a", File: "a.md", ProposedText: "x"},
			{ID: "z", File: "other.md", ProposedText: "z"},
		},
	}
	st, _ = c.HideSuggestion(st, decisionCtx("hideSuggestion", "a", ""))
	// Hide one on another file too (simulate the reviewer having browsed there).
	st.SelectedFile = "other.md"
	st, _ = c.HideSuggestion(st, decisionCtx("hideSuggestion", "z", ""))
	// Back on a.md, "show hidden" clears only a.md's hide.
	st.SelectedFile = "a.md"
	st, _ = c.ShowHiddenSuggestions(st, decisionCtx("showHiddenSuggestions", "", ""))
	if !hasID(visIDs(st), "a") {
		t.Error("show hidden should bring a back on a.md")
	}
	if len(st.Hidden) != 1 || st.Hidden[0].SuggestionID != "z" {
		t.Errorf("other files' hides must survive, got %+v", st.Hidden)
	}
}

// TestHideIsViewOnly: hiding is a pure view filter — it must NOT record a
// decision, so the suggestion is still an open (un-decided) item for hand-off.
func TestHideIsViewOnly(t *testing.T) {
	c := decisionController(t)
	st := PrereviewState{
		SelectedFile: "a.md",
		Suggestions:  []Suggestion{{ID: "a", File: "a.md", ProposedText: "x"}},
	}
	st, _ = c.HideSuggestion(st, decisionCtx("hideSuggestion", "a", ""))
	if len(st.Decisions) != 0 {
		t.Errorf("hiding must not record a decision, got %+v", st.Decisions)
	}
	if _, decided := st.DecisionsBySuggestion()["a"]; decided {
		t.Error("a hidden suggestion must remain undecided (open for hand-off)")
	}
}
