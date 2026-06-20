package main

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
func (c *PrereviewController) SelectLine(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	n := ctx.GetInt("line")
	if n <= 0 {
		return state, fmt.Errorf("selectLine: missing or invalid 'line'")
	}
	side := ctx.GetString("side")
	if side == "" {
		side = "new"
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
	state.SelectionAnchor = from
	state.SelectionEnd = to
	state.SelectionSide = side
	// Capturing a region disarms the overlay so scrolling returns and the
	// composer is reachable (mirror of SelectImageArea).
	state.RegionSelectArmed = false
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
	state.SelectionArea = Area{}
	state.RegionSelectArmed = false
	state.CommentMode = ""
	state.DraftBody = ""
	state.EditingCommentID = ""
	state.ReanchorCommentID = ""
	state.URLHashScrollAnchor = ""
	return state, nil
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
	state.CommentMode = commentKindFile
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.SelectionArea = Area{}
	state.RegionSelectArmed = false
	state.EditingCommentID = ""
	state.ReanchorCommentID = ""
	state.MoreMenuOpen = false
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
