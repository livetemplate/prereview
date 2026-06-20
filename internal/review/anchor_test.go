package review

import (
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

// fd builds a FileDiff whose new side is exactly the given lines
// (line i is number i+1) — enough for anchor capture/relocate, which
// only read NewNum>0 + Content.
func fd(lines ...string) *gitdiff.FileDiff {
	d := &gitdiff.FileDiff{}
	for i, s := range lines {
		d.Lines = append(d.Lines, gitdiff.DiffLine{
			OldNum: i + 1, NewNum: i + 1, Kind: "ctx", Content: s,
		})
	}
	return d
}

// mk captures an anchor from orig at [from,to] and returns a comment
// ready to relocate against a modified file.
func mk(orig *gitdiff.FileDiff, from, to int) Comment {
	return Comment{
		ID: "c1", File: "d.md", FromLine: from, ToLine: to, Side: "new",
		Anchor: captureAnchor(orig, from, to, "new"), AnchorStatus: anchorOK,
	}
}

func TestRelocate_Table(t *testing.T) {
	cases := []struct {
		name             string
		orig             []string
		from, to         int
		modified         []string
		wantFrom, wantTo int
		wantStatus       string
		wantChanged      bool
	}{
		{
			name: "insert above shifts down (moved)",
			orig: []string{"A", "B", "TARGET sentence", "C"}, from: 3, to: 3,
			modified: []string{"X", "Y", "A", "B", "TARGET sentence", "C"},
			wantFrom: 5, wantTo: 5, wantStatus: anchorMoved, wantChanged: true,
		},
		{
			name: "delete above shifts up (moved)",
			orig: []string{"A", "B", "TARGET sentence", "C"}, from: 3, to: 3,
			modified: []string{"B", "TARGET sentence", "C"},
			wantFrom: 2, wantTo: 2, wantStatus: anchorMoved, wantChanged: true,
		},
		{
			name: "unchanged in place (ok, no-op)",
			orig: []string{"A", "B", "TARGET sentence", "C"}, from: 3, to: 3,
			modified: []string{"A", "B", "TARGET sentence", "C"},
			wantFrom: 3, wantTo: 3, wantStatus: anchorOK, wantChanged: false,
		},
		{
			name: "anchored line edited (outdated, ints untouched)",
			orig: []string{"A", "B", "TARGET sentence", "C"}, from: 3, to: 3,
			modified: []string{"A", "B", "TARGET sentence EDITED", "C"},
			wantFrom: 3, wantTo: 3, wantStatus: anchorOutdated, wantChanged: true,
		},
		{
			// Original line no longer holds the text (fast path fails);
			// two occurrences exist; before/after context picks the right.
			name: "duplicate text, context disambiguates (moved to right one)",
			orig: []string{"unique pre", "TARGET dup", "unique post"}, from: 2, to: 2,
			modified: []string{"TARGET dup", "other B", "filler",
				"unique pre", "TARGET dup", "unique post"},
			wantFrom: 5, wantTo: 5, wantStatus: anchorMoved, wantChanged: true,
		},
		{
			// Original line no longer matches; duplicates with no
			// distinguishing context → refuse to guess (outdated).
			name: "duplicate text, no separating context (outdated)",
			orig: []string{"TARGET dup"}, from: 1, to: 1,
			modified: []string{"x", "TARGET dup", "y", "TARGET dup", "z"},
			wantFrom: 1, wantTo: 1, wantStatus: anchorOutdated, wantChanged: true,
		},
		{
			name: "anchored content gone (outdated)",
			orig: []string{"A", "TARGET sentence", "B"}, from: 2, to: 2,
			modified: []string{"A", "B"},
			wantFrom: 2, wantTo: 2, wantStatus: anchorOutdated, wantChanged: true,
		},
		{
			name: "multi-line span moves as a block",
			orig: []string{"A", "L1", "L2", "L3", "B"}, from: 2, to: 4,
			modified: []string{"X", "X", "A", "L1", "L2", "L3", "B"},
			wantFrom: 4, wantTo: 6, wantStatus: anchorMoved, wantChanged: true,
		},
		{
			name: "start of file, no before-context, still relocates",
			orig: []string{"ONLY sentence", "B"}, from: 1, to: 1,
			modified: []string{"A", "ONLY sentence", "B"},
			wantFrom: 2, wantTo: 2, wantStatus: anchorMoved, wantChanged: true,
		},
		{
			name: "whitespace-only differences still match in place (ok)",
			orig: []string{"the   quick  fox"}, from: 1, to: 1,
			modified: []string{"  the quick fox  "},
			wantFrom: 1, wantTo: 1, wantStatus: anchorOK, wantChanged: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mk(fd(tc.orig...), tc.from, tc.to)
			changed := relocate(fd(tc.modified...), &c)
			if c.FromLine != tc.wantFrom || c.ToLine != tc.wantTo {
				t.Errorf("range = %d-%d, want %d-%d", c.FromLine, c.ToLine, tc.wantFrom, tc.wantTo)
			}
			if c.AnchorStatus != tc.wantStatus {
				t.Errorf("status = %q, want %q", c.AnchorStatus, tc.wantStatus)
			}
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}
		})
	}
}

func TestRelocate_MovedIsSticky(t *testing.T) {
	c := mk(fd("A", "TARGET sentence", "B"), 2, 2)
	// First drift (insert above) → moved, shifted to line 4.
	if !relocate(fd("X", "X", "A", "TARGET sentence", "B"), &c) {
		t.Fatal("expected a change on first drift")
	}
	if c.AnchorStatus != anchorMoved || c.FromLine != 4 {
		t.Fatalf("want moved@4, got %q@%d", c.AnchorStatus, c.FromLine)
	}
	// Stable reload, content unchanged at the new position: must STAY
	// moved (not silently downgraded to ok) and report no change.
	if relocate(fd("X", "X", "A", "TARGET sentence", "B"), &c) {
		t.Error("sticky moved should report no change on a stable reload")
	}
	if c.AnchorStatus != anchorMoved {
		t.Errorf("moved must be sticky across reloads, got %q", c.AnchorStatus)
	}
}

func TestRelocate_ResolvedIsFrozen(t *testing.T) {
	c := mk(fd("A", "TARGET", "B"), 2, 2)
	c.Resolved = true
	// Content clearly moved, but a resolved comment must not be touched.
	if relocate(fd("X", "X", "A", "TARGET", "B"), &c) {
		t.Fatal("relocate reported a change for a resolved comment")
	}
	if c.FromLine != 2 || c.ToLine != 2 || c.AnchorStatus != anchorOK {
		t.Errorf("resolved comment mutated: %d-%d %q", c.FromLine, c.ToLine, c.AnchorStatus)
	}
}

func TestRelocate_LegacyEmptyAnchorSkipped(t *testing.T) {
	// Pre-migration comment: no anchor, empty status — must behave
	// exactly as before (untouched, never flagged outdated).
	c := Comment{ID: "old", File: "d.md", FromLine: 9, ToLine: 9, Side: "new"}
	if relocate(fd("totally", "different", "file"), &c) {
		t.Fatal("legacy comment without an anchor should not change")
	}
	if c.FromLine != 9 || c.AnchorStatus != "" {
		t.Errorf("legacy comment mutated: %d %q", c.FromLine, c.AnchorStatus)
	}
}

func TestCaptureAnchor_ContextWindowsAndBounds(t *testing.T) {
	d := fd("l1", "l2", "l3", "l4", "l5", "l6", "l7")
	a := captureAnchor(d, 4, 4, "new")
	if a.Text != "l4" {
		t.Errorf("Text = %q, want l4", a.Text)
	}
	if len(a.Before) != 3 || a.Before[0] != "l1" || a.Before[2] != "l3" {
		t.Errorf("Before = %v, want [l1 l2 l3]", a.Before)
	}
	if len(a.After) != 3 || a.After[0] != "l5" || a.After[2] != "l7" {
		t.Errorf("After = %v, want [l5 l6 l7]", a.After)
	}
	// At EOF the after-window truncates without panic.
	tail := captureAnchor(d, 7, 7, "new")
	if tail.Text != "l7" || len(tail.After) != 0 {
		t.Errorf("EOF anchor = %+v, want Text=l7 After=[]", tail)
	}
	// Out-of-range / empty file → empty anchor (caller skips it).
	if !captureAnchor(d, 99, 99, "new").Empty() {
		t.Error("out-of-range capture should be empty")
	}
	if !captureAnchor(fd(), 1, 1, "new").Empty() {
		t.Error("empty file capture should be empty")
	}
}
