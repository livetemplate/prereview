package review

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/livetemplate/livetemplate"
)

// SaveDraft updates DraftBody as the user types. Bound to the textarea's
// change event so the draft survives reconnect (state has lvt:"persist").
func (c *PrereviewController) SaveDraft(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.DraftBody = ctx.GetString("body")
	return state, nil
}

// materializeDraft turns the composer's in-progress text into a DRAFT comment
// instead of discarding it, so navigating away (switching files, jumping to a
// comment) WITHOUT clicking Save doesn't lose the note (#105/#119). It reuses the
// normal add path (any kind) then flips the new comment to Draft. A no-op when
// there's no pending text, when editing/re-anchoring an existing comment, or when
// the add can't anchor. The materialized draft is enqueue-able / deletable from
// its card; explicit Cancel (clearSelection) still discards.
//
// Called at the START of the composer-abandon navigation actions — NOT from
// clearSelection (Cancel/Esc), which the reviewer chose to keep as "discard".
func (c *PrereviewController) materializeDraft(state PrereviewState) PrereviewState {
	if state.EditingCommentID != "" || state.ReanchorCommentID != "" {
		return state // editing an existing comment — abandoning reverts, doesn't draft
	}
	body := strings.TrimSpace(state.DraftBody)
	if body == "" {
		return state
	}
	before := len(state.Comments)
	st, err := c.addCommentBody(state, body)
	if err != nil || len(st.Comments) <= before {
		return state // couldn't anchor (no live selection) — leave the composer as-is
	}
	st.Comments[len(st.Comments)-1].Draft = true
	if perr := c.persist(st.Comments); perr != nil {
		slog.Warn("materialize draft", "err", perr)
		return state
	}
	return st
}

// AddComment validates body+selection, appends a Comment, writes the CSV
// atomically, and clears selection + draft for the next round. Save enqueues
// (the comment is actionable); the draft path is materializeDraft.
func (c *PrereviewController) AddComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.addCommentBody(state, strings.TrimSpace(ctx.GetString("body")))
}

// addCommentBody is AddComment's core, parameterized on the body so
// materializeDraft can reuse the full kind-dispatch with the autosaved DraftBody
// instead of a form value.
func (c *PrereviewController) addCommentBody(state PrereviewState, body string) (PrereviewState, error) {
	if body == "" {
		return state, fmt.Errorf("comment body cannot be empty")
	}
	// Region comments (--external mode) anchor to a URL, not a file — handle
	// before the file guard below, which would otherwise reject them.
	if state.CommentMode == commentKindRegion {
		return c.addRegionComment(state, body)
	}
	if state.SelectedFile == "" {
		return state, fmt.Errorf("no file selected")
	}
	// File-level comments take a dedicated path: no line range, no
	// anchor capture, and a separate Kind tag in both the in-memory
	// Comment and the persisted CSV. Edits to existing file-level
	// comments flow through here too (EditComment sets CommentMode +
	// DraftBody + EditingCommentID).
	if state.CommentMode == commentKindFile {
		return c.addFileLevelComment(state, body)
	}
	// Image-area comments take a dedicated path: rectangle in
	// SelectionArea (set by SelectImageArea, dispatched from the
	// client's lvt-fx:area-select directive), no line range, no anchor
	// capture, kind="area" in both memory and CSV.
	if state.CommentMode == commentKindArea {
		return c.addAreaComment(state, body)
	}
	// Text (character-range) comments take a dedicated path: a line span PLUS
	// FromCol/ToCol and the selected snippet (set by SelectText). kind="text".
	if state.CommentMode == commentKindText {
		return c.addTextComment(state, body)
	}
	if state.SelectionAnchor == 0 {
		return state, fmt.Errorf("no line selected")
	}

	from, to := state.SelectionAnchor, state.SelectionEnd
	if from > to {
		from, to = to, from
	}

	// Re-anchor mode: the user picked a NEW location for an outdated
	// comment. Re-point it and re-capture its anchor at the chosen
	// range; this is the sanctioned move path (Edit is hidden for
	// outdated comments). Self-contained: own persist + reset.
	if state.ReanchorCommentID != "" {
		idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == state.ReanchorCommentID })
		if idx >= 0 {
			prev := state.Comments[idx]
			state.Comments[idx].Body = body
			state.Comments[idx].FromLine = from
			state.Comments[idx].ToLine = to
			state.Comments[idx].Side = state.SelectionSide
			state.Comments[idx].Anchor = captureAnchor(state.CurrentDiff, from, to, state.SelectionSide)
			state.Comments[idx].AnchorStatus = anchorOK
			if err := c.persist(state.Comments); err != nil {
				state.Comments[idx] = prev
				return state, fmt.Errorf("persist re-anchor: %w", err)
			}
			state.SelectionAnchor = 0
			state.SelectionEnd = 0
			state.SelectionSide = ""
			state.DraftBody = ""
			state.ReanchorCommentID = ""
			state.EditingCommentID = ""
			state.LastDeletedComment = nil
			state.LastSaved = time.Now().Format("15:04:05")
			state.Files = annotateCommentCounts(state.Files, state.Comments)
			return state, nil
		}
		// Comment vanished (session race) — drop the flag and fall
		// through to the normal add path rather than lose the body.
		state.ReanchorCommentID = ""
	}

	// Edit-mode: state.EditingCommentID was set by EditComment when the
	// user clicked Edit on an existing comment. Update that comment in
	// place rather than appending a new one. ID, Created, and Resolved
	// stay the same; body, line range, and side may change.
	//
	// If the user concurrently deleted the comment we're "editing"
	// (e.g., a session race), the lookup misses and we fall through to
	// the append path — better to surface the change as a new comment
	// than to lose the body the user typed.
	var rollback func()
	if state.EditingCommentID != "" {
		idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == state.EditingCommentID })
		if idx >= 0 {
			prev := state.Comments[idx]
			state.Comments[idx].Body = body
			if prev.AnchorStatus == anchorOutdated {
				// The stored range points at unrelated content (the
				// original is gone). Re-capturing here would silently
				// re-anchor the comment to whatever now sits there and
				// stamp it ok. Only the body changes; the user must use
				// Re-anchor (not Edit) to re-place it. The UI also hides
				// Edit for outdated comments — this is defense in depth.
			} else {
				state.Comments[idx].FromLine = from
				state.Comments[idx].ToLine = to
				state.Comments[idx].Side = state.SelectionSide
				// Re-capture at the (possibly new) range — else a later
				// relocate would drag the edited comment back.
				state.Comments[idx].Anchor = captureAnchor(state.CurrentDiff, from, to, state.SelectionSide)
				state.Comments[idx].AnchorStatus = anchorOK
			}
			rollback = func() { state.Comments[idx] = prev }
		}
	}
	if rollback == nil {
		cm := Comment{
			ID:           newCommentID(),
			File:         state.SelectedFile,
			FromLine:     from,
			ToLine:       to,
			Side:         state.SelectionSide,
			Body:         body,
			Created:      time.Now().UTC(),
			Anchor:       captureAnchor(state.CurrentDiff, from, to, state.SelectionSide),
			AnchorStatus: anchorOK,
		}
		state.Comments = append(state.Comments, cm)
		// Land scroll + focus on the just-saved comment so a keyboard user
		// lands on it after the composer closes (see commentCardFull/Simple).
		// Clear any pending heading target — the two scroll intents are
		// mutually exclusive (else the comment scroll fights a stale heading).
		state.ScrollToCommentID = cm.ID
		state.ScrollToHeadingID = ""
		rollback = func() { state.Comments = state.Comments[:len(state.Comments)-1] }
	}

	if err := c.persist(state.Comments); err != nil {
		// Roll back so memory stays consistent with disk.
		rollback()
		return state, fmt.Errorf("persist comment: %w", err)
	}

	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.DraftBody = ""
	state.EditingCommentID = ""
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	return state, nil
}

// addFileLevelComment is the AddComment branch for whole-file comments.
// Mirrors the line-comment path's append-and-persist shape but skips
// every line-range / anchor concern. Edits to an existing file-level
// comment update in place when EditingCommentID is set — same rule as
// the line-comment path.
func (c *PrereviewController) addFileLevelComment(state PrereviewState, body string) (PrereviewState, error) {
	var rollback func()
	if state.EditingCommentID != "" {
		idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == state.EditingCommentID })
		if idx >= 0 {
			prev := state.Comments[idx]
			state.Comments[idx].Body = body
			rollback = func() { state.Comments[idx] = prev }
		}
	}
	if rollback == nil {
		cm := Comment{
			ID:      newCommentID(),
			File:    state.SelectedFile,
			Body:    body,
			Created: time.Now().UTC(),
			Kind:    commentKindFile,
			// FromLine/ToLine/Side/Anchor/AnchorStatus stay zero — the
			// "no anchor to relocate" contract is what IsFileLevel()
			// keys off of in relocate() and the UI ranges.
		}
		state.Comments = append(state.Comments, cm)
		// Land scroll + focus on the just-saved comment so a keyboard user
		// lands on it after the composer closes (see commentCardFull/Simple).
		// Clear any pending heading target — the two scroll intents are
		// mutually exclusive (else the comment scroll fights a stale heading).
		state.ScrollToCommentID = cm.ID
		state.ScrollToHeadingID = ""
		rollback = func() { state.Comments = state.Comments[:len(state.Comments)-1] }
	}

	if err := c.persist(state.Comments); err != nil {
		rollback()
		return state, fmt.Errorf("persist file-level comment: %w", err)
	}

	state.CommentMode = ""
	state.DraftBody = ""
	state.EditingCommentID = ""
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	return state, nil
}

// addAreaComment is the AddComment branch for image-area annotations.
// Shape matches addFileLevelComment — append-and-persist with a kind
// tag and no anchor — but also carries the SelectionArea rectangle.
// Edits to an existing area comment update body + rectangle in place
// when EditingCommentID is set.
func (c *PrereviewController) addAreaComment(state PrereviewState, body string) (PrereviewState, error) {
	if state.SelectionArea.Empty() {
		return state, fmt.Errorf("no image area selected")
	}
	var rollback func()
	if state.EditingCommentID != "" {
		idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == state.EditingCommentID })
		if idx >= 0 {
			prev := state.Comments[idx]
			state.Comments[idx].Body = body
			state.Comments[idx].Area = state.SelectionArea
			rollback = func() { state.Comments[idx] = prev }
		}
	}
	if rollback == nil {
		cm := Comment{
			ID:      newCommentID(),
			File:    state.SelectedFile,
			Body:    body,
			Created: time.Now().UTC(),
			Kind:    commentKindArea,
			Area:    state.SelectionArea,
			// FromLine/ToLine/Side/Anchor/AnchorStatus stay zero — the
			// "no anchor to relocate" contract is what IsAreaLevel()
			// keys off of in relocate() and the UI ranges.
		}
		state.Comments = append(state.Comments, cm)
		// Land scroll + focus on the just-saved comment so a keyboard user
		// lands on it after the composer closes (see commentCardFull/Simple).
		// Clear any pending heading target — the two scroll intents are
		// mutually exclusive (else the comment scroll fights a stale heading).
		state.ScrollToCommentID = cm.ID
		state.ScrollToHeadingID = ""
		rollback = func() { state.Comments = state.Comments[:len(state.Comments)-1] }
	}

	if err := c.persist(state.Comments); err != nil {
		rollback()
		return state, fmt.Errorf("persist area comment: %w", err)
	}

	state.CommentMode = ""
	state.SelectionArea = Area{}
	state.DraftBody = ""
	state.EditingCommentID = ""
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	return state, nil
}

// addRegionComment persists a kind=region annotation: a rectangle
// (document-fraction Area, set by SelectRegion) on the current proxied page
// (CurrentURL), with no file / line / anchor. Mirrors addAreaComment; the
// only differences are the URL anchor and Kind. Edits (EditingCommentID)
// update body + rectangle in place.
func (c *PrereviewController) addRegionComment(state PrereviewState, body string) (PrereviewState, error) {
	if state.SelectionArea.Empty() {
		return state, fmt.Errorf("no region selected")
	}
	if state.CurrentURL == "" {
		return state, fmt.Errorf("no current page")
	}
	var rollback func()
	if state.EditingCommentID != "" {
		idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == state.EditingCommentID })
		if idx >= 0 {
			prev := state.Comments[idx]
			state.Comments[idx].Body = body
			state.Comments[idx].Area = state.SelectionArea
			rollback = func() { state.Comments[idx] = prev }
		}
	}
	if rollback == nil {
		cm := Comment{
			ID:      newCommentID(),
			Body:    body,
			Created: time.Now().UTC(),
			Kind:    commentKindRegion,
			Area:    state.SelectionArea,
			URL:     state.CurrentURL,
			// File/FromLine/ToLine/Side/Anchor stay zero — IsRegionLevel()
			// keys off the "no anchor to relocate" contract.
		}
		state.Comments = append(state.Comments, cm)
		// Land scroll + focus on the just-saved comment so a keyboard user
		// lands on it after the composer closes (see commentCardFull/Simple).
		// Clear any pending heading target — the two scroll intents are
		// mutually exclusive (else the comment scroll fights a stale heading).
		state.ScrollToCommentID = cm.ID
		state.ScrollToHeadingID = ""
		rollback = func() { state.Comments = state.Comments[:len(state.Comments)-1] }
	}

	if err := c.persist(state.Comments); err != nil {
		rollback()
		return state, fmt.Errorf("persist region comment: %w", err)
	}

	state.CommentMode = ""
	state.SelectionArea = Area{}
	state.DraftBody = ""
	state.EditingCommentID = ""
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// addTextComment is the AddComment branch for character-range (kind=text)
// comments. It carries a line span like a line comment — so it reuses the
// line-anchor drift machinery (Anchor.Text is the whole-line join, relocate
// shifts the lines) — plus FromCol/ToCol and the exact selected substring
// (Anchor.Snippet) that pin the sub-line range. Edits update body + range +
// columns in place when EditingCommentID is set.
func (c *PrereviewController) addTextComment(state PrereviewState, body string) (PrereviewState, error) {
	if state.SelectionAnchor == 0 || state.SelectionText == "" {
		return state, fmt.Errorf("no text selected")
	}
	// Client dispatches doc-ordered (fromLine<=toLine); keep the pairing.
	from, to := state.SelectionAnchor, state.SelectionEnd
	side := state.SelectionSide
	anchor := captureAnchor(state.CurrentDiff, from, to, side)
	anchor.Snippet = state.SelectionText

	var rollback func()
	if state.EditingCommentID != "" {
		idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == state.EditingCommentID })
		if idx >= 0 {
			prev := state.Comments[idx]
			state.Comments[idx].Body = body
			state.Comments[idx].FromLine = from
			state.Comments[idx].ToLine = to
			state.Comments[idx].FromCol = state.SelectionFromCol
			state.Comments[idx].ToCol = state.SelectionToCol
			state.Comments[idx].Side = side
			state.Comments[idx].Anchor = anchor
			state.Comments[idx].AnchorStatus = anchorOK
			rollback = func() { state.Comments[idx] = prev }
		}
	}
	if rollback == nil {
		cm := Comment{
			ID:           newCommentID(),
			File:         state.SelectedFile,
			FromLine:     from,
			ToLine:       to,
			FromCol:      state.SelectionFromCol,
			ToCol:        state.SelectionToCol,
			Side:         side,
			Body:         body,
			Created:      time.Now().UTC(),
			Kind:         commentKindText,
			Anchor:       anchor,
			AnchorStatus: anchorOK,
		}
		state.Comments = append(state.Comments, cm)
		state.ScrollToCommentID = cm.ID
		state.ScrollToHeadingID = ""
		rollback = func() { state.Comments = state.Comments[:len(state.Comments)-1] }
	}

	if err := c.persist(state.Comments); err != nil {
		rollback()
		return state, fmt.Errorf("persist text comment: %w", err)
	}

	state.CommentMode = ""
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.SelectionFromCol = 0
	state.SelectionToCol = 0
	state.SelectionText = ""
	state.DraftBody = ""
	state.EditingCommentID = ""
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	return state, nil
}

// EditComment seeds the composer with an existing comment's body +
// line range so the user can rewrite it. The original comment stays in
// state.Comments — AddComment detects EditingCommentID and updates
// in place rather than appending. This keeps Cancel non-destructive:
// if the user opens Edit and changes their mind, the original survives.
func (c *PrereviewController) EditComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("editComment: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("editComment: id %s not found", id)
	}
	cm := state.Comments[idx]
	state.SelectedFile = cm.File
	// Reload diff for the comment's file in case we're on a different one.
	// Region comments (--external) have no file, so skip the git call entirely.
	if cm.File != "" {
		if diff, err := c.loadDiffCached(state.Base, cm.File); err == nil {
			state.CurrentDiff = diff
		}
	}
	// Route the composer into the right mode based on the comment's
	// Kind — file-level lands in file-mode, area in area-mode (with
	// the saved rectangle so the pending overlay re-renders), and
	// line-anchored in the default line mode.
	switch {
	case cm.IsFileLevel():
		state.CommentMode = commentKindFile
		state.SelectionAnchor = 0
		state.SelectionEnd = 0
		state.SelectionSide = ""
		state.SelectionArea = Area{}
	case cm.IsAreaLevel():
		state.CommentMode = commentKindArea
		state.SelectionArea = cm.Area
		state.SelectionAnchor = 0
		state.SelectionEnd = 0
		state.SelectionSide = ""
	case cm.IsRegionLevel():
		state.CommentMode = commentKindRegion
		state.SelectionArea = cm.Area
		// Scope to the comment's page so addRegionComment writes it back to
		// the right URL even if the iframe is currently on another page.
		state.CurrentURL = cm.URL
		state.SelectionAnchor = 0
		state.SelectionEnd = 0
		state.SelectionSide = ""
	default:
		state.CommentMode = ""
		state.SelectionAnchor = cm.FromLine
		state.SelectionEnd = cm.ToLine
		state.SelectionSide = cm.Side
		state.SelectionArea = Area{}
	}
	state.DraftBody = cm.Body
	state.EditingCommentID = cm.ID
	state.LastDeletedComment = nil
	// The composer only renders in the diff branch; when Edit is invoked
	// from the all-comments view this drops back into the file so the
	// edit composer is actually visible (no-op when already in the diff).
	state.ShowAllComments = false
	return state, nil
}

// ReanchorComment starts re-placing an outdated comment: it jumps to
// the comment's file and arms ReanchorCommentID, but deliberately does
// NOT pre-seed the (stale) line selection — the user must pick the new
// location. The body is preserved in the composer. The next Save
// (AddComment, ReanchorCommentID branch) re-points the comment and
// re-captures its content anchor. This is the only sanctioned way to
// move an outdated comment; Edit is hidden for outdated comments
// precisely so it can't silently re-anchor against stale content.
func (c *PrereviewController) ReanchorComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("reanchorComment: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("reanchorComment: id %s not found", id)
	}
	cm := state.Comments[idx]
	state.SelectedFile = cm.File
	if diff, err := c.loadDiffCached(state.Base, cm.File); err == nil {
		state.CurrentDiff = diff
	}
	// No pre-seeded selection — the whole point is to choose a new spot.
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = cm.Side
	state.DraftBody = cm.Body
	state.ReanchorCommentID = cm.ID
	state.EditingCommentID = ""
	state.LastDeletedComment = nil
	state.ShowAllComments = false
	return state, nil
}

// DeleteComment removes the named comment, rewrites the CSV, and stashes
// the deleted comment in state.LastDeletedComment so the user can undo
// for the remainder of the session (or until another mutation).
func (c *PrereviewController) DeleteComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("deleteComment: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("deleteComment: id %s not found", id)
	}
	deleted := state.Comments[idx]
	state.Comments = slices.Delete(state.Comments, idx, idx+1)
	if err := c.persist(state.Comments); err != nil {
		return state, fmt.Errorf("persist after delete: %w", err)
	}
	state.LastDeletedComment = &deleted
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// ToggleResolved flips the Resolved flag on the named comment and rewrites
// the CSV. Unlike DeleteComment, this keeps the comment as a historical
// record; the skill should treat resolved comments as "addressed" and
// only act on unresolved ones.
func (c *PrereviewController) ToggleResolved(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("toggleResolved: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("toggleResolved: id %s not found", id)
	}
	state.Comments[idx].Resolved = !state.Comments[idx].Resolved
	if err := c.persist(state.Comments); err != nil {
		// Roll back so disk and memory match.
		state.Comments[idx].Resolved = !state.Comments[idx].Resolved
		return state, fmt.Errorf("persist after toggle resolved: %w", err)
	}
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// setCommentDraft flips a comment's Draft flag by id and persists, rolling back
// on a write error so disk and memory stay in sync. Shared by EnqueueComment
// (draft→queued) and MoveToDraft (queued→draft) — the two moves in the draft
// lifecycle (#119).
func (c *PrereviewController) setCommentDraft(state PrereviewState, id string, draft bool) (PrereviewState, error) {
	if id == "" {
		return state, fmt.Errorf("setCommentDraft: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("setCommentDraft: id %s not found", id)
	}
	if state.Comments[idx].Draft == draft {
		return state, nil // already in the requested state
	}
	prev := state.Comments[idx].Draft
	state.Comments[idx].Draft = draft
	if err := c.persist(state.Comments); err != nil {
		state.Comments[idx].Draft = prev
		return state, fmt.Errorf("persist after draft toggle: %w", err)
	}
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// EnqueueComment moves a draft comment into the queue (Draft=false) so the agent
// will act on it. Bound to the per-card "Enqueue" action.
func (c *PrereviewController) EnqueueComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.setCommentDraft(state, ctx.GetString("id"), false)
}

// MoveToDraft pulls a queued comment back to a draft (Draft=true) so it's held
// out of the agent's snapshot while the reviewer keeps working on it.
func (c *PrereviewController) MoveToDraft(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.setCommentDraft(state, ctx.GetString("id"), true)
}

// ReenqueueComment moves a "done" comment back to "queued" (#119): the agent
// marked it worked-on but the reviewer wants it redone. Records an un-processed
// tombstone so the comment leaves the done bucket and re-arms the snapshot.
func (c *PrereviewController) ReenqueueComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if err := c.reenqueueComment(&state, ctx.GetString("id")); err != nil {
		return state, err
	}
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// HideComment individually re-hides a RESOLVED comment (issue #88): it stays out
// of the diff + overview even while "Show resolved" is on, without turning the
// whole resolved group back off. A view-only flag — the comment is untouched
// otherwise (still resolved, still in the CSV, already excluded from the
// handoff). Recovered in bulk via UnhideAllResolved. Mirrors ToggleResolved's
// persist-and-rollback shape.
func (c *PrereviewController) HideComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("hideComment: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("hideComment: id %s not found", id)
	}
	state.Comments[idx].Hidden = true
	if err := c.persist(state.Comments); err != nil {
		state.Comments[idx].Hidden = false // roll back so disk and memory match
		return state, fmt.Errorf("persist after hide: %w", err)
	}
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// UnhideAllResolved clears the individual-hide flag on every comment at once —
// the recovery for HideComment, surfaced as "Unhide N" next to the Show-resolved
// toggle. Rewrites the CSV; rolls back the in-memory flags on a write error so
// disk and memory stay in sync.
func (c *PrereviewController) UnhideAllResolved(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	hiddenIdx := make([]int, 0)
	for i := range state.Comments {
		if state.Comments[i].Hidden {
			state.Comments[i].Hidden = false
			hiddenIdx = append(hiddenIdx, i)
		}
	}
	if len(hiddenIdx) == 0 {
		return state, nil
	}
	if err := c.persist(state.Comments); err != nil {
		for _, i := range hiddenIdx {
			state.Comments[i].Hidden = true // roll back
		}
		return state, fmt.Errorf("persist after unhide all: %w", err)
	}
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// UndoDelete restores the most recently deleted comment to state.Comments
// and rewrites the CSV. No-op if LastDeletedComment is nil (the undo
// affordance shouldn't even render in that case, but defending in depth).
func (c *PrereviewController) UndoDelete(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if state.LastDeletedComment == nil {
		return state, nil
	}
	state.Comments = append(state.Comments, *state.LastDeletedComment)
	if err := c.persist(state.Comments); err != nil {
		// Don't clear LastDeletedComment so the user can try again.
		return state, fmt.Errorf("persist after undo: %w", err)
	}
	state.LastDeletedComment = nil
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// DismissUndo clears the undo affordance WITHOUT restoring the comment — the
// deletion stands. This backs the undo toast's ✕ button, the manual
// counterpart to UndoDelete's restore. Mirror of ClearFlash: a one-field clear
// with no persistence (the CSV already reflects the deletion).
func (c *PrereviewController) DismissUndo(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.LastDeletedComment = nil
	return state, nil
}
