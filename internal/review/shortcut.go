package review

import (
	"embed"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/internal/library"
)

// shortcut.go is #204: one-tap comments on a selection, for the instructions a
// reviewer keeps giving ("Drop this", "Why is this here?"). Since #275 the
// whole-file prompts are shortcuts too, with a "file" scope: chips in the file
// comment box.

//go:embed builtin_shortcuts/*.md
var builtinShortcutsFS embed.FS

// Shortcut is one chip: Label on the chip, Body the text it adds to the comment.
// Source says where it comes from (built-in, yours: a file in your folder, learned:
// an accepted learning); Hidden ones are listed in the Library but not offered.
// Scope is the comment box it shows in: ScopeLine or ScopeFile.
type Shortcut struct {
	Slug   string
	Label  string
	Body   string
	Source string
	Uses   int
	Hidden bool
	Scope  string
}

const (
	shortcutBuiltin = "built-in"
	shortcutYours   = "yours"
	shortcutLearned = "learned"

	// ScopeLine chips show on a line, text or block comment; ScopeFile chips on
	// the whole-file comment (the prompts, #275).
	ScopeLine = "line"
	ScopeFile = "file"

	// fileSlugPrefix keeps whole-file slugs apart from line ones: a prompt and a
	// shortcut may share a file name, and line use counts keep their keys.
	fileSlugPrefix = "file-"
)

// LoadShortcuts is the built-in shortcuts overlaid with the reviewer's folder,
// by the prompt library's rules (override by slug, skip what doesn't parse).
func LoadShortcuts(userDir string) []Prompt {
	return loadPromptLibrary(builtinShortcutsFS, "builtin_shortcuts", userDir)
}

// allShortcuts is every shortcut, most used first (ties by label): the line
// built-ins and your shortcuts folder, the whole-file prompts (built-in and your
// prompts folder), then each accepted learning that adds one.
func (c *PrereviewController) allShortcuts() []Shortcut {
	var uses map[string]int
	var hidden map[string]bool
	if c.LibraryDir != "" {
		uses = library.ShortcutUseCounts(c.LibraryDir)
		hidden = library.HiddenShortcuts(c.LibraryDir)
	}
	var out []Shortcut
	add := func(slug, label, body, source, scope string) {
		out = append(out, Shortcut{Slug: slug, Label: label, Body: unwrapParagraphs(body), Source: source,
			Uses: uses[slug], Hidden: hidden[slug], Scope: scope})
	}
	source := func(p Prompt) string {
		if p.User {
			return shortcutYours
		}
		return shortcutBuiltin
	}
	for _, p := range LoadShortcuts(c.ShortcutsDir) {
		add(p.Slug, p.Title, p.Body, source(p), ScopeLine)
	}
	for _, p := range LoadPrompts(c.PromptsDir) {
		add(fileSlugPrefix+p.Slug, p.Title, p.Body, source(p), ScopeFile)
	}
	for _, l := range c.learnings() {
		if l.Status == library.StatusAccepted && l.ShortcutBody != "" {
			add("learned-"+l.ID, l.ShortcutLabel, l.ShortcutBody, shortcutLearned, shortcutScope(l.ShortcutScope))
		}
	}
	slices.SortStableFunc(out, func(a, b Shortcut) int { return b.Uses - a.Uses })
	return out
}

// shortcutScope is a learning's chip scope: ScopeFile, or ScopeLine by default.
func shortcutScope(s string) string {
	if s == ScopeFile {
		return ScopeFile
	}
	return ScopeLine
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

// applyShortcuts loads the chips (hidden ones left out) and, while it's open, the
// Library's Shortcuts tab, from one pass. It runs at Mount and when the Library
// changes, never on a tap, so a tap doesn't move the chips under the reviewer's
// finger; new use counts reorder them on the next load.
func (c *PrereviewController) applyShortcuts(state *PrereviewState) {
	all := c.allShortcuts()
	state.ShortcutsTab = nil
	if state.ShowLibrary && state.LibraryOnShortcuts() {
		t := &ShortcutsTab{Rows: slices.Clone(all)}
		for _, s := range all {
			if s.Scope == ScopeFile {
				t.File = append(t.File, s)
			} else {
				t.Line = append(t.Line, s)
			}
		}
		state.ShortcutsTab = t
	}
	state.Shortcuts = slices.DeleteFunc(all, func(s Shortcut) bool { return s.Hidden })
}

// ShortcutsTab is the Library's Shortcuts tab: every shortcut, hidden ones too,
// and the same split into its two lists (#275).
type ShortcutsTab struct {
	Rows       []Shortcut
	Line, File []Shortcut
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
	return state, nil
}

// fileChipsShown is how many whole-file chips the file comment box shows before
// More (#275).
const fileChipsShown = 4

// ShowShortcuts gates the chips: a line, block, text or file composer, not an area
// or region one, and not while re-anchoring a comment.
func (s PrereviewState) ShowShortcuts() bool {
	switch s.CommentMode {
	case "", "text", "file":
	default:
		return false
	}
	return s.ReanchorCommentID == "" && len(s.composerScope()) > 0
}

// composerScope is every chip for the open comment box: whole-file ones for the
// file composer, line ones for the rest, most used first.
func (s PrereviewState) composerScope() []Shortcut {
	scope := ScopeLine
	if s.CommentMode == "file" {
		scope = ScopeFile
	}
	var out []Shortcut
	for _, sc := range s.Shortcuts {
		if sc.Scope == scope {
			out = append(out, sc)
		}
	}
	return out
}

// ComposerShortcuts is the chips the open comment box shows: every line chip, or
// the first fileChipsShown whole-file ones until More is tapped.
func (s PrereviewState) ComposerShortcuts() []Shortcut {
	all := s.composerScope()
	if s.CommentMode == "file" && !s.ShowMoreShortcuts && len(all) > fileChipsShown {
		return all[:fileChipsShown]
	}
	return all
}

// HasMoreShortcuts reports whole-file chips held back behind More.
func (s PrereviewState) HasMoreShortcuts() bool {
	return s.CommentMode == "file" && !s.ShowMoreShortcuts && len(s.composerScope()) > fileChipsShown
}

// ShowMoreShortcutChips shows every whole-file chip (More). The chip submits with the
// composer's form, so what was typed is kept.
func (c *PrereviewController) ShowMoreShortcutChips(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ShowMoreShortcuts = true
	state.DraftBody = ctx.GetString("body")
	state.ComposerNonce++
	return state, nil
}

// OpenShortcutLibrary opens the Library's Shortcuts tab: the file comment box's
// "Add your own" (#275), where both folders are named.
func (c *PrereviewController) OpenShortcutLibrary(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if !state.ShowLibrary {
		toggleFullView(&state, &state.ShowLibrary)
	}
	state.LibraryTab = libTabShortcuts
	c.applyLibrary(&state)
	return state, nil
}

// UseShortcut adds a shortcut's text to the open composer. The chip submits with the
// composer's form, so "body" is what the reviewer has typed so far (never a stale
// autosave); the shortcut goes after it, or replaces it when it's empty. The
// selection (or the file comment) is kept. Bumping ComposerNonce remounts the
// composer: livetemplate never writes into the focused textarea it would otherwise
// patch.
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
