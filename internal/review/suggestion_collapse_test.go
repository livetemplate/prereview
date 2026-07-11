package review

import "testing"

// TestSuggestionCollapse: an applied suggestion auto-collapses out of the inline
// render (SuggestionsByEndLine) by default; expanding it (ExpandedSuggestions) brings
// the inline box back. The ✦ applied badge (AppliedByLine) is ALWAYS present for an
// applied suggestion — it's the expand/collapse toggle — and an applied suggestion is
// never in the green "to review" count (SuggestionCountLines), so the two suggestion
// badges never double-report (#159 M4.3b).
func TestSuggestionCollapse(t *testing.T) {
	base := PrereviewState{
		SelectedFile: "a.go",
		Suggestions: []Suggestion{
			{ID: "acc", File: "a.go", ToLine: 3, Side: "new"}, // accepted, not applied
			{ID: "app", File: "a.go", ToLine: 7, Side: "new"}, // applied
		},
		Applied: map[string]bool{"app": true},
	}

	// Default: applied suggestion is collapsed; accepted-pending one is not.
	if !base.suggestionCollapsed("app") {
		t.Error("an applied suggestion should collapse by default")
	}
	if base.suggestionCollapsed("acc") {
		t.Error("a not-yet-applied suggestion must never collapse")
	}

	// Collapsed: no inline box; a ✦ badge; excluded from the green count.
	inline := base.SuggestionsByEndLine()
	if len(inline[7]) != 0 {
		t.Errorf("collapsed applied suggestion should NOT render inline; got %d on line 7", len(inline[7]))
	}
	if len(inline[3]) != 1 {
		t.Errorf("accepted suggestion should still render inline; got %d on line 3", len(inline[3]))
	}
	if badges := base.AppliedByLine(); len(badges[7]) != 1 || badges[7][0].ID != "app" {
		t.Errorf("applied suggestion should have a ✦ badge on line 7; got %+v", badges[7])
	}
	if base.SuggestionCountLines()["7-new"] != 0 {
		t.Error("the green suggestion count must EXCLUDE an applied suggestion")
	}
	if base.SuggestionCountLines()["3-new"] != 1 {
		t.Error("the green count includes the non-applied accepted suggestion")
	}
	if base.AppliedBadgeLines()["7-new"] != 1 {
		t.Error("the ✦ badge count should be 1 on the applied line")
	}

	// Expanded: the box comes back inline, the ✦ badge STAYS (it's the toggle), and
	// the applied suggestion is still out of the green count.
	expanded := base
	expanded.ExpandedSuggestions = map[string]bool{"app": true}
	if expanded.suggestionCollapsed("app") {
		t.Error("an expanded applied suggestion is not collapsed")
	}
	if len(expanded.SuggestionsByEndLine()[7]) != 1 {
		t.Error("an expanded applied suggestion should render inline again")
	}
	if len(expanded.AppliedByLine()[7]) != 1 {
		t.Error("the ✦ badge must persist while the applied box is expanded (it's the toggle)")
	}
	if expanded.SuggestionCountLines()["7-new"] != 0 {
		t.Error("an applied suggestion stays out of the green count even when expanded")
	}
}
