package review

import (
	"embed"
	"log/slog"
	"slices"
	"strings"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/internal/library"
)

// shortcut.go is #204: one-tap comments on a selection. A chip on the line, text or
// block composer pre-fills the comment with an instruction the reviewer keeps giving
// ("Drop this", "Why is this here?"). Built-ins are embedded; the reviewer's own live
// in <library>/shortcuts/*.md (same format as the prompt library); the chips are
// ordered by how often each was tapped.

//go:embed builtin_shortcuts/*.md
var builtinShortcutsFS embed.FS

// Shortcut is one chip: Label on the chip, Body the text it adds to the comment.
type Shortcut struct {
	Slug  string
	Label string
	Body  string
	Uses  int
}

// LoadShortcuts is the built-in shortcuts overlaid with the reviewer's folder,
// by the prompt library's rules (override by slug, skip what doesn't parse).
func LoadShortcuts(userDir string) []Prompt {
	return loadPromptLibrary(builtinShortcutsFS, "builtin_shortcuts", userDir)
}

// applyShortcuts loads the chips for this visit: hidden ones dropped, most used
// first (ties by label). The order is fixed for the visit, so a tap never moves the
// chips under the reviewer's finger; it shows on the next one.
func (c *PrereviewController) applyShortcuts(state *PrereviewState) {
	var uses map[string]int
	hidden := map[string]bool{}
	if c.LibraryDir != "" {
		uses = library.ShortcutUseCounts(c.LibraryDir)
		hidden = library.HiddenShortcuts(c.LibraryDir)
	}
	var out []Shortcut
	for _, p := range LoadShortcuts(c.ShortcutsDir) {
		if !hidden[p.Slug] {
			out = append(out, Shortcut{Slug: p.Slug, Label: p.Title, Body: p.Body, Uses: uses[p.Slug]})
		}
	}
	slices.SortStableFunc(out, func(a, b Shortcut) int { return b.Uses - a.Uses })
	state.Shortcuts = out
}

// ShowShortcuts gates the chips: a line, block or text composer, not a file, area or
// region one, and not while re-anchoring a comment.
func (s PrereviewState) ShowShortcuts() bool {
	return len(s.Shortcuts) > 0 && (s.CommentMode == "" || s.CommentMode == "text") && s.ReanchorCommentID == ""
}

// UseShortcut adds a shortcut's text to the open composer. The chip submits with the
// composer's form, so "body" is what the reviewer has typed so far (never a stale
// autosave); the shortcut goes after it, or replaces it when it's empty. The
// selection is kept (unlike PickPrompt, which turns the composer into a file
// comment). Bumping ComposerNonce remounts the composer: livetemplate never writes
// into the focused textarea it would otherwise patch.
func (c *PrereviewController) UseShortcut(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	slug := ctx.GetString("value")
	i := slices.IndexFunc(state.Shortcuts, func(s Shortcut) bool { return s.Slug == slug })
	if i < 0 {
		return state, nil // a stale chip
	}
	typed := strings.TrimRight(ctx.GetString("body"), " \t\n")
	if typed == "" {
		state.DraftBody = state.Shortcuts[i].Body
	} else {
		state.DraftBody = typed + "\n\n" + state.Shortcuts[i].Body
	}
	state.ComposerNonce++
	if c.LibraryDir != "" {
		if err := library.RecordShortcutUse(c.LibraryDir, slug); err != nil {
			slog.Warn("record shortcut use", "err", err)
		}
	}
	return state, nil
}
