package gitdiff

import (
	"strings"
	"testing"
)

// ctxLine / addLine / delLine build DiffLines tersely for these tests.
func ctxLine(n int) DiffLine { return DiffLine{OldNum: n, NewNum: n, Kind: "ctx"} }
func addLine(n int) DiffLine { return DiffLine{NewNum: n, Kind: "add"} }
func delLine(n int) DiffLine { return DiffLine{OldNum: n, Kind: "del"} }

// kinds renders the Kind sequence for compact assertions; a fold is
// shown as f<count>.
func kinds(lines []DiffLine) string {
	var b strings.Builder
	for _, l := range lines {
		if l.Kind == "fold" {
			b.WriteString("f" + l.Content + " ")
			continue
		}
		b.WriteString(l.Kind + " ")
	}
	return b.String()
}

func TestCollapse_NoChange_ReturnsInput(t *testing.T) {
	in := []DiffLine{ctxLine(1), ctxLine(2), ctxLine(3)}
	out := CollapseToHunks(in, 3, nil)
	if len(out) != 3 {
		t.Fatalf("no-change file should be returned unchanged, got %d lines", len(out))
	}
	for _, l := range out {
		if l.Kind == "fold" {
			t.Fatal("no fold marker expected when there are no changes")
		}
	}
}

func TestCollapse_MiddleGapFolded(t *testing.T) {
	// 20 ctx lines with a single add at line 10, ctx=3.
	var in []DiffLine
	for i := 1; i <= 20; i++ {
		if i == 10 {
			in = append(in, addLine(i))
		} else {
			in = append(in, ctxLine(i))
		}
	}
	out := CollapseToHunks(in, 3, nil)
	// Expect: fold(lines 1..6) , ctx7 ctx8 ctx9 add10 ctx11 ctx12 ctx13 , fold(14..20)
	got := kinds(out)
	want := "f6 ctx ctx ctx add ctx ctx ctx f7 "
	if got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	// First fold carries the count and the first skipped line's numbers.
	if out[0].Kind != "fold" || out[0].Content != "6" {
		t.Fatalf("first fold = %+v, want Kind=fold Content=6", out[0])
	}
	if out[0].OldNum != 1 || out[0].NewNum != 1 {
		t.Errorf("fold should carry first skipped line's numbers (1,1), got (%d,%d)", out[0].OldNum, out[0].NewNum)
	}
}

func TestCollapse_AdjacentHunksNoFoldBetween(t *testing.T) {
	// Changes close enough that their context windows overlap → no
	// fold separates them.
	var in []DiffLine
	for i := 1; i <= 10; i++ {
		switch i {
		case 4:
			in = append(in, addLine(i))
		case 7:
			in = append(in, delLine(i))
		default:
			in = append(in, ctxLine(i))
		}
	}
	out := CollapseToHunks(in, 3, nil)
	// ctx windows of line4 (1..7) and line7 (4..10) cover everything → no fold.
	for _, l := range out {
		if l.Kind == "fold" {
			t.Fatalf("overlapping context should leave no fold; got %q", kinds(out))
		}
	}
	if len(out) != 10 {
		t.Fatalf("expected all 10 lines kept, got %d", len(out))
	}
}

func TestCollapse_LeadingAndTrailingFolds(t *testing.T) {
	var in []DiffLine
	for i := 1; i <= 12; i++ {
		if i == 6 {
			in = append(in, delLine(i))
		} else {
			in = append(in, ctxLine(i))
		}
	}
	out := CollapseToHunks(in, 2, nil)
	// fold(1..3)=3, ctx4 ctx5 del6 ctx7 ctx8, fold(9..12)=4
	if out[0].Kind != "fold" || out[0].Content != "3" {
		t.Errorf("leading fold = %+v, want count 3", out[0])
	}
	last := out[len(out)-1]
	if last.Kind != "fold" || last.Content != "4" {
		t.Errorf("trailing fold = %+v, want count 4", last)
	}
}

func TestCollapse_AllAdd_NoFold(t *testing.T) {
	var in []DiffLine
	for i := 1; i <= 8; i++ {
		in = append(in, addLine(i))
	}
	out := CollapseToHunks(in, 3, nil)
	if len(out) != 8 {
		t.Fatalf("all-add file: expected 8 lines, got %d (%q)", len(out), kinds(out))
	}
	for _, l := range out {
		if l.Kind == "fold" {
			t.Fatal("all-add file should have no folds")
		}
	}
}

// pinnedFile is a change on line 1 followed by unchanged lines 2..n.
func pinnedFile(n int) []DiffLine {
	in := []DiffLine{addLine(1)}
	for i := 2; i <= n; i++ {
		in = append(in, ctxLine(i))
	}
	return in
}

func pinNew(nums ...int) func(DiffLine) bool {
	return func(l DiffLine) bool {
		for _, n := range nums {
			if l.NewNum == n {
				return true
			}
		}
		return false
	}
}

// #231: a kept (pinned) unchanged line stays out of the folds with the same
// context as a change, so a card hung on it has a row.
func TestCollapse_PinnedLineKeptWithContext(t *testing.T) {
	out := CollapseToHunks(pinnedFile(20), 2, pinNew(15))
	want := "add ctx ctx f9 ctx ctx ctx ctx ctx f3 "
	if got := kinds(out); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if out[4].NewNum != 13 || out[8].NewNum != 17 {
		t.Errorf("pinned run should be lines 13..17, got %d..%d", out[4].NewNum, out[8].NewNum)
	}
}

func TestCollapse_NearbyPinsMerge(t *testing.T) {
	// Pins on 10 and 13 with ctx 1 overlap into one run 9..14.
	got := kinds(CollapseToHunks(pinnedFile(20), 1, pinNew(10, 13)))
	if want := "add ctx f6 ctx ctx ctx ctx ctx ctx f6 "; got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestCollapse_PinOnUnchangedFile_ReturnsInput(t *testing.T) {
	in := []DiffLine{ctxLine(1), ctxLine(2), ctxLine(3)}
	if got := kinds(CollapseToHunks(in, 0, pinNew(2))); got != "ctx ctx ctx " {
		t.Fatalf("an unchanged file is shown whole regardless of pins, got %q", got)
	}
}
