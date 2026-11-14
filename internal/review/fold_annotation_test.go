package review

import (
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

func hasKind(lines []gitdiff.DiffLine, kind string) bool {
	for _, l := range lines {
		if l.Kind == kind {
			return true
		}
	}
	return false
}

// #231: a card's row must survive Diff view's folding, whatever kind of
// annotation hangs on it, while unrelated unchanged runs still fold.
func TestVisibleLines_AnnotationKeepsRowOutOfFold(t *testing.T) {
	base := PrereviewState{SelectedFile: "a.md", CurrentDiff: fdWithChange(30)}
	if hasLine(base.VisibleLines(), 15) {
		t.Fatal("precondition: line 15 is folded with no annotation")
	}

	cases := map[string]PrereviewState{}
	c := base
	c.Comments = []Comment{{ID: "c", File: "a.md", FromLine: 8, ToLine: 15, Side: "new", Kind: "line"}}
	cases["comment"] = c
	r := c
	r.Comments = []Comment{{ID: "c", File: "a.md", FromLine: 15, ToLine: 15, Side: "new", Kind: "line", Resolved: true}}
	cases["resolved comment"] = r
	q := base
	q.Quizzes = []Quiz{{ID: "z", File: "a.md", Questions: []Question{{ID: "q", Kind: QuestionKindLine, FromLine: 15, ToLine: 15}}}}
	cases["quiz question"] = q

	for name, s := range cases {
		vl := s.VisibleLines()
		if !hasLine(vl, 15) {
			t.Errorf("%s on line 15: its row must be kept out of the fold", name)
		}
		if hasLine(vl, 8) {
			t.Errorf("%s: only the card's ToLine (+context) is kept, not the whole range", name)
		}
		if !hasKind(vl, "fold") {
			t.Errorf("%s: unrelated unchanged runs must still fold", name)
		}
	}

	h := base
	h.Comments = []Comment{{ID: "h", File: "a.md", FromLine: 15, ToLine: 15, Side: "new", Kind: "line", Hidden: true}}
	if hasLine(h.VisibleLines(), 15) {
		t.Error("a Hidden comment has no card, so it must not keep its row")
	}
}

// #231: a suggestion used to reveal the whole working-tree file (#98), which
// dropped every del row — an old-side comment then had no row. It now keeps
// only its own row, so Diff view stays a diff.
func TestVisibleLines_SuggestionKeepsDelRows(t *testing.T) {
	d := &gitdiff.FileDiff{Path: "a.md"}
	d.Lines = append(d.Lines, gitdiff.DiffLine{Kind: "del", OldNum: 1}, gitdiff.DiffLine{Kind: "add", NewNum: 1})
	for i := 2; i <= 30; i++ {
		d.Lines = append(d.Lines, gitdiff.DiffLine{Kind: "ctx", OldNum: i, NewNum: i})
	}
	s := PrereviewState{
		SelectedFile: "a.md", CurrentDiff: d,
		Suggestions: []Suggestion{{ID: "s", File: "a.md", FromLine: 20, ToLine: 20, Side: "new", OriginalText: "x", ProposedText: "y"}},
		Comments:    []Comment{{ID: "old", File: "a.md", FromLine: 1, ToLine: 1, Side: "old", Kind: "line"}},
	}
	vl := s.VisibleLines()
	if !hasKind(vl, "del") {
		t.Error("the del row (and the old-side comment on it) must survive a suggestion on the file")
	}
	if !hasLine(vl, 20) || !hasKind(vl, "fold") {
		t.Errorf("the suggestion's row is kept and the rest still folds: line20=%v fold=%v", hasLine(vl, 20), hasKind(vl, "fold"))
	}
}
