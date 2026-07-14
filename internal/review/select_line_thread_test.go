package review

import (
	"context"
	"testing"

	"github.com/livetemplate/livetemplate"
)

func lineCtx(line int, side string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), "selectLine",
		map[string]interface{}{"line": line, "side": side})
}

func stateWithComment(cm Comment) PrereviewState {
	return PrereviewState{SelectedFile: "a.md", Comments: []Comment{cm}}
}

func lineComment(id string, line int, side string) Comment {
	return Comment{ID: id, File: "a.md", FromLine: line, ToLine: line, Side: side,
		Body: "existing thread", Kind: commentKindLine}
}

// A line is ONE conversation. Clicking a line that already carries a comment must OPEN that
// thread — reveal the card and arm its reply box — instead of starting a second comment on
// top of it. Before this, the click always dropped a fresh new-comment composer on the row,
// so the natural way to answer a comment ("click the line it's on") created a rival comment.
func TestSelectLine_OpensExistingThread(t *testing.T) {
	c := &PrereviewController{}
	state := stateWithComment(lineComment("c1", 3, "new"))

	state, err := c.SelectLine(state, lineCtx(3, "new"))
	if err != nil {
		t.Fatalf("SelectLine: %v", err)
	}

	if state.ReplyingID != "c1" {
		t.Errorf("clicking a commented line must open that thread's reply box "+
			"(ReplyingID=%q, want %q)", state.ReplyingID, "c1")
	}
	// The selection must be CLEARED, or the new-comment composer renders on top of the thread
	// the reviewer just asked to see — two composers, one row.
	if state.SelectionAnchor != 0 || state.SelectionEnd != 0 || state.SelectionSide != "" {
		t.Errorf("opening a thread must leave no selection behind, else the new-comment "+
			"composer renders over it; got anchor=%d end=%d side=%q",
			state.SelectionAnchor, state.SelectionEnd, state.SelectionSide)
	}
}

// A kind=text (phrase) comment is a thread on its row too, so clicking the line opens it —
// the same rule, deliberately: the row renders one card and the click reaches it. (Its card
// does render a reply form, so this is a live affordance, not a dead click —
// TestE2E_LineClickOpensThread proves that end-to-end.)
func TestSelectLine_OpensATextCommentsThread(t *testing.T) {
	c := &PrereviewController{}
	cm := lineComment("c1", 3, "new")
	cm.Kind = commentKindText
	cm.FromCol, cm.ToCol = 9, 14
	cm.Anchor.Snippet = "hello"

	state, err := c.SelectLine(stateWithComment(cm), lineCtx(3, "new"))
	if err != nil {
		t.Fatalf("SelectLine: %v", err)
	}
	if state.ReplyingID != "c1" {
		t.Errorf("a phrase comment is still the row's conversation — clicking the line must "+
			"open it (ReplyingID=%q)", state.ReplyingID)
	}
}

// The escape hatch the user asked to keep: a line that already has a comment can STILL take a
// brand-new one — by selecting a phrase on it (kind=text). That path is SelectText, which the
// thread-opening pre-check must not touch.
func TestSelectText_StillComposesNewCommentOnACommentedLine(t *testing.T) {
	c := &PrereviewController{}
	state := stateWithComment(lineComment("c1", 3, "new"))

	// Worst case: the reviewer got here by CLICKING the line, so its thread is already open.
	// Selecting a phrase on it must still compose a new comment — and close that reply box,
	// or the row renders a reply form and a composer at once.
	state, err := c.SelectLine(state, lineCtx(3, "new"))
	if err != nil {
		t.Fatal(err)
	}
	if state.ReplyingID != "c1" {
		t.Fatalf("precondition: the line click should have opened the thread")
	}

	tctx := livetemplate.NewContext(context.TODO(), "selectText", map[string]interface{}{
		"fromLine": 3, "toLine": 3, "side": "new", "fromCol": 4, "toCol": 9, "text": "quick",
	})
	state, err = c.SelectText(state, tctx)
	if err != nil {
		t.Fatalf("SelectText: %v", err)
	}

	if state.ReplyingID != "" {
		t.Errorf("selecting text must compose a NEW comment and close the line's reply box, "+
			"not render both on one row (ReplyingID=%q)", state.ReplyingID)
	}
	if state.CommentMode != commentKindText {
		t.Errorf("the composer must be in text mode; got %q", state.CommentMode)
	}
	if state.SelectionAnchor != 3 || state.SelectionEnd != 3 {
		t.Errorf("text-select on a commented line must still arm the composer over the "+
			"selection; got anchor=%d end=%d", state.SelectionAnchor, state.SelectionEnd)
	}
	if state.SelectionFromCol != 4 || state.SelectionToCol != 9 {
		t.Errorf("the selected character range must survive; got cols %d-%d",
			state.SelectionFromCol, state.SelectionToCol)
	}
}

// Clicking a line whose comment the reviewer had COLLAPSED (#174's badge toggle) must bring
// the card back — otherwise the click opens a reply box on a hidden card and looks like it
// did nothing at all.
func TestSelectLine_UncollapsesTheRowItOpens(t *testing.T) {
	c := &PrereviewController{}
	state := stateWithComment(lineComment("c1", 3, "new"))
	state.ToggledRows = map[string]bool{"3-new": true, "9-new": true}

	state, err := c.SelectLine(state, lineCtx(3, "new"))
	if err != nil {
		t.Fatalf("SelectLine: %v", err)
	}

	if state.ToggledRows["3-new"] {
		t.Error("opening a thread on a collapsed row must un-collapse it — a reply box on a " +
			"hidden card reads as a dead click")
	}
	if !state.ToggledRows["9-new"] {
		t.Error("un-collapsing the clicked row must not disturb other rows")
	}
}

// A line with no (open) comment keeps the ordinary two-click range selection: the composer.
// Resolved and outdated comments do NOT count — a closed thread must not swallow the click
// that was meant to start a fresh one.
func TestSelectLine_FallsThroughToComposer(t *testing.T) {
	resolved := lineComment("c1", 3, "new")
	resolved.Resolved = true
	outdated := lineComment("c2", 4, "new")
	outdated.AnchorStatus = anchorOutdated
	otherFile := lineComment("c3", 5, "new")
	otherFile.File = "b.md"
	otherSide := lineComment("c4", 6, "old")

	cases := []struct {
		name string
		cm   Comment
		line int
	}{
		{"no comment at all", lineComment("c0", 99, "new"), 3},
		{"resolved comment", resolved, 3},
		{"outdated comment", outdated, 4},
		{"comment on another file", otherFile, 5},
		{"comment on the other side of the row", otherSide, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &PrereviewController{}
			state, err := c.SelectLine(stateWithComment(tc.cm), lineCtx(tc.line, "new"))
			if err != nil {
				t.Fatalf("SelectLine: %v", err)
			}
			if state.ReplyingID != "" {
				t.Errorf("no OPEN thread on this row — the click must fall through to the "+
					"new-comment composer, not open %q", state.ReplyingID)
			}
			if state.SelectionAnchor != tc.line || state.SelectionEnd != tc.line {
				t.Errorf("the click must anchor the composer on line %d; got anchor=%d end=%d",
					tc.line, state.SelectionAnchor, state.SelectionEnd)
			}
		})
	}
}

// Mid-range, the reviewer is deliberately extending a selection across lines. One of those
// lines happening to carry a comment must NOT hijack it — you'd lose the range you were
// halfway through drawing.
func TestSelectLine_MidRangeExtensionIsNotHijacked(t *testing.T) {
	c := &PrereviewController{}
	state := stateWithComment(lineComment("c1", 5, "new"))

	// First click on a bare line 3 — anchor placed.
	state, err := c.SelectLine(state, lineCtx(3, "new"))
	if err != nil {
		t.Fatal(err)
	}
	// Second click extends to line 5, which HAS a comment.
	state, err = c.SelectLine(state, lineCtx(5, "new"))
	if err != nil {
		t.Fatal(err)
	}

	if state.ReplyingID != "" {
		t.Errorf("extending a range onto a commented line must not open its thread and drop "+
			"the range; opened %q", state.ReplyingID)
	}
	if state.SelectionAnchor != 3 || state.SelectionEnd != 5 {
		t.Errorf("the range 3-5 must complete; got anchor=%d end=%d",
			state.SelectionAnchor, state.SelectionEnd)
	}
}
