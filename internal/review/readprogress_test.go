package review

import (
	"context"
	"testing"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
)

func viewportCtx(top, bottom string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), "reportViewport",
		map[string]interface{}{"topKey": top, "bottomKey": bottom})
}

func TestKeyLine(t *testing.T) {
	cases := map[string]int{
		// code lines "L<old>-<new>" → new
		"L4-5": 5, "L0-5": 5, "L10-12": 12,
		// markdown blocks "MB-<start>-<end>" → end
		"MB-5-8": 8, "MB-1-1": 1, "MB-40-42": 42,
		// unparseable
		"": 0, "garbage": 0, "L4-x": 0,
	}
	for k, want := range cases {
		if got := keyLine(k); got != want {
			t.Errorf("keyLine(%q) = %d, want %d", k, got, want)
		}
	}
}

// TestReportViewport_HighWaterMark (#128): the read mark only ever advances
// (scrolling back up doesn't un-read what you've seen), while the restore target
// follows the current top.
func TestReportViewport_HighWaterMark(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{SelectedFile: "a.go"}

	st, _ = c.ReportViewport(st, viewportCtx("L1-1", "L10-10"))
	if st.ReadThrough["a.go"] != 10 {
		t.Fatalf("read-through should be 10, got %d", st.ReadThrough["a.go"])
	}
	if st.LastReadTopKey["a.go"] != "L1-1" {
		t.Errorf("restore target should be L1-1, got %q", st.LastReadTopKey["a.go"])
	}

	// Scroll further down → mark advances.
	st, _ = c.ReportViewport(st, viewportCtx("L15-15", "L25-25"))
	if st.ReadThrough["a.go"] != 25 {
		t.Errorf("mark should advance to 25, got %d", st.ReadThrough["a.go"])
	}

	// Scroll back up → mark holds (already read), restore target follows.
	st, _ = c.ReportViewport(st, viewportCtx("L2-2", "L8-8"))
	if st.ReadThrough["a.go"] != 25 {
		t.Errorf("scrolling up must not lower the mark, got %d", st.ReadThrough["a.go"])
	}
	if st.LastReadTopKey["a.go"] != "L2-2" {
		t.Errorf("restore target should follow current top L2-2, got %q", st.LastReadTopKey["a.go"])
	}
}

// TestReportViewport_ClearsRestoreAndScopes: a report clears a pending one-shot
// restore (the reviewer scrolled), and progress is tracked per file.
func TestReportViewport_ClearsRestoreAndScopes(t *testing.T) {
	c := &PrereviewController{}

	// No selected file → no-op.
	st, _ := c.ReportViewport(PrereviewState{}, viewportCtx("L1-1", "L5-5"))
	if len(st.ReadThrough) != 0 {
		t.Errorf("no selected file must not record progress, got %+v", st.ReadThrough)
	}

	// A pending restore is cleared once the reviewer scrolls.
	st = PrereviewState{SelectedFile: "a.go", ScrollToReadKey: "L9-9"}
	st, _ = c.ReportViewport(st, viewportCtx("L1-1", "L10-10"))
	if st.ScrollToReadKey != "" {
		t.Errorf("a report should clear the one-shot restore, got %q", st.ScrollToReadKey)
	}

	// Per-file marks.
	st.SelectedFile = "b.go"
	st, _ = c.ReportViewport(st, viewportCtx("L1-1", "L3-3"))
	if st.ReadThrough["a.go"] != 10 || st.ReadThrough["b.go"] != 3 {
		t.Errorf("per-file marks wrong: %+v", st.ReadThrough)
	}
}

// TestReadPercent: the high-water mark as a fraction of the file's line count,
// clamped to 100; 0 when nothing's read.
// fiveLine is a 5-line all-context file for the frontier/percent tests.
func fiveLine() *gitdiff.FileDiff {
	return &gitdiff.FileDiff{Lines: []gitdiff.DiffLine{
		{OldNum: 1, NewNum: 1, Kind: "ctx"}, {OldNum: 2, NewNum: 2, Kind: "ctx"},
		{OldNum: 3, NewNum: 3, Kind: "ctx"}, {OldNum: 4, NewNum: 4, Kind: "ctx"},
		{OldNum: 5, NewNum: 5, Kind: "ctx"},
	}}
}

// TestReadFrontierKey_CodeView: the frontier key is the line at the high-water
// mark, in the exact "L<old>-<new>" form the template stamps.
func TestReadFrontierKey_CodeView(t *testing.T) {
	st := PrereviewState{SelectedFile: "a.go", CurrentDiff: fiveLine(), ReadThrough: map[string]int{"a.go": 3}}
	if got := st.ReadFrontierKey(); got != "L3-3" {
		t.Errorf("frontier key = %q, want L3-3", got)
	}
	if !st.HasReadFrontier() { // 3/5 = 60%
		t.Error("60%% read should offer a resume frontier")
	}
}

// TestHasReadFrontier_Bounds: no resume offered at 0% or 100%.
func TestHasReadFrontier_Bounds(t *testing.T) {
	if (PrereviewState{SelectedFile: "a", CurrentDiff: fiveLine()}).HasReadFrontier() {
		t.Error("0%% read → no resume affordance")
	}
	full := PrereviewState{SelectedFile: "a", CurrentDiff: fiveLine(), ReadThrough: map[string]int{"a": 5}}
	if full.HasReadFrontier() {
		t.Error("100%% read → no resume affordance")
	}
}

// TestJumpToReadFrontier sets the one-shot scroll target to the frontier.
func TestJumpToReadFrontier(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{SelectedFile: "a.go", CurrentDiff: fiveLine(), ReadThrough: map[string]int{"a.go": 3}}
	st, _ = c.JumpToReadFrontier(st, nil)
	if st.ScrollToReadKey != "L3-3" {
		t.Errorf("resume should set scroll target to frontier L3-3, got %q", st.ScrollToReadKey)
	}
}

func TestReadPercent(t *testing.T) {
	fiveLineFile := &gitdiff.FileDiff{Lines: []gitdiff.DiffLine{
		{OldNum: 1, NewNum: 1, Kind: "ctx"},
		{OldNum: 2, NewNum: 2, Kind: "ctx"},
		{OldNum: 3, NewNum: 3, Kind: "ctx"},
		{OldNum: 4, NewNum: 4, Kind: "ctx"},
		{OldNum: 5, NewNum: 5, Kind: "ctx"},
	}}
	cases := []struct {
		name string
		mark int
		want int
	}{
		{"three of five", 3, 60},
		{"all five", 5, 100},
		{"beyond end clamps", 9, 100},
		{"none read", 0, 0},
	}
	for _, tc := range cases {
		st := PrereviewState{SelectedFile: "a.go", CurrentDiff: fiveLineFile}
		if tc.mark > 0 {
			st.ReadThrough = map[string]int{"a.go": tc.mark}
		}
		if got := st.ReadPercent(); got != tc.want {
			t.Errorf("%s: ReadPercent()=%d, want %d", tc.name, got, tc.want)
		}
	}

	// No diff loaded → 0 (no division by zero).
	if got := (PrereviewState{SelectedFile: "a.go", ReadThrough: map[string]int{"a.go": 3}}).ReadPercent(); got != 0 {
		t.Errorf("no diff → 0, got %d", got)
	}
}
