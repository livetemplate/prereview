package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/internal/library"
)

func TestLoadShortcuts_BuiltinsAndOverlay(t *testing.T) {
	got := LoadShortcuts("")
	var slugs []string
	for _, p := range got {
		slugs = append(slugs, p.Slug)
	}
	if len(got) != 8 {
		t.Fatalf("want 8 built-in shortcuts, got %v", slugs)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "drop.md"), []byte("# Cut it\n\nmy own drop"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range LoadShortcuts(dir) {
		if p.Slug == "drop" && (p.Title != "Cut it" || p.Body != "my own drop") {
			t.Errorf("an overlay file should replace the built-in by slug, got %+v", p)
		}
	}
}

// Most used first, hidden ones gone; ties keep the loader's label order.
func TestApplyShortcuts_OrderAndHidden(t *testing.T) {
	lib := t.TempDir()
	for range 2 {
		if err := library.RecordShortcutUse(lib, "shorter"); err != nil {
			t.Fatal(err)
		}
	}
	if err := library.RecordShortcutUse(lib, "evidence"); err != nil {
		t.Fatal(err)
	}
	if err := library.SetShortcutHidden(lib, "lead", true); err != nil {
		t.Fatal(err)
	}
	c := &PrereviewController{LibraryDir: lib}
	var st PrereviewState
	c.applyShortcuts(&st)
	if len(st.Shortcuts) != 7 {
		t.Fatalf("one hidden of 8: got %d", len(st.Shortcuts))
	}
	if st.Shortcuts[0].Slug != "shorter" || st.Shortcuts[1].Slug != "evidence" || st.Shortcuts[0].Uses != 2 {
		t.Errorf("order = %s, %s (uses %d); want shorter, evidence first", st.Shortcuts[0].Slug, st.Shortcuts[1].Slug, st.Shortcuts[0].Uses)
	}
	for _, s := range st.Shortcuts {
		if s.Slug == "lead" {
			t.Error("a hidden shortcut is still a chip")
		}
	}
}

func useCtx(slug, body string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), "useShortcut", map[string]interface{}{"value": slug, "body": body})
}

// A tap keeps the line selection (a PickPrompt copy would turn it into a file
// comment), adds after what's typed, remounts the composer and counts the use.
func TestUseShortcut(t *testing.T) {
	lib := t.TempDir()
	c := &PrereviewController{LibraryDir: lib}
	st := PrereviewState{SelectedFile: "a.md", SelectionAnchor: 4, SelectionEnd: 4, SelectionSide: "new"}
	c.applyShortcuts(&st)

	st, _ = c.UseShortcut(st, useCtx("shorter", ""))
	if !strings.HasPrefix(st.DraftBody, "Make this shorter") || st.ComposerNonce != 1 {
		t.Errorf("empty composer: draft %q nonce %d", st.DraftBody, st.ComposerNonce)
	}
	st, _ = c.UseShortcut(st, useCtx("evidence", "typed by me\n"))
	if !strings.HasPrefix(st.DraftBody, "typed by me\n\nThis needs evidence") || st.ComposerNonce != 2 {
		t.Errorf("typed text must stay first: %q", st.DraftBody)
	}
	if st.SelectionAnchor != 4 || st.CommentMode != "" {
		t.Errorf("the line selection must survive: anchor %d mode %q", st.SelectionAnchor, st.CommentMode)
	}
	if n := library.ShortcutUseCounts(lib)["evidence"]; n != 1 {
		t.Errorf("uses recorded = %d", n)
	}
	before := st
	st, _ = c.UseShortcut(st, useCtx("gone", "x"))
	if st.DraftBody != before.DraftBody || st.ComposerNonce != before.ComposerNonce {
		t.Error("an unknown slug must change nothing")
	}
}

// The textarea a chip replaced can still send its debounced autosave; it carries
// the old nonce and must not overwrite the shortcut's text.
func TestSaveDraft_IgnoresAutosaveFromBeforeAShortcut(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{SelectionAnchor: 4, SelectionEnd: 4, DraftBody: "with shortcut", ComposerNonce: 1}
	st, _ = c.SaveDraft(st, livetemplate.NewContext(context.TODO(), "saveDraft", map[string]interface{}{"body": "old", "nonce": 0}))
	if st.DraftBody != "with shortcut" {
		t.Errorf("stale autosave applied: %q", st.DraftBody)
	}
	st, _ = c.SaveDraft(st, livetemplate.NewContext(context.TODO(), "saveDraft", map[string]interface{}{"body": "new", "nonce": 1}))
	if st.DraftBody != "new" {
		t.Errorf("current autosave dropped: %q", st.DraftBody)
	}
}

// An accepted learning that adds a shortcut is a chip; a proposed one isn't yet.
// Hide takes a chip out of the composer but keeps it on the Library tab.
func TestShortcuts_LearnedAndHidden(t *testing.T) {
	lib := t.TempDir()
	for _, l := range []library.Learning{
		{ID: "a1", Text: "t", Status: library.StatusAccepted, ShortcutLabel: "Why?", ShortcutBody: "Why is this here?"},
		{ID: "p1", Text: "t", Status: library.StatusProposed, ShortcutLabel: "Not yet", ShortcutBody: "not yet"},
	} {
		if err := library.Append(filepath.Join(lib, library.LearningsFile), l); err != nil {
			t.Fatal(err)
		}
	}
	c := &PrereviewController{LibraryDir: lib}
	st := PrereviewState{ShowLibrary: true, LibraryTab: libTabShortcuts}
	c.applyShortcuts(&st)
	c.applyShortcutsTab(&st)
	find := func(list []Shortcut, slug string) *Shortcut {
		for i := range list {
			if list[i].Slug == slug {
				return &list[i]
			}
		}
		return nil
	}
	if s := find(st.Shortcuts, "learned-a1"); s == nil || s.Source != shortcutLearned || s.Label != "Why?" {
		t.Fatalf("accepted shortcut learning: %+v", s)
	}
	if find(st.Shortcuts, "learned-p1") != nil {
		t.Error("a proposed learning must not add a chip before it's accepted")
	}
	st, _ = c.HideShortcut(st, livetemplate.NewContext(context.TODO(), "hideShortcut", map[string]interface{}{"value": "learned-a1"}))
	if find(st.Shortcuts, "learned-a1") != nil {
		t.Error("a hidden shortcut is still a chip")
	}
	if s := find(st.ShortcutsTab.Rows, "learned-a1"); s == nil || !s.Hidden {
		t.Errorf("the tab must still list it, marked hidden: %+v", s)
	}
	st, _ = c.ShowShortcut(st, livetemplate.NewContext(context.TODO(), "showShortcut", map[string]interface{}{"value": "learned-a1"}))
	if find(st.Shortcuts, "learned-a1") == nil {
		t.Error("Show must bring the chip back")
	}
}

func TestUnwrapParagraphs(t *testing.T) {
	got := unwrapParagraphs("Drop this. Then keep\nthe narrative whole.\n\nSecond   para\nhere.\n")
	if want := "Drop this. Then keep the narrative whole.\n\nSecond para here."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
