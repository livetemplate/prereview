package review

import (
	"fmt"
	"github.com/livetemplate/livetemplate"
	"slices"
)

// ToggleCommentList flips between the diff viewer and the all-comments
// overview pane. Bound to the "N comments" entry in the overflow menu.
// Closes the menu so the user sees the result immediately.
func (c *PrereviewController) ToggleCommentList(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ShowAllComments = !state.ShowAllComments
	state.MoreMenuOpen = false
	// Opening the overview must show accurate badges/snippets for every
	// commented file, including ones never opened this session.
	if state.ShowAllComments {
		c.relocateAll(&state)
	}
	return state, nil
}

// ToggleShowResolved flips whether resolved comments are visible in the
// inline diff and the all-comments overview. Default off — resolved
// comments add noise once they're handled. Bound to an entry in the
// overflow menu. Closes the menu so the user can immediately see the
// effect on the diff.
func (c *PrereviewController) ToggleShowResolved(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.MoreMenuOpen = false
	// With no resolved comments, toggling has no visible effect — surface a
	// flash so the keypress isn't a silent no-op.
	if state.ResolvedCount() == 0 {
		state.Flash = "No resolved comments"
		return state, nil
	}
	state.ShowResolved = !state.ShowResolved
	state.Flash = ""
	c.savePrefs(state)
	return state, nil
}

// ClearFlash dismisses the transient status toast (auto-clicked after a few
// seconds, or via the toast's manual dismiss button).
func (c *PrereviewController) ClearFlash(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.Flash = ""
	return state, nil
}

// ToggleFocusMode flips the distraction-free reading view. When on, the
// desktop file drawer (left) and TOC sidebar (right) are hidden so the
// center reading surface gets the full width. Persisted per-user. Closes
// the overflow menu so the effect is visible immediately (the menu entry
// is the mobile control; the desktop control is a toolbar button).
func (c *PrereviewController) ToggleFocusMode(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FocusMode = !state.FocusMode
	state.MoreMenuOpen = false
	c.savePrefs(state)
	return state, nil
}


// ToggleMarks flips whether the per-line comment/suggestion count badges (#151)
// and their inline cards are shown in the diff. Off by default (badges visible);
// when on, the diff reads clean so a reviewer can scan the raw code. Persisted
// per-user. Closes the overflow menu so the effect is visible immediately.
func (c *PrereviewController) ToggleMarks(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.HideMarks = !state.HideMarks
	state.MoreMenuOpen = false
	c.savePrefs(state)
	return state, nil
}

// CycleTheme advances the Light/Dark/System color mode (issue #60), cycling
// System → Light → Dark → System. Persisted per-user; the page re-renders with
// the new data-mode attribute (omitted for System) and the cascade does the
// rest — no JS, no CSS refetch (/syntax.css already carries both modes). The
// overflow menu is left open so a click from inside it can cycle again in
// place; the toolbar button is the primary control.
func (c *PrereviewController) CycleTheme(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ThemeMode = state.NextThemeMode()
	c.savePrefs(state)
	return state, nil
}

// CycleScheme advances the curated color scheme (the Theme axis, issue from
// the UI-overhaul plan), cycling through gitdiff.Schemes in registry order.
// Persisted per-user; the page re-renders with the new data-scheme attribute
// and the cascade re-skins chrome + diff — no JS, no CSS refetch (/syntax.css
// carries every scheme). Like CycleTheme, leaves the overflow menu open so a
// click from inside it can cycle again in place.
func (c *PrereviewController) CycleScheme(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.SchemeName = state.NextScheme()
	c.savePrefs(state)
	return state, nil
}

// ToggleKeyboardHelp opens/closes the keyboard-shortcuts help overlay.
// Triggered by the "?" key and the toolbar help button (both dispatch
// toggleKeyboardHelp); Esc closes it via ClearSelection. Closes the overflow
// menu so the two overlays don't stack.
func (c *PrereviewController) ToggleKeyboardHelp(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.KeyHelpOpen = !state.KeyHelpOpen
	state.MoreMenuOpen = false
	return state, nil
}

// ToggleMoreMenu opens/closes the 3-dots overflow menu in the top bar.
// Mirrors the file-drawer pattern: state-driven boolean + CSS class
// toggle. No JS. Backdrop tap submits CloseMoreMenu.
func (c *PrereviewController) ToggleMoreMenu(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.MoreMenuOpen = !state.MoreMenuOpen
	return state, nil
}

// CloseMoreMenu is the explicit close action — bound to the menu
// backdrop so tapping outside dismisses without toggling the open state
// to "true" on a subsequent click.
func (c *PrereviewController) CloseMoreMenu(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.MoreMenuOpen = false
	return state, nil
}

// OpenTOC opens the mobile Table-of-Contents overlay. Bound to the
// "Table of contents" entry in the 3-dots menu, so the menu must close
// at the same time — otherwise the dropdown stays drawn over the
// overlay it just summoned. Desktop never renders this entry; the TOC
// is a permanent right sidebar there.
func (c *PrereviewController) OpenTOC(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.TOCOpen = true
	state.MoreMenuOpen = false
	return state, nil
}

// CloseTOC dismisses the mobile TOC overlay. Bound to two things: the
// backdrop tap (close-without-jump) and the click on a heading link
// inside the overlay (close-and-jump — the <a href="#…"> performs the
// native anchor scroll, this action closes the overlay in the same
// gesture). Browser-level anchor navigation is unaffected because
// event-delegation.ts only preventDefault's submit and drag events.
func (c *PrereviewController) CloseTOC(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.TOCOpen = false
	return state, nil
}

// ToggleFileView flips between diff-overlay mode (default) and plain
// file-view mode. See PrereviewState.FileView. Closes the overflow
// menu so the effect on the diff is immediately visible.
func (c *PrereviewController) ToggleFileView(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileView = !state.FileView
	state.MoreMenuOpen = false
	c.savePrefs(state)
	return state, nil
}

// ToggleFileScope flips the drawer file list between changed-only
// (default) and all tracked files. See PrereviewState.ShowAllFiles and
// scopedFiles. Lives in the drawer, so no overflow-menu interaction.
func (c *PrereviewController) ToggleFileScope(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ShowAllFiles = !state.ShowAllFiles
	return state, nil
}

// ToggleSuggestions shows/hides the inline LLM suggestion boxes for the current
// view (issue #98). A declutter toggle only — it never touches the underlying
// suggestions. Session-scoped (HideSuggestions is lvt:"persist"), so it isn't a
// durable per-user pref; closes the overflow menu so the effect is visible on
// mobile.
func (c *PrereviewController) ToggleSuggestions(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.HideSuggestions = !state.HideSuggestions
	state.MoreMenuOpen = false
	return state, nil
}

// ToggleRawMarkdown flips a Markdown file between the rendered view
// (default) and the raw line view. Closes the overflow menu so the
// effect is immediately visible on mobile.
func (c *PrereviewController) ToggleRawMarkdown(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RawMarkdown = !state.RawMarkdown
	state.MoreMenuOpen = false
	c.savePrefs(state)
	return state, nil
}

// SetMarkdownView is the idempotent setter behind the desktop radio
// group (Rendered / Raw). Reads form field `view`; anything other than
// "raw" resolves to rendered. Unlike ToggleRawMarkdown, clicking the
// already-active radio is a no-op for state — the value reflects the
// final mode, not a flip.
func (c *PrereviewController) SetMarkdownView(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RawMarkdown = ctx.GetString("view") == "raw"
	state.MoreMenuOpen = false
	c.savePrefs(state)
	return state, nil
}

// ToggleRawHTML is the .html/.htm equivalent of ToggleRawMarkdown:
// flips the iframe preview off and the syntax-highlighted line view on
// (and back). Closes the overflow menu so the change is visible on
// mobile.
func (c *PrereviewController) ToggleRawHTML(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RawHTML = !state.RawHTML
	state.MoreMenuOpen = false
	c.savePrefs(state)
	return state, nil
}

// SetHTMLView is the idempotent setter behind the HTML Preview/Raw
// radio group. Mirrors SetMarkdownView: "raw" → line view, anything
// else → iframe preview.
func (c *PrereviewController) SetHTMLView(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RawHTML = ctx.GetString("view") == "raw"
	state.MoreMenuOpen = false
	c.savePrefs(state)
	return state, nil
}

// SetFileViewMode is the setter counterpart of ToggleFileView for the
// desktop radio group (Diff / File). Reads form field `view`; "file"
// → FileView true, anything else (incl. "diff") → false.
func (c *PrereviewController) SetFileViewMode(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileView = ctx.GetString("view") == "file"
	state.MoreMenuOpen = false
	c.savePrefs(state)
	return state, nil
}

// NavigateToHeading is the server-side bookkeeping for a TOC heading
// link click. It dismisses both the mobile TOC overlay AND the
// all-comments overview, then records ScrollToHeadingID so the
// framework's `lvt-fx:scroll="into-view"` directive on the matching
// MarkdownBlock scrolls the section into view on the next render —
// the same declarative pattern JumpToComment uses for comments.
//
// Fixes issue #12: previously the TOC link only closed the overlay
// (closeTOC); from inside all-comments view the heading was never in
// the DOM, so the native anchor scroll missed and the user stayed
// stuck on the comments overview.
//
// data-id on the link supplies the slugified heading id (matches the
// `id="..."` goldmark's WithAutoHeadingID writes into the rendered
// HTML and that ExtractHeadings reads back).
func (c *PrereviewController) NavigateToHeading(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("navigateToHeading: missing id")
	}
	state.TOCOpen = false
	state.ShowAllComments = false
	state.ScrollToHeadingID = id
	// Heading scroll and comment scroll/focus are mutually exclusive intents —
	// clear any pending comment target so it doesn't steal the scroll/focus
	// (the comment card carries lvt-fx:scroll + lvt-autofocus on this match).
	state.ScrollToCommentID = ""
	return state, nil
}

// JumpToComment closes the all-comments view, selects the comment's
// file, and sets ScrollToCommentID so the framework's
// `lvt-fx:scroll="into-view"` directive on the matching inline comment
// scrolls it into view on the next render. Pure declarative wiring —
// no custom app-level JS.
func (c *PrereviewController) JumpToComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("jumpToComment: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("jumpToComment: id %s not found", id)
	}
	state, err := c.selectFileForJump(state, state.Comments[idx].File)
	if err != nil {
		return state, err
	}
	state.ScrollToCommentID = id
	return state, nil
}

// JumpToSuggestion selects the file an accepted suggestion lives in, from its
// row in the queue panel (#159). It stops at file selection — suggestions have
// no per-item scroll target today (unlike ScrollToCommentID), so the reviewer
// lands on the file with the inline suggestion box visible; a precise scroll can
// ride along with the collapse-to-badge work.
func (c *PrereviewController) JumpToSuggestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("jumpToSuggestion: missing id")
	}
	sg := state.findSuggestion(id)
	if sg == nil {
		return state, fmt.Errorf("jumpToSuggestion: id %s not found", id)
	}
	return c.selectFileForJump(state, sg.File)
}

// selectFileForJump switches the view to file (loading its diff if it changed)
// and closes the all-comments overview, preserving unsaved composer text (#105).
// Shared by JumpToComment/JumpToSuggestion; each sets its own scroll target after
// (or none). Clears both one-render scroll nudges so a caller that sets neither
// leaves no stale target behind.
func (c *PrereviewController) selectFileForJump(state PrereviewState, file string) (PrereviewState, error) {
	state = c.materializeDraft(state)
	if file != state.SelectedFile {
		diff, err := c.loadDiffCached(state.Base, file)
		if err != nil {
			return state, fmt.Errorf("load diff: %w", err)
		}
		state.SelectedFile = file
		state.CurrentDiff = diff
	}
	state.ShowAllComments = false
	state.ScrollToCommentID = ""
	state.ScrollToHeadingID = ""
	return state, nil
}
