package review

import (
	"fmt"
	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
)

// SelectLine implements two-click range selection. data-line and data-side
// arrive from <button lvt-on:click="selectLine" data-line="N" data-side="new">.
//
//   - First click on a line: anchor = end = N.
//   - Second click on a different line: end = N (range complete).
//   - Third click: reseat as a new anchor.
//
// Side is captured on the first click and locked thereafter so a range
// can't accidentally span sides of the diff.
//
// EXCEPT when the line already carries a comment (#174): a line is ONE conversation, so
// clicking it OPENS that thread rather than starting a second comment on top of it — see
// openThreadOnLine. Further input belongs in the thread as a reply. A separate comment on
// an already-commented line is still reachable by selecting text on it (kind=text).
func (c *PrereviewController) SelectLine(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	n := ctx.GetInt("line")
	if n <= 0 {
		return state, fmt.Errorf("selectLine: missing or invalid 'line'")
	}
	side := ctx.GetString("side")
	if side == "" {
		side = "new"
	}

	// Only on a FRESH click (no range in progress). Mid-range the reviewer is deliberately
	// extending a selection across lines, and one of those lines happening to carry a comment
	// must not hijack it.
	if state.SelectionAnchor == 0 {
		if opened, ok := openThreadOnLine(state, n, side); ok {
			return opened, nil
		}
	}

	switch {
	case state.SelectionAnchor == 0:
		// First click — start a new range.
		state.SelectionAnchor = n
		state.SelectionEnd = n
		state.SelectionSide = side
	case state.SelectionAnchor != 0 && state.SelectionAnchor == state.SelectionEnd:
		// Anchor placed but not yet extended — this click sets the end.
		// Reject cross-side extensions; require explicit ClearSelection first.
		if side != state.SelectionSide {
			state.SelectionAnchor = n
			state.SelectionEnd = n
			state.SelectionSide = side
		} else {
			state.SelectionEnd = n
		}
	default:
		// Range already complete — start over from this line.
		state.SelectionAnchor = n
		state.SelectionEnd = n
		state.SelectionSide = side
	}
	return state, nil
}

// openThreadOnLine handles a click on a line that ALREADY carries a comment (#174): a line
// is one conversation, so the click opens that thread instead of composing a second comment
// on top of it. It reveals the card (clearing any collapse the reviewer had toggled on, so a
// click on a line whose comment is hidden brings it back rather than appearing to do nothing)
// and arms the reply box, putting the cursor straight into the conversation.
//
// Reports false when the line has no comment, in which case the caller falls through to
// ordinary range selection and the new-comment composer.
//
// It matches exactly the comments the diff RENDERS on that row — the same predicate as
// CommentsByEndLine (file- and area-level comments don't belong to a line at all).
//
// RESOLVED and outdated comments are deliberately skipped: a closed thread must not swallow
// a click that was meant to start a fresh comment on that line.
//
// Side matters: a modified line's number exists on BOTH the del(old) and add(new) rows, so a
// comment on the old side must not hijack a click on the new one. A comment with no side
// sits on both rows and matches either.
func openThreadOnLine(state PrereviewState, line int, side string) (PrereviewState, bool) {
	for _, cm := range state.Comments {
		if cm.File != state.SelectedFile || cm.ToLine != line {
			continue
		}
		if cm.IsFileLevel() || cm.IsAreaLevel() || cm.Hidden {
			continue
		}
		if cm.Resolved || cm.AnchorOutdated() {
			continue
		}
		if cm.Side != "" && cm.Side != side {
			continue
		}
		// Un-collapse the row so the thread is actually ON SCREEN — otherwise clicking a line
		// whose comment the reviewer had hidden (#174) would silently do nothing.
		for _, k := range rowKeysFor(line, side) {
			delete(state.ToggledRows, k)
		}
		state.ReplyingID = cm.ID
		state.ReplyDraft = ""
		// Clear the selection, or the NEW-comment composer renders on top of the thread.
		state.SelectionAnchor = 0
		state.SelectionEnd = 0
		state.SelectionSide = ""
		return state, true
	}
	return state, false
}

// openThreadOnBlock is openThreadOnLine's rendered-view twin: a click on a Markdown/HTML
// block that ALREADY carries a comment opens that thread rather than composing a second one.
//
// It iterates FileComments() — literally the list the "blockComments" partial renders — and
// uses the partial's own membership rule (a comment belongs to the block when its ToLine
// falls inside the block's source range), so what the reviewer clicked and what opens can
// never drift apart. Resolved / outdated comments are skipped for the same reason as on a
// line: a closed thread must not swallow a click meant to start a fresh comment.
//
// No side gate: the rendered views have no diff sides (every block is "new").
func openThreadOnBlock(state PrereviewState, from, to int) (PrereviewState, bool) {
	for _, cm := range state.FileComments() {
		if cm.ToLine < from || cm.ToLine > to {
			continue
		}
		if cm.Resolved || cm.AnchorOutdated() {
			continue
		}
		// Un-collapse the block (#174's badge) so the thread is actually on screen.
		delete(state.ToggledRows, fmt.Sprintf("MB-%d-%d", from, to))
		state.ReplyingID = cm.ID
		state.ReplyDraft = ""
		// Clear the selection, or the NEW-comment composer renders inside the block on top
		// of the thread the reviewer just asked to see.
		state.SelectionAnchor = 0
		state.SelectionEnd = 0
		state.SelectionSide = ""
		return state, true
	}
	return state, false
}

// SelectBlock selects a whole source block in one shot: a rendered-
// Markdown block, a region drawn over the rendered-HTML preview, or a
// region drawn over the code view (issue #26 region comments). A block
// IS a range, so unlike SelectLine's two-click anchor/extend, this sets
// the full source line span at once — for the previews, data-from/data-to
// are the real source lines the client's region-select directive resolved
// the drawn box to. The existing composer/AddComment flow then anchors
// the comment to those lines, so it round-trips with the raw view and the
// CSV unchanged.
//
// `side` is optional and defaults to "new" (rendered Markdown/HTML have no
// diff sides; deep-link line numbers are post-diff). The code-view region
// path passes the side of the box's first matched row so a comment on a
// deleted ("old") row anchors correctly.
//
// A block, like a line, is ONE conversation (#174): CLICKING a rendered block that already
// carries a comment OPENS that thread instead of composing a second one on top of it — see
// openThreadOnBlock. A brand-new comment on that block is still reachable by selecting a
// phrase inside it (kind=text, data-surface="block").
func (c *PrereviewController) SelectBlock(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	from := ctx.GetInt("from")
	to := ctx.GetInt("to")
	if from <= 0 || to < from {
		return state, fmt.Errorf("selectBlock: invalid range from=%d to=%d", from, to)
	}
	side := ctx.GetString("side")
	if side != "old" {
		side = "new"
	}

	// Only a plain CLICK opens a thread. A region DRAWN over a preview reaches this same
	// handler (lvt-fx:region-select), but drawing a box is an unambiguous "comment on THIS
	// area" gesture, and the overlay that captures it only exists while armed — so an armed
	// overlay is exactly the signal that the reviewer meant a new comment, not a read.
	if !state.RegionSelectArmed {
		if opened, ok := openThreadOnBlock(state, from, to); ok {
			return opened, nil
		}
	}

	state.SelectionAnchor = from
	state.SelectionEnd = to
	state.SelectionSide = side
	// Capturing a region disarms the overlay so scrolling returns and the
	// composer is reachable (mirror of SelectImageArea).
	state.RegionSelectArmed = false
	return state, nil
}

// SelectText opens the composer for a CHARACTER range (a word / phrase / span
// across lines), dispatched from the client's lvt-fx:text-select directive when
// a native selection settles inside the diff. Payload: from_line/from_col +
// to_line/to_col (rune offsets, doc-ordered so from precedes to), side, and the
// exact selected text. It reuses the line composer's placement — SelectionAnchor
// /End are the line span, so the existing SelectionEndMax gate renders the
// composer under the range's last line — and adds the columns + snippet that
// distinguish a kind=text comment from a whole-line one.
func (c *PrereviewController) SelectText(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	fromLine := ctx.GetInt("fromLine")
	toLine := ctx.GetInt("toLine")
	if fromLine <= 0 || toLine < fromLine {
		return state, fmt.Errorf("selectText: invalid line range from=%d to=%d", fromLine, toLine)
	}
	side := ctx.GetString("side")
	if side != "old" {
		side = "new"
	}
	text := ctx.GetString("text")
	if text == "" {
		return state, fmt.Errorf("selectText: empty selection")
	}
	state.SelectionAnchor = fromLine
	state.SelectionEnd = toLine
	state.SelectionSide = side
	state.SelectionFromCol = ctx.GetInt("fromCol")
	state.SelectionToCol = ctx.GetInt("toCol")
	state.SelectionText = text
	state.CommentMode = commentKindText
	// A fresh selection replaces any prior edit/re-anchor/reply intent. Reply matters since
	// #174: clicking a commented line now OPENS that thread, so text-selecting on that same
	// line to add a second comment would otherwise render the reply box AND the new-comment
	// composer on one row.
	state.EditingCommentID = ""
	state.ReanchorCommentID = ""
	state.ReplyingID = ""
	state.ReplyDraft = ""
	return state, nil
}

// ToggleRegionSelect flips the "draw a box to comment" overlay for the
// current preview on/off. Bound to the "Select region" toggle button.
// Off by default so one-finger gestures scroll; on, the parent-document
// overlay (lvt-fx:region-select) captures a drag and resolves it to a
// pixel rect (image) or a source line range (rendered HTML / code).
func (c *PrereviewController) ToggleRegionSelect(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RegionSelectArmed = !state.RegionSelectArmed
	return state, nil
}

// ToggleAnnotations opens/closes the --external annotations sidebar (collapsed
// by default so the live site gets full width). Pure flip.
func (c *PrereviewController) ToggleAnnotations(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.AnnoDrawerOpen = !state.AnnoDrawerOpen
	return state, nil
}

// FocusComment marks a region annotation as the one to locate: its on-page
// pin renders highlighted and the client (via the beacon) navigates the
// iframe to its page + scrolls it into view. FocusSeq bumps every call so
// re-tapping the same annotation re-fires the client even with an unchanged id.
func (c *PrereviewController) FocusComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("focusComment: missing id")
	}
	state.FocusedCommentID = id
	state.FocusSeq++
	return state, nil
}

// ClearSelection wipes the line selection and any draft. Bound to a
// "Cancel" button next to the composer and to ESC keydown on the body.
func (c *PrereviewController) ClearSelection(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.SelectionFromCol = 0
	state.SelectionToCol = 0
	state.SelectionText = ""
	state.SelectionArea = Area{}
	state.RegionSelectArmed = false
	state.CommentMode = ""
	state.DraftBody = ""
	state.EditingCommentID = ""
	state.ReanchorCommentID = ""
	state.URLHashScrollAnchor = ""
	// Esc is the universal "close" key: it also dismisses the keyboard-help
	// overlay and the cmd+k search palette (the modals not implicitly closed by
	// clearing selection).
	state.KeyHelpOpen = false
	state.SearchOpen = false
	return state, nil
}

// lineCursorKey is the data-key the template stamps on each diff line button
// ("L<old>-<new>"), used to match the keyboard line cursor.
func lineCursorKey(l gitdiff.DiffLine) string {
	return fmt.Sprintf("L%d-%d", l.OldNum, l.NewNum)
}

// CursorDown / CursorUp move the keyboard line cursor through the diff (bound to
// ArrowDown / ArrowUp). The cursor line button is highlighted, scrolled into
// view, and focused, so Enter activates it (→ the line composer). j/k still
// switch files.
func (c *PrereviewController) CursorDown(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.moveCursor(state, +1), nil
}

func (c *PrereviewController) CursorUp(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.moveCursor(state, -1), nil
}

// moveCursor steps the cursor over commentable diff lines (fold markers are
// skipped — they aren't real lines). With no cursor yet, Down lands on the
// first line and Up on the last. No-op when the view has no diff lines (e.g.
// the rendered Markdown/HTML views).
func (c *PrereviewController) moveCursor(state PrereviewState, delta int) PrereviewState {
	lines := state.VisibleLines()
	keys := make([]string, 0, len(lines))
	for _, l := range lines {
		if l.Kind == "fold" {
			continue
		}
		keys = append(keys, lineCursorKey(l))
	}
	if len(keys) == 0 {
		return state
	}
	cur := -1
	for i, k := range keys {
		if k == state.CursorKey {
			cur = i
			break
		}
	}
	n := len(keys)
	var next int
	switch {
	case cur == -1 && delta > 0:
		next = 0
	case cur == -1:
		next = n - 1
	default:
		next = ((cur+delta)%n + n) % n
	}
	state.CursorKey = keys[next]
	return state
}

// SetURLHash dispatches from the client's url-hash directive when
// `location.hash` changes (page-load with a hash, address-bar edit,
// back-button, permalink click). Parses the hash via gitdiff.ParseHash
// and updates state.SelectedFile + selection range + anchor; loads
// the diff if the path changed and resolves to a known file. Tolerant
// of bogus hashes (an unrelated `#confirm-delete-xyz` dialog hash
// resolves to a non-existent file and is silently dropped) — the
// existing dialog/popover/details hash machinery in setupHashLink
// handles those independently.
//
// On a successful path match, also clears any in-progress composer
// state from the previous file (mirror of SelectFile), since the user
// just navigated.
func (c *PrereviewController) SetURLHash(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	hash := ctx.GetString("hash")
	parsed := gitdiff.ParseHash(hash)
	if parsed.Path == "" {
		// No path → nothing to do. The directive fires this on the
		// initial-load case too, so empty hashes are normal.
		return state, nil
	}

	// Path change requires a diff load; if the load fails (file not in
	// the repo), treat as no-op rather than surfacing a controller
	// error — the user pasted a stale link or hit an unrelated hash.
	if parsed.Path != state.SelectedFile {
		diff, err := c.loadDiffCached(state.Base, parsed.Path)
		if err != nil {
			return state, nil
		}
		state.SelectedFile = parsed.Path
		state.CurrentDiff = diff
		state.SelectionArea = Area{}
		state.RegionSelectArmed = false
		state.CommentMode = ""
		state.DraftBody = ""
		state.EditingCommentID = ""
		state.ReanchorCommentID = ""
		state.FileDrawerOpen = false
		state.ShowAllComments = false
		state.ShowQuiz = false
		// Refresh the per-file version timeline for the newly linked file, same as
		// SelectFile/stepFile/search do — otherwise the Versions panel keeps the
		// previously-selected file's history (the deep-link's mount-default on load).
		c.applyVersionList(&state)
	}

	if parsed.FromLine > 0 {
		state.SelectionAnchor = parsed.FromLine
		state.SelectionEnd = parsed.ToLine
		// Default side is "new" — line numbers in deep links are the
		// post-diff numbering. Matches selectLine's default for
		// add/ctx rows; user can still manually re-select if they
		// want the old side.
		state.SelectionSide = "new"
		state.URLHashScrollAnchor = ""
	} else if parsed.Anchor != "" {
		state.SelectionAnchor = 0
		state.SelectionEnd = 0
		state.SelectionSide = ""
		state.URLHashScrollAnchor = parsed.Anchor
		// Anchor inside a markdown file? Route through ScrollToHeadingID
		// so the existing block-scroll directive lights up. (HTML previews
		// render in one iframe and have no block-level scroll target.)
		if gitdiff.IsMarkdownPath(state.SelectedFile) {
			state.ScrollToHeadingID = parsed.Anchor
		}
	} else {
		// Path-only hash: same file, no target. Don't touch
		// SelectionAnchor — it might already be a meaningful selection.
		state.URLHashScrollAnchor = ""
	}

	c.relocateSelected(&state)
	return state, nil
}

// OpenFileComment opens the composer in "comment on whole file" mode.
// Distinct from SelectLine in that no line range is involved; the
// resulting Comment persists with Kind="file", FromLine=0, ToLine=0.
// Clears any pending line selection / edit / re-anchor so the composer
// renders once at the file head rather than twice. Mirrors the markdown
// raw-toggle's "close the overflow menu" behaviour so mobile clicks
// don't leave the menu hanging open over the composer.
func (c *PrereviewController) OpenFileComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if state.SelectedFile == "" {
		return state, fmt.Errorf("openFileComment: no file selected")
	}
	resetToFileComment(&state)
	return state, nil
}

// resetToFileComment clears every selection/edit field and opens a fresh whole-file
// composer (CommentMode=file). Shared by OpenFileComment and PickPrompt. It does NOT
// touch DraftBody — OpenFileComment leaves an in-progress draft intact; PickPrompt
// overwrites it with the prompt afterwards.
func resetToFileComment(state *PrereviewState) {
	state.CommentMode = commentKindFile
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.SelectionArea = Area{}
	state.RegionSelectArmed = false
	state.EditingCommentID = ""
	state.ReanchorCommentID = ""
	state.MoreMenuOpen = false
}

// PickPrompt opens a file-level comment PRE-FILLED with a chosen "ask for suggestions"
// prompt (#147): it mirrors OpenFileComment, then seeds DraftBody with the prompt's
// body, so the reviewer sees the instruction in the composer, can tweak/scope it, and
// hits Save — creating an ordinary kind=file comment the agent answers with
// `prereview suggest`. An unknown slug is a no-op (a stale picker click).
func (c *PrereviewController) PickPrompt(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if state.SelectedFile == "" {
		return state, fmt.Errorf("pickPrompt: no file selected")
	}
	slug := ctx.GetString("slug")
	var body string
	for _, p := range state.Prompts {
		if p.Slug == slug {
			body = p.Body
			break
		}
	}
	if body == "" {
		return state, nil // unknown/stale slug
	}
	resetToFileComment(&state)
	state.DraftBody = body // pre-fill the composer with the prompt
	return state, nil
}

// SelectImageArea opens the composer in "area" mode with a captured
// rectangle. Fired by the client's lvt-fx:area-select directive on
// pointerup with the final 0..1-fraction coords. Sets CommentMode +
// SelectionArea and clears any pending line/file selection so the
// area composer is the only one visible.
func (c *PrereviewController) SelectImageArea(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if state.SelectedFile == "" {
		return state, fmt.Errorf("selectImageArea: no file selected")
	}
	area := Area{
		X: ctx.GetFloat("x"),
		Y: ctx.GetFloat("y"),
		W: ctx.GetFloat("w"),
		H: ctx.GetFloat("h"),
	}
	if !validUnitRect(area) {
		return state, fmt.Errorf("selectImageArea: out-of-range rectangle %+v", area)
	}
	state.CommentMode = commentKindArea
	state.SelectionArea = area
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.EditingCommentID = ""
	state.ReanchorCommentID = ""
	state.MoreMenuOpen = false
	// Capturing a region disarms the overlay so the composer is reachable.
	state.RegionSelectArmed = false
	return state, nil
}

// validUnitRect reports whether a is a non-empty rectangle fully inside the
// unit square (0..1 fractions) — the shared shape for kind=area (image) and
// kind=region (live page) selections. The client clamps before dispatch;
// this rejects a buggy/malicious out-of-range payload rather than persisting
// a nonsense rectangle. The 1.0001 slack absorbs float rounding at the edge.
func validUnitRect(a Area) bool {
	return a.W > 0 && a.H > 0 &&
		a.X >= 0 && a.X <= 1 && a.Y >= 0 && a.Y <= 1 &&
		a.X+a.W <= 1.0001 && a.Y+a.H <= 1.0001
}

// SetProxyURL records the proxied page the iframe is currently showing,
// reported by the injected beacon on load / pushState / popstate. It scopes
// which region annotations render (RegionComments) and where new ones
// anchor. Navigating to a different page drops any in-progress region
// composer — it belonged to the page we just left.
func (c *PrereviewController) SetProxyURL(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	url := ctx.GetString("url")
	if url == "" || url == state.CurrentURL {
		return state, nil
	}
	state.CurrentURL = url
	if state.CommentMode == commentKindRegion {
		state.CommentMode = ""
		state.SelectionArea = Area{}
		state.DraftBody = ""
		state.EditingCommentID = ""
	}
	return state, nil
}

// SelectRegion arms the composer for a region annotation on the current
// proxied page: a rectangle in document-fraction coordinates (computed
// client-side from the drag box + the beacon's scroll/document metrics),
// dispatched from the overlay's lvt-fx:region-select="selectRegion"
// data-surface="page" directive. Mirrors SelectImageArea but anchors to
// CurrentURL instead of a file.
func (c *PrereviewController) SelectRegion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if state.CurrentURL == "" {
		return state, fmt.Errorf("selectRegion: no current page")
	}
	area := Area{
		X: ctx.GetFloat("x"),
		Y: ctx.GetFloat("y"),
		W: ctx.GetFloat("w"),
		H: ctx.GetFloat("h"),
	}
	if !validUnitRect(area) {
		return state, fmt.Errorf("selectRegion: out-of-range rectangle %+v", area)
	}
	state.CommentMode = commentKindRegion
	state.SelectionArea = area
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.EditingCommentID = ""
	state.ReanchorCommentID = ""
	state.MoreMenuOpen = false
	// Capturing a region disarms the overlay so the live page is interactive
	// again (scroll / click / navigate) and the composer is reachable.
	state.RegionSelectArmed = false
	return state, nil
}
