package review

import (
	"slices"
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

// shrunkDiff is a file that is now 3 lines long on the new side, with two base lines
// deleted, so the old side is 5 lines long.
func shrunkDiff() *gitdiff.FileDiff {
	return &gitdiff.FileDiff{Path: "a.md", Lines: []gitdiff.DiffLine{
		{Kind: "ctx", OldNum: 1, NewNum: 1},
		{Kind: "ctx", OldNum: 2, NewNum: 2},
		{Kind: "del", OldNum: 3},
		{Kind: "del", OldNum: 4},
		{Kind: "ctx", OldNum: 5, NewNum: 3},
	}}
}

func commentIDs(cs []Comment) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

// #218: an annotation whose line no longer exists has no row to hang its card on, so it
// goes in the end-of-file block. "Past the end" is per side, ignores anchor status, and
// catches a range that only ENDS past the last line.
func TestPastEndComments(t *testing.T) {
	s := PrereviewState{
		SelectedFile: "a.md",
		CurrentDiff:  shrunkDiff(),
		Comments: []Comment{
			{ID: "lastLine", File: "a.md", FromLine: 3, ToLine: 3, Side: "new", Kind: "line"},
			{ID: "pastNew", File: "a.md", FromLine: 5, ToLine: 5, Side: "new", Kind: "line", AnchorStatus: anchorOutdated},
			{ID: "pastNoSide", File: "a.md", FromLine: 4, ToLine: 4, Kind: "line"},
			{ID: "straddles", File: "a.md", FromLine: 2, ToLine: 4, Side: "new", Kind: "line"},
			{ID: "oldInRange", File: "a.md", FromLine: 4, ToLine: 4, Side: "old", Kind: "line"},
			{ID: "pastOld", File: "a.md", FromLine: 6, ToLine: 6, Side: "old", Kind: "line"},
			{ID: "resolved", File: "a.md", FromLine: 9, ToLine: 9, Side: "new", Kind: "line", Resolved: true},
			{ID: "hidden", File: "a.md", FromLine: 9, ToLine: 9, Side: "new", Kind: "line", Resolved: true, Hidden: true},
			{ID: "fileLevel", File: "a.md", Kind: commentKindFile},
			{ID: "otherFile", File: "b.md", FromLine: 9, ToLine: 9, Side: "new", Kind: "line"},
		},
	}
	got := commentIDs(s.PastEndComments())
	want := []string{"pastNew", "pastNoSide", "straddles", "pastOld", "resolved"}
	if !slices.Equal(got, want) {
		t.Fatalf("PastEndComments = %v, want %v", got, want)
	}
	if !s.PastEndHasOpen() {
		t.Error("open past-EOF comments must make the block open")
	}
	if s.rowState(PastEndRowKey) != rowStateOpen {
		t.Errorf("rowState(eof) = %q, want open", s.rowState(PastEndRowKey))
	}

	// The inline map still holds every inline comment: past-EOF ones just have no row.
	if n := len(s.CommentsByEndLine()[3]); n != 1 {
		t.Errorf("the last-line comment must stay on its row, got %d cards on line 3", n)
	}

	// All resolved → the block defaults to collapsed, like a done row.
	for i := range s.Comments {
		s.Comments[i].Resolved = true
	}
	if s.PastEndHasOpen() || s.rowState(PastEndRowKey) != rowStateCollapsed {
		t.Error("an all-resolved block must default collapsed")
	}
}

func TestPastEndComments_NoLines(t *testing.T) {
	c := []Comment{{ID: "x", File: "a.md", FromLine: 9, ToLine: 9, Side: "new", Kind: "line"}}
	for name, s := range map[string]PrereviewState{
		"no diff":   {SelectedFile: "a.md", Comments: c},
		"binary":    {SelectedFile: "a.md", Comments: c, CurrentDiff: &gitdiff.FileDiff{Path: "a.md", IsBinary: true}},
		"no select": {Comments: c, CurrentDiff: shrunkDiff()},
	} {
		if got := s.PastEndComments(); got != nil {
			t.Errorf("%s: PastEndComments = %v, want none", name, commentIDs(got))
		}
	}
}

func TestPastEndSuggestions(t *testing.T) {
	past := Suggestion{ID: "past", File: "a.md", FromLine: 5, ToLine: 5, Side: "new", OriginalText: "x", ProposedText: "y"}
	inRange := Suggestion{ID: "in", File: "a.md", FromLine: 3, ToLine: 3, Side: "new", OriginalText: "p", ProposedText: "q"}
	s := PrereviewState{SelectedFile: "a.md", CurrentDiff: shrunkDiff(), Suggestions: []Suggestion{past, inRange}}

	if got := s.PastEndSuggestions(); len(got) != 1 || got[0].ID != "past" {
		t.Fatalf("PastEndSuggestions = %+v, want [past]", got)
	}
	if !s.PastEndHasOpen() || s.PastEndCount() != 1 {
		t.Errorf("an undecided past-EOF suggestion: open=%v count=%d", s.PastEndHasOpen(), s.PastEndCount())
	}

	// Accepted but not yet applied: no longer open, but the badge's middle state.
	acc := s
	acc.Decisions = []SuggestionDecision{{SuggestionID: "past", Verdict: verdictAccept, Fingerprint: suggestionFingerprint(past)}}
	if acc.PastEndHasOpen() || !acc.PastEndHasAccepted() {
		t.Errorf("accepted past-EOF suggestion: open=%v accepted=%v, want false/true", acc.PastEndHasOpen(), acc.PastEndHasAccepted())
	}

	hidden := s
	hidden.Hidden = []HiddenSuggestion{{SuggestionID: "past", Fingerprint: suggestionFingerprint(past)}}
	if got := hidden.PastEndSuggestions(); len(got) != 0 {
		t.Errorf("a per-card hidden suggestion must stay hidden, got %+v", got)
	}
	off := s
	off.HideSuggestions = true
	if got := off.PastEndSuggestions(); len(got) != 0 {
		t.Errorf("hide-suggestions must hide past-EOF suggestions too, got %+v", got)
	}
}
