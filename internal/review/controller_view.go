package review

import (
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
)

// ToggleCommentList flips between the diff viewer and the all-comments
// overview pane. Bound to the "N comments" entry in the overflow menu.
// Closes the menu so the user sees the result immediately.
func (c *PrereviewController) ToggleCommentList(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if state.ShowAllComments {
		closeFullViews(&state)
		state.MoreMenuOpen = false
		state.LastResolvedIDs = nil // Resolve all's Undo belongs to the screen (a deleted comment's stays)
		return state, nil
	}
	c.openCommentList(&state, "")
	return state, nil
}

// OpenAnnotations opens the Annotations page on the live site's annotations, from the
// live page's count or a tap on an annotation's marker. With an id, that
// annotation's card scrolls into view.
func (c *PrereviewController) OpenAnnotations(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.openCommentList(&state, CommentFilterAnnotations)
	clearScrollTargets(&state)
	state.ScrollToCommentID = ctx.GetString("id")
	return state, nil
}

// FocusHTMLBlock answers a tap on an HTML preview marker: the block's card
// opens (PreviewCard) and its comments' boxes show.
func (c *PrereviewController) FocusHTMLBlock(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	state.HiddenBoxes = nil // opening or closing a marker shows all its boxes next time
	if id == state.FocusedHTMLBlock {
		// A second tap on the same marker hides its boxes again.
		state.FocusedHTMLBlock = ""
		return state, nil
	}
	clearScrollTargets(&state)
	state.FocusedHTMLBlock = id
	return state, nil
}

// ClosePreviewCard answers the × on the HTML preview's card (PreviewCard): it
// clears everything that keeps a card open, short of a composer, which closes
// with its own Cancel.
func (c *PrereviewController) ClosePreviewCard(state PrereviewState, _ *livetemplate.Context) (PrereviewState, error) {
	state.HiddenBoxes = nil
	closeReply(&state)
	clearScrollTargets(&state) // clears FocusedHTMLBlock too
	return state, nil
}

// RaiseBox answers a tap on a preview box a marker opened: it comes to the
// front, above the boxes it overlaps.
func (c *PrereviewController) RaiseBox(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FrontBoxID = ctx.GetString("box")
	return state, nil
}

// UnfocusAnnotation answers Hide on an annotation's corner bar: on the live
// site's focused box it unfocuses that annotation; on a preview box it hides
// that box, and once the open marker has nothing left showing, closes it. Each
// surface keeps the other's focus, as both can be set in a combined session.
func (c *PrereviewController) UnfocusAnnotation(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id != "" && id == state.FocusedCommentID {
		state.FocusedCommentID = ""
		return state, nil
	}
	if id == "" {
		state.FocusedHTMLBlock, state.HiddenBoxes = "", nil
		return state, nil
	}
	hidden := maps.Clone(state.HiddenBoxes)
	if hidden == nil {
		hidden = map[string]bool{}
	}
	hidden[id] = true
	state.HiddenBoxes = hidden
	for _, m := range state.HTMLMarkers() {
		if m.Key != state.FocusedHTMLBlock {
			continue
		}
		if m.Unboxed {
			return state, nil // the block's outline still shows; the marker closes it
		}
		for _, b := range m.Boxed {
			if !hidden[b.ID] {
				return state, nil // a box of the open marker still shows
			}
		}
	}
	state.FocusedHTMLBlock, state.HiddenBoxes = "", nil
	return state, nil
}

// SetCommentFilter switches the Annotations page between every comment
// ("") and the live site's annotations.
func (c *PrereviewController) SetCommentFilter(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	switch f := ctx.GetString("filter"); f {
	case "", CommentFilterAnnotations:
		state.CommentListFilter = f
	default:
		return state, fmt.Errorf("setCommentFilter: unknown filter %q", f)
	}
	state.LastResolvedIDs = nil // Undo belongs to the list it resolved
	return state, nil
}

// openCommentList opens the Annotations page (closing the other full-page
// views) with filter applied.
func (c *PrereviewController) openCommentList(state *PrereviewState, filter string) {
	closeFullViews(state)
	state.ShowAllComments = true
	state.CommentListFilter = filter
	state.MoreMenuOpen = false
	state.LastResolvedIDs = nil // Resolve all's Undo belongs to the screen (a deleted comment's stays)
	// Opening the overview must show accurate badges/snippets for every
	// commented file, including ones never opened this session.
	if c.relocateAll(state) {
		if err := c.persist(state.Comments); err != nil {
			slog.Warn("self-heal persist (all comments)", "err", err)
		}
	}
}

// ToggleShowResolved (the r key) switches the open surface's Annotations filter
// between Open and All. Closes the overflow menu so the effect shows at once.
func (c *PrereviewController) ToggleShowResolved(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.MoreMenuOpen = false
	// With no resolved comments, toggling has no visible effect — surface a
	// flash so the keypress isn't a silent no-op.
	if state.ResolvedCount() == 0 {
		state.Flash = "No resolved comments"
		return state, nil
	}
	state.editAnnShow(func(f AnnFilter) AnnFilter { return f.flipShow(annShowAll) })
	state.Flash = ""
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

// ToggleQueueScope switches the queue panel between THIS FILE's work (the default — the
// queue is about the document in front of you) and the whole review's (#171). Persisted
// per-user, so the choice survives the relaunch-per-review workflow.
//
// A view filter only: the agent is always handed every actionable item in the review, so
// flipping this can never strand work it would otherwise have picked up.
func (c *PrereviewController) ToggleQueueScope(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.QueueGlobal = !state.QueueGlobal
	c.savePrefs(state)
	return state, nil
}

// ToggleRow flips one row's annotation cards away from their default visibility (#174) —
// the count badge's click. Green badge: peek the done cards (what it always did). Yellow
// badge: collapse the open comment/suggestion behind the badge, so it can be set aside and
// come back to later without being resolved.
//
// Server-side on purpose: the old `peek-done` was a client-only class the server never
// emitted, so any re-render morphed it away and silently re-collapsed a card the reviewer
// had just opened (#173). Now the server owns it and the morph converges.
//
// The row key is the badge's own: "<line>-<side>" in the diff, "MB-<start>-<end>" in the
// md-view. Deleting rather than storing false keeps the persisted map to just the rows the
// reviewer actually flipped.
func (c *PrereviewController) ToggleRow(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	key := ctx.GetString("row")
	if key == "" {
		return state, fmt.Errorf("toggleRow: missing row key")
	}
	if _, on := state.ToggledRows[key]; on {
		delete(state.ToggledRows, key)
		return state, nil
	}
	if state.ToggledRows == nil {
		state.ToggledRows = map[string]string{}
	}
	// Record the default we're flipping AWAY FROM, so the toggle expires if that default
	// later changes (RowToggled) instead of silently inverting into its opposite.
	state.ToggledRows[key] = state.rowState(key)
	return state, nil
}

// ToggleMarks switches the open surface's Annotations filter between None
// (no badges or cards, #151, so the diff reads clean) and Open.
func (c *PrereviewController) ToggleMarks(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.editAnnShow(func(f AnnFilter) AnnFilter { return f.flipShow(annShowNone) })
	state.MoreMenuOpen = false
	return state, nil
}

// ToggleBoxes shows or hides the outline of every annotation's box on the live
// site and the HTML preview.
func (c *PrereviewController) ToggleBoxes(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ShowBoxes = !state.ShowBoxes
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

// ToggleHideWhitespace shows or stops showing whitespace-only changes as
// unchanged (#115 B1). A durable view pref.
func (c *PrereviewController) ToggleHideWhitespace(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.HideWhitespace = !state.HideWhitespace
	state.MoreMenuOpen = false
	c.savePrefs(state)
	return state, nil
}

// DismissSinceLook stops marking what changed in the open file since your last
// look (#115 B4).
func (c *PrereviewController) DismissSinceLook(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	delete(state.SinceLook, state.SelectedFile)
	c.applySinceLook(&state)
	return state, nil
}

// DiffSinceLook shows the full diff from your last look to the file now,
// removed lines included: the read-only comparison view versions use.
func (c *PrereviewController) DiffSinceLook(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	base, held := state.SinceLook[state.SelectedFile]
	if !held || c.Versions == nil {
		return state, nil
	}
	old, err := c.Versions.Blob(base.SHA)
	if err != nil {
		return state, fmt.Errorf("diff since look: %w", err)
	}
	cur, _ := os.ReadFile(filepath.Join(c.RepoPath, state.SelectedFile))
	c.showHistoricalDiff(&state, gitdiff.DiffContents(state.SelectedFile, old, cur))
	state.ViewingVersion, state.VersionViewDiff, state.VersionViewSeq = true, true, 0
	state.SelectionAnchor, state.SelectionEnd = 0, 0
	state.CommentMode = ""
	c.applyVersionList(&state)
	return state, nil
}

// ToggleFileScope flips the drawer file list between changed-only
// (default) and all tracked files. See PrereviewState.ShowAllFiles and
// scopedFiles. Lives in the drawer, so no overflow-menu interaction.
func (c *PrereviewController) ToggleFileScope(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ShowAllFiles = !state.ShowAllFiles
	return state, nil
}

// ToggleSuggestions (the s key) turns Suggestions on or off in the open
// surface's Annotations filter (issue #98). It never touches the suggestions.
func (c *PrereviewController) ToggleSuggestions(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.editAnnFilter(func(f AnnFilter) AnnFilter { return f.flipKind(navSuggestion) })
	state.MoreMenuOpen = false
	return state, nil
}

// SetMarkdownView is the idempotent setter behind Preview and Raw (the file
// header, and the ⋯ menu on a phone). Reads form field `view`; anything other
// than "raw" resolves to rendered. Clicking the one already on is a no-op for
// state: the value is the final mode, not a flip.
func (c *PrereviewController) SetMarkdownView(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RawMarkdown = ctx.GetString("view") == "raw"
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
	closeFullViews(&state)
	// Heading scroll and comment scroll/focus are mutually exclusive intents —
	// clear any pending comment target so it doesn't steal the scroll/focus
	// (the comment card carries lvt-fx:scroll + lvt-autofocus on this match).
	clearScrollTargets(&state)
	state.ScrollToHeadingID = id
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
	if state.Comments[idx].IsRegionLevel() {
		return focusLive(state, id), nil
	}
	state, err := c.selectFileForJump(state, state.Comments[idx].File)
	if err != nil {
		return state, err
	}
	state.scrollToComment(id, state.Comments[idx].ToLine)
	return state, nil
}

// JumpToSuggestion selects the file a suggestion lives in, from its row in the
// queue panel (#159), and scrolls its card into view (ScrollToItemKey, #193).
func (c *PrereviewController) JumpToSuggestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("jumpToSuggestion: missing id")
	}
	sg := state.findSuggestion(id)
	if sg == nil {
		return state, fmt.Errorf("jumpToSuggestion: id %s not found", id)
	}
	state, err := c.selectFileForJump(state, sg.File)
	if err == nil {
		state.scrollToSuggestion(id, sg.ToLine)
	}
	return state, err
}

// JumpToQuestion opens the file a question set is on, from its Queue row, and
// scrolls its card into view.
func (c *PrereviewController) JumpToQuestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	set, err := findQuestionSet(&state, ctx.GetString("id"))
	if err != nil {
		return state, fmt.Errorf("jumpToQuestion: %w", err)
	}
	id, line := set.ID, set.ToLine
	state, err = c.selectFileForJump(state, set.File)
	if err == nil {
		state.scrollToQuestion(id, line)
	}
	return state, err
}

// selectFileForJump switches the view to file (loading its diff if it changed)
// and closes the full-page views (Annotations, quiz, Library), preserving unsaved composer text (#105).
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
		c.enterFile(&state, file, diff)
	}
	closeFullViews(&state)
	state.LiveView = false
	clearScrollTargets(&state)
	return state, nil
}

// ToggleLiveView switches surfaces in a combined session (#114): the header's
// Live toggle and the "l" shortcut. It flips what is on screen, so from a
// full-page view over the live site it brings the site back.
func (c *PrereviewController) ToggleLiveView(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	showLive(&state, !state.LiveShowing())
	return state, nil
}

// focusLive jumps to a live-site annotation (#114): it shows the live site and
// focuses the annotation, which moves the framed site to its page and scrolls
// to it, as the live-only sidebar's Locate does. The file stays selected, for
// the switch back.
func focusLive(state PrereviewState, id string) PrereviewState {
	showLive(&state, true)
	clearScrollTargets(&state)
	focusRegion(&state, id)
	return state
}

// showLive shows the live site (on) or the files. Either way the reviewer lands
// on that surface, so a full-page view closes; and the region overlay disarms,
// as it does on a file switch, because the HTML preview and the live stage each
// have one and they share RegionSelectArmed. Only a combined session has two
// surfaces to switch between; any other ignores it.
func showLive(state *PrereviewState, on bool) {
	if !state.Combined() {
		return
	}
	state.LiveView = on
	closeFullViews(state)
	state.RegionSelectArmed = false
	state.MoreMenuOpen = false
}

// clearScrollTargets drops every one-render scroll nudge, so a jump that sets none
// (or a different one) leaves no stale target behind.
func clearScrollTargets(state *PrereviewState) {
	state.FocusedHTMLBlock = ""
	state.ScrollToCommentID = ""
	state.ScrollToHeadingID = ""
	state.ScrollToItemKey = ""
}

// closeFullViews returns to the file view from any full-page view: the
// all-comments overview, the quiz, and the Library. Every jump into a file calls it.
func closeFullViews(state *PrereviewState) {
	state.ShowAllComments = false
	state.ShowUnderstand = false
	state.ShowLibrary = false
}

// toggleFullView opens one full-page view (closing the others) or closes it.
// view points at one of state's Show* flags.
func toggleFullView(state *PrereviewState, view *bool) {
	open := !*view
	closeFullViews(state)
	*view = open
}

// SetAnnShow sets the open surface's Show: "open", "all" or "none".
func (c *PrereviewController) SetAnnShow(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	show := ctx.GetString("show")
	if show == "open" {
		show = annShowOpen
	}
	if show != annShowOpen && show != annShowAll && show != annShowNone {
		return state, nil
	}
	state.editAnnShow(func(f AnnFilter) AnnFilter { f.Show = show; return f })
	state.MoreMenuOpen = false
	return state, nil
}

// ToggleAnnKind turns one kind on or off in the open surface's filter.
func (c *PrereviewController) ToggleAnnKind(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	kind := ctx.GetString("kind")
	if navKindIndex(kind) < 0 {
		return state, nil
	}
	state.editAnnFilter(func(f AnnFilter) AnnFilter { return f.flipKind(kind) })
	state.MoreMenuOpen = false
	return state, nil
}
