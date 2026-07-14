package review

import (
	"context"
	"testing"

	"github.com/livetemplate/livetemplate"
)

func toggleCtx(key string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), "toggleRow", map[string]interface{}{"row": key})
}

// #174: the count badge is a real show/hide toggle. Clicking it flips the row away from its
// DEFAULT visibility — which is what the yellow (open) badge was missing entirely: it toggled
// a class that no CSS rule matched, so nothing happened.
//
// The state is a set of the rows the reviewer explicitly flipped; the CSS applies the
// inversion. Un-toggling DELETES the key rather than storing false, so the persisted map
// stays the size of what was actually flipped.
func TestToggleRow_FlipsAndUnflipsARow(t *testing.T) {
	c := &PrereviewController{}
	state := PrereviewState{}

	// Nothing is flipped to start: every row renders at its #165 default.
	if len(state.ToggledRows) != 0 {
		t.Fatalf("precondition: want no toggled rows, got %v", state.ToggledRows)
	}

	// Click the badge on line 4 (new side) — the row flips.
	state, err := c.ToggleRow(state, toggleCtx("4-new"))
	if err != nil {
		t.Fatalf("ToggleRow: %v", err)
	}
	if _, on := state.ToggledRows["4-new"]; !on {
		t.Error("clicking the badge must flip the row — this is the whole bug: the yellow " +
			"badge used to toggle a class nothing matched, so it did nothing at all")
	}

	// Click it again — it flips back, and the key is GONE (not stored as false).
	state, err = c.ToggleRow(state, toggleCtx("4-new"))
	if err != nil {
		t.Fatalf("ToggleRow (second click): %v", err)
	}
	if _, on := state.ToggledRows["4-new"]; on {
		t.Error("a second click must un-flip the row")
	}
	if _, present := state.ToggledRows["4-new"]; present {
		t.Errorf("un-toggling should DELETE the key — the persisted map "+
			"should only ever hold rows the reviewer actually flipped; got %v", state.ToggledRows)
	}
}

// Rows are independent, and the md-view's block key shares the same set (it is namespaced
// "MB-<start>-<end>", so it can never collide with the diff's "<line>-<side>").
func TestToggleRow_RowsAndBlocksAreIndependent(t *testing.T) {
	c := &PrereviewController{}
	state := PrereviewState{}

	for _, key := range []string{"4-new", "9-old", "MB-3-5"} {
		var err error
		if state, err = c.ToggleRow(state, toggleCtx(key)); err != nil {
			t.Fatalf("ToggleRow(%s): %v", key, err)
		}
	}
	for _, key := range []string{"4-new", "9-old", "MB-3-5"} {
		if _, on := state.ToggledRows[key]; !on {
			t.Errorf("row %q should be flipped", key)
		}
	}

	// Un-flipping one must not disturb its neighbours.
	state, err := c.ToggleRow(state, toggleCtx("9-old"))
	if err != nil {
		t.Fatal(err)
	}
	if _, on := state.ToggledRows["9-old"]; on {
		t.Error("9-old should be un-flipped")
	}
	_, keep1 := state.ToggledRows["4-new"]
	_, keep2 := state.ToggledRows["MB-3-5"]
	if !keep1 || !keep2 {
		t.Errorf("un-flipping one row disturbed the others: %v", state.ToggledRows)
	}
}

// A missing key is a programming error in the template, not something to swallow silently:
// a badge that quietly did nothing is exactly the bug being fixed.
func TestToggleRow_MissingKeyErrors(t *testing.T) {
	c := &PrereviewController{}
	if _, err := c.ToggleRow(PrereviewState{}, toggleCtx("")); err == nil {
		t.Error("a badge click with no row key must error, not no-op")
	}
}

// A toggle EXPIRES when the row's default flips underneath it.
//
// The regression this pins: peek a DONE card (row collapsed by default, so the toggle means
// "show it"), then Undo the verdict. The suggestion goes back to undecided, so the row's
// default becomes "open" — and the stale toggle, which only ever meant "flip", would now
// mean HIDE, vanishing the very card the reviewer is working on. It made the accepted →
// undo flow unusable (TestE2E_SuggestionDecisions / TestE2E_RevertRestoresFile hung on a
// button that could never become visible).
func TestRowToggled_ExpiresWhenTheRowsDefaultChanges(t *testing.T) {
	c := &PrereviewController{}

	// A row whose only annotation is RESOLVED: collapsed by default.
	state := PrereviewState{
		SelectedFile: "a.go",
		Comments: []Comment{{
			ID: "c1", File: "a.go", FromLine: 4, ToLine: 4, Side: "new",
			Body: "done", Kind: commentKindLine, Resolved: true,
		}},
	}
	if got := state.rowState("4-new"); got != rowStateCollapsed {
		t.Fatalf("precondition: a resolved-only row should default to collapsed, got %q", got)
	}

	// Peek it — the toggle records the default it is flipping away from.
	state, err := c.ToggleRow(state, toggleCtx("4-new"))
	if err != nil {
		t.Fatal(err)
	}
	if state.ToggledRows["4-new"] != rowStateCollapsed {
		t.Fatalf("the toggle must record the default it flipped away from, got %q",
			state.ToggledRows["4-new"])
	}
	if !state.RowToggled()["4-new"] {
		t.Error("while the row is still collapsed-by-default, the peek applies")
	}

	// Now REOPEN the comment: the row's default becomes "open" (the card shows inline).
	state.Comments[0].Resolved = false

	if state.RowToggled()["4-new"] {
		t.Error("the toggle must EXPIRE once the row's default flips — otherwise the reviewer's " +
			"'show me this' silently becomes 'hide this', and the card they just reopened " +
			"disappears from under them")
	}
	// The raw entry may linger (it is cleaned up on the next toggle); what matters is that
	// the RENDER no longer applies it.
}
