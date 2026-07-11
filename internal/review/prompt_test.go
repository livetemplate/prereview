package review

import (
	"os"
	"path/filepath"
	"testing"
)

func hasSlug(ps []Prompt, slug string) bool {
	for _, p := range ps {
		if p.Slug == slug {
			return true
		}
	}
	return false
}

func TestLoadPrompts_BuiltinsAndOverlay(t *testing.T) {
	// Built-ins only (no user dir): the three shipped prompts are present.
	builtins := LoadPrompts("")
	for _, s := range []string{"grammar", "prose", "code-review"} {
		if !hasSlug(builtins, s) {
			t.Errorf("missing built-in prompt %q; got %v", s, builtins)
		}
	}

	// User overlay: override a built-in slug, add a new one, skip a body-less file
	// and a non-.md file.
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("grammar.md", "# My grammar\n\ncustom grammar body")
	write("custom.md", "# Custom\n\ndo the thing")
	write("blank.md", "# Only a title\n") // no body → skipped
	write("notes.txt", "ignored")         // not .md → ignored

	merged := LoadPrompts(dir)
	for _, p := range merged {
		if p.Slug == "grammar" && (p.Title != "My grammar" || p.Body != "custom grammar body") {
			t.Errorf("user grammar.md should override the built-in; got %+v", p)
		}
	}
	if !hasSlug(merged, "custom") {
		t.Error("user custom.md should be added")
	}
	if hasSlug(merged, "blank") {
		t.Error("a title-only (body-less) prompt should be skipped")
	}
	if hasSlug(merged, "notes") {
		t.Error("a non-.md file should be ignored")
	}
	// Sorted by title for a stable picker.
	for i := 1; i < len(merged); i++ {
		if merged[i-1].Title > merged[i].Title {
			t.Errorf("prompts not sorted by title: %q > %q", merged[i-1].Title, merged[i].Title)
		}
	}
}

func TestParsePromptBytes(t *testing.T) {
	p, ok := parsePromptBytes("grammar.md", []byte("# Fix grammar\n\nproofread it"))
	if !ok || p.Slug != "grammar" || p.Title != "Fix grammar" || p.Body != "proofread it" {
		t.Errorf("heading parse: %+v ok=%v", p, ok)
	}
	// No heading → title falls back to the slug; whole content is the body.
	p2, ok := parsePromptBytes("x.md", []byte("just a body"))
	if !ok || p2.Title != "x" || p2.Body != "just a body" {
		t.Errorf("no-heading parse: %+v ok=%v", p2, ok)
	}
	// Body-less → skipped.
	if _, ok := parsePromptBytes("y.md", []byte("# Title only\n")); ok {
		t.Error("a title-only file should be skipped (ok=false)")
	}
	if _, ok := parsePromptBytes("z.md", []byte("   ")); ok {
		t.Error("a blank file should be skipped (ok=false)")
	}
}
