package review

import (
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/internal/library"
)

// shortcut.go is #204: one-tap comments on a selection, for the instructions a
// reviewer keeps giving ("Drop this", "Why is this here?").

//go:embed builtin_shortcuts/*.md
var builtinShortcutsFS embed.FS

// Shortcut is one chip: Label on the chip, Body the text it adds to the comment.
// Source says where it comes from (built-in, yours: a file in your folder, learned:
// an accepted learning); Hidden ones are listed in the Library but not offered.
type Shortcut struct {
	Slug   string
	Label  string
	Body   string
	Source string
	Uses   int
	Hidden bool
}

// Shortcut sources.
const (
	shortcutBuiltin = "built-in"
	shortcutYours   = "yours"
	shortcutLearned = "learned"
)

// LoadShortcuts is the built-in shortcuts overlaid with the reviewer's folder,
// by the prompt library's rules (override by slug, skip what doesn't parse).
func LoadShortcuts(userDir string) []Prompt {
	return loadPromptLibrary(builtinShortcutsFS, "builtin_shortcuts", userDir)
}

// allShortcuts is every shortcut, most used first (ties by label): the built-ins and
// your folder, then each accepted learning that adds one.
func (c *PrereviewController) allShortcuts() []Shortcut {
	var uses map[string]int
	var hidden map[string]bool
	if c.LibraryDir != "" {
		uses = library.ShortcutUseCounts(c.LibraryDir)
		hidden = library.HiddenShortcuts(c.LibraryDir)
	}
	var out []Shortcut
	add := func(slug, label, body, source string) {
		out = append(out, Shortcut{Slug: slug, Label: label, Body: unwrapParagraphs(body), Source: source, Uses: uses[slug], Hidden: hidden[slug]})
	}
	for _, p := range LoadShortcuts(c.ShortcutsDir) {
		source := shortcutBuiltin
		if c.ShortcutsDir != "" {
			if _, err := os.Stat(filepath.Join(c.ShortcutsDir, p.Slug+".md")); err == nil {
				source = shortcutYours
			}
		}
		add(p.Slug, p.Title, p.Body, source)
	}
	for _, l := range c.learnings() {
		if l.Status == library.StatusAccepted && l.ShortcutBody != "" {
			add("learned-"+l.ID, l.ShortcutLabel, l.ShortcutBody, shortcutLearned)
		}
	}
	slices.SortStableFunc(out, func(a, b Shortcut) int { return b.Uses - a.Uses })
	return out
}

// unwrapParagraphs joins the hard-wrapped lines of a Markdown file into one line per
// paragraph (blank lines still separate paragraphs), so the comment a chip fills in
// doesn't break mid-sentence.
func unwrapParagraphs(s string) string {
	paras := strings.Split(strings.TrimSpace(s), "\n\n")
	for i, p := range paras {
		paras[i] = strings.Join(strings.Fields(p), " ")
	}
	return strings.Join(paras, "\n\n")
}

// applyShortcuts loads the chips for this visit, without the hidden ones. The order
// is fixed for the visit, so a tap never moves the chips under the reviewer's
// finger; it shows on the next one.
func (c *PrereviewController) applyShortcuts(state *PrereviewState) {
	state.Shortcuts = slices.DeleteFunc(c.allShortcuts(), func(s Shortcut) bool { return s.Hidden })
}

// ShortcutsTab is the Library's Shortcuts tab: every shortcut, hidden ones too.
type ShortcutsTab struct {
	Rows []Shortcut
}

// applyShortcutsTab builds the tab while it's open (nil otherwise, so a closed
// Library carries none of it).
func (c *PrereviewController) applyShortcutsTab(state *PrereviewState) {
	state.ShortcutsTab = nil
	if state.ShowLibrary && state.LibraryOnShortcuts() {
		state.ShortcutsTab = &ShortcutsTab{Rows: c.allShortcuts()}
	}
}

// HideShortcut / ShowShortcut take a chip out of the comment box or put it back
// (the button's value is its slug). The chips refresh at once; nothing is deleted.
func (c *PrereviewController) HideShortcut(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.setShortcutHidden(state, ctx.GetString("value"), true)
}

func (c *PrereviewController) ShowShortcut(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.setShortcutHidden(state, ctx.GetString("value"), false)
}

func (c *PrereviewController) setShortcutHidden(state PrereviewState, slug string, hidden bool) (PrereviewState, error) {
	if c.LibraryDir == "" || slug == "" {
		return state, nil
	}
	if err := library.SetShortcutHidden(c.LibraryDir, slug, hidden); err != nil {
		return state, fmt.Errorf("shortcut: %w", err)
	}
	c.applyShortcuts(&state)
	c.applyShortcutsTab(&state)
	return state, nil
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
