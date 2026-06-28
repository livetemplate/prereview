package review

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/livetemplate/livetemplate"
)

func kbCtx(action string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), action, nil)
}

// TestKeyBindings_ActionsResolveToHandlers pins the single-source contract:
// every keymap Action with a non-empty handler name must resolve to an
// exported controller method, using the same capitalize-first routing
// livetemplate applies to an action string. Catches a typo'd Action or a
// renamed handler before it ships as a dead key.
func TestKeyBindings_ActionsResolveToHandlers(t *testing.T) {
	ct := reflect.TypeOf(&PrereviewController{})
	for _, b := range keyBindings {
		if b.Action == "" {
			continue // help-only row (Esc / Enter)
		}
		method := strings.ToUpper(b.Action[:1]) + b.Action[1:]
		if _, ok := ct.MethodByName(method); !ok {
			t.Errorf("keymap action %q has no controller method %q", b.Action, method)
		}
	}
}

// TestKeyBindings_NonEmpty guards against the slice being accidentally emptied
// (which would silently drop all shortcuts and the help overlay).
func TestKeyBindings_NonEmpty(t *testing.T) {
	if len(PrereviewState{}.KeyBindings()) == 0 {
		t.Fatal("keymap is empty")
	}
	for _, b := range keyBindings {
		if len(b.Keys) == 0 {
			t.Errorf("binding %q has no keys", b.Label)
		}
		if b.Display == "" || b.Label == "" {
			t.Errorf("binding %v missing Display or Label", b.Keys)
		}
	}
}

// TestToggleKeyboardHelp_Flips pins the help overlay as a pure on/off flip,
// and that it closes the more-menu so the two overlays don't stack.
func TestToggleKeyboardHelp_Flips(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{MoreMenuOpen: true}
	st, _ = c.ToggleKeyboardHelp(st, kbCtx("toggleKeyboardHelp"))
	if !st.KeyHelpOpen {
		t.Error("expected help open after first toggle")
	}
	if st.MoreMenuOpen {
		t.Error("opening help should close the more-menu")
	}
	st, _ = c.ToggleKeyboardHelp(st, kbCtx("toggleKeyboardHelp"))
	if st.KeyHelpOpen {
		t.Error("expected help closed after second toggle")
	}
}

// TestClearSelection_ClosesKeyboardHelp pins that Esc (clearSelection) also
// dismisses the help overlay — the universal close key.
func TestClearSelection_ClosesKeyboardHelp(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{KeyHelpOpen: true}
	st, _ = c.ClearSelection(st, kbCtx("clearSelection"))
	if st.KeyHelpOpen {
		t.Error("clearSelection should close the keyboard-help overlay")
	}
}

// stepCommentState builds a single-file state with n open comments so
// stepComment never needs to load a diff (target.File == SelectedFile).
func stepCommentState(n int) PrereviewState {
	cs := make([]Comment, n)
	for i := range cs {
		cs[i] = Comment{ID: string(rune('a' + i)), File: "a.md"}
	}
	return PrereviewState{SelectedFile: "a.md", Comments: cs}
}

// TestStepComment_Wraparound pins next/prev stepping and wrap, starting from a
// known focused comment.
func TestStepComment_Wraparound(t *testing.T) {
	c := &PrereviewController{}
	st := stepCommentState(3) // ids: a, b, c

	st.ScrollToCommentID = "a"
	st, _ = c.NextComment(st, kbCtx("nextComment"))
	if st.ScrollToCommentID != "b" {
		t.Errorf("next from a: got %q want b", st.ScrollToCommentID)
	}
	st, _ = c.NextComment(st, kbCtx("nextComment"))
	st, _ = c.NextComment(st, kbCtx("nextComment")) // c -> wrap -> a
	if st.ScrollToCommentID != "a" {
		t.Errorf("next wrap: got %q want a", st.ScrollToCommentID)
	}
	st, _ = c.PrevComment(st, kbCtx("prevComment")) // a -> wrap -> c
	if st.ScrollToCommentID != "c" {
		t.Errorf("prev wrap: got %q want c", st.ScrollToCommentID)
	}
	// Stepping a comment leaves the all-comments overview so the target is
	// visible in the diff.
	if st.ShowAllComments {
		t.Error("stepComment should close the all-comments overview")
	}
}

// TestStepComment_StartsFromNothing pins the cursor-less case: Next lands on
// the first comment, Prev on the last.
func TestStepComment_StartsFromNothing(t *testing.T) {
	c := &PrereviewController{}

	st := stepCommentState(3)
	st, _ = c.NextComment(st, kbCtx("nextComment"))
	if st.ScrollToCommentID != "a" {
		t.Errorf("next from nothing: got %q want a", st.ScrollToCommentID)
	}

	st = stepCommentState(3)
	st, _ = c.PrevComment(st, kbCtx("prevComment"))
	if st.ScrollToCommentID != "c" {
		t.Errorf("prev from nothing: got %q want c", st.ScrollToCommentID)
	}
}

// TestStepComment_EmptyAndResolved pins the no-op cases: no comments, and only
// resolved comments (which VisibleComments filters out by default).
func TestStepComment_EmptyAndResolved(t *testing.T) {
	c := &PrereviewController{}

	st := PrereviewState{SelectedFile: "a.md"}
	st, err := c.NextComment(st, kbCtx("nextComment"))
	if err != nil || st.ScrollToCommentID != "" {
		t.Errorf("empty set should be a no-op, got id=%q err=%v", st.ScrollToCommentID, err)
	}

	st = PrereviewState{
		SelectedFile: "a.md",
		Comments:     []Comment{{ID: "a", File: "a.md", Resolved: true}},
	}
	st, _ = c.NextComment(st, kbCtx("nextComment"))
	if st.ScrollToCommentID != "" {
		t.Errorf("all-resolved (hidden) should be a no-op, got id=%q", st.ScrollToCommentID)
	}
}

// TestToggleShowResolved_FlashWhenNone pins that pressing "r" with no resolved
// comments shows a flash instead of silently toggling, and that with a resolved
// comment present it toggles normally and clears any flash.
func TestToggleShowResolved_FlashWhenNone(t *testing.T) {
	c := &PrereviewController{}

	// No resolved comments → flash, no toggle.
	st := PrereviewState{Comments: []Comment{{ID: "a", Resolved: false}}}
	st, _ = c.ToggleShowResolved(st, kbCtx("toggleShowResolved"))
	if st.ShowResolved {
		t.Error("should not toggle ShowResolved when there are no resolved comments")
	}
	if st.Flash == "" {
		t.Error("expected a flash message when there are no resolved comments")
	}

	// A resolved comment present → toggles and clears the flash.
	st = PrereviewState{Flash: "stale", Comments: []Comment{{ID: "a", Resolved: true}}}
	st, _ = c.ToggleShowResolved(st, kbCtx("toggleShowResolved"))
	if !st.ShowResolved {
		t.Error("expected ShowResolved to toggle on when a resolved comment exists")
	}
	if st.Flash != "" {
		t.Errorf("flash should clear on a real toggle, got %q", st.Flash)
	}
}

// TestClearFlash_Clears pins the toast dismiss.
func TestClearFlash_Clears(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{Flash: "No resolved comments"}
	st, _ = c.ClearFlash(st, kbCtx("clearFlash"))
	if st.Flash != "" {
		t.Errorf("ClearFlash should empty Flash, got %q", st.Flash)
	}
}
