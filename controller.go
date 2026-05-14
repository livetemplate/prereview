package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
)

// PrereviewController holds singleton dependencies. State is cloned per
// session by the framework — never store per-user data on the controller.
type PrereviewController struct {
	// RepoPath, Base, CSVPath, DonePath are set once by main.go and are
	// read-only. CSVWriter is a goroutine-safe serializer over CSVPath.
	RepoPath  string
	Base      string
	CSVPath   string
	DonePath  string
	CSVWriter *csv.Writer
}

// Mount runs on every HTTP GET and WebSocket connect. It rebuilds the file
// list from `git diff` so the user sees current state without restarting
// the binary.
func (c *PrereviewController) Mount(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	files, err := gitdiff.ListFiles(c.RepoPath, c.Base)
	if err != nil {
		return state, fmt.Errorf("list files: %w", err)
	}
	state.Files = files
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	state.CSVPath = c.CSVPath

	// Eager-load the diff for whichever file was previously selected so a
	// page refresh keeps the right pane populated.
	if state.SelectedFile != "" {
		if !fileInList(state.Files, state.SelectedFile) {
			// The previously-selected file disappeared (e.g. user discarded changes).
			// Reset so we don't render a stale viewer.
			state.SelectedFile = ""
			state.CurrentDiff = nil
		} else {
			diff, err := gitdiff.LoadDiff(c.RepoPath, c.Base, state.SelectedFile)
			if err != nil {
				slog.Warn("load diff in mount", "path", state.SelectedFile, "err", err)
				state.CurrentDiff = nil
			} else {
				state.CurrentDiff = diff
			}
		}
	}
	return state, nil
}

// SelectFile loads the diff for the file path supplied as a hidden form
// input (`name="path"`). Resets any line selection.
func (c *PrereviewController) SelectFile(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path := ctx.GetString("path")
	if path == "" {
		return state, fmt.Errorf("selectFile: missing path")
	}
	diff, err := gitdiff.LoadDiff(c.RepoPath, c.Base, path)
	if err != nil {
		return state, fmt.Errorf("load diff %s: %w", path, err)
	}
	state.SelectedFile = path
	state.CurrentDiff = diff
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	return state, nil
}

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

// ClearSelection wipes the line selection and any draft. Bound to a
// "Cancel" button next to the composer.
func (c *PrereviewController) ClearSelection(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.DraftBody = ""
	return state, nil
}

// SaveDraft updates DraftBody as the user types. Bound to the textarea's
// change event so the draft survives reconnect (state has lvt:"persist").
func (c *PrereviewController) SaveDraft(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.DraftBody = ctx.GetString("body")
	return state, nil
}

// AddComment validates body+selection, appends a Comment, writes the CSV
// atomically, and clears selection + draft for the next round.
func (c *PrereviewController) AddComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	body := strings.TrimSpace(ctx.GetString("body"))
	if body == "" {
		return state, fmt.Errorf("comment body cannot be empty")
	}
	if state.SelectedFile == "" {
		return state, fmt.Errorf("no file selected")
	}
	if state.SelectionAnchor == 0 {
		return state, fmt.Errorf("no line selected")
	}

	from, to := state.SelectionAnchor, state.SelectionEnd
	if from > to {
		from, to = to, from
	}

	cm := Comment{
		ID:       newCommentID(),
		File:     state.SelectedFile,
		FromLine: from,
		ToLine:   to,
		Side:     state.SelectionSide,
		Body:     body,
		Created:  time.Now().UTC(),
	}
	state.Comments = append(state.Comments, cm)

	if err := c.persist(state.Comments); err != nil {
		// Roll back the in-memory append so state stays consistent with disk.
		state.Comments = state.Comments[:len(state.Comments)-1]
		return state, fmt.Errorf("persist comment: %w", err)
	}

	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.DraftBody = ""
	state.LastSaved = time.Now().Format("15:04:05")
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	return state, nil
}

// EditComment loads the named comment back into the composer (draft body +
// selection range) so the user can rewrite it. The original Comment stays
// in the slice until the new submission, at which point AddComment will
// have appended a fresh one — Edit's job is to seed the composer, not to
// mutate in place. (Saves us a separate code path for the actual update.)
//
// On submission, the OLD comment is removed by ID inside this same action
// when called with `commit=true`. Two-phase keeps the UI predictable:
// click Edit → seeds composer → optionally edit → click "Update" which
// calls EditComment with the form data + original ID.
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
	diff, err := gitdiff.LoadDiff(c.RepoPath, c.Base, cm.File)
	if err == nil {
		state.CurrentDiff = diff
	}
	state.SelectionAnchor = cm.FromLine
	state.SelectionEnd = cm.ToLine
	state.SelectionSide = cm.Side
	state.DraftBody = cm.Body

	// Remove the comment being edited — submission of the composer will
	// re-add it via AddComment with the (possibly updated) body. This
	// avoids a separate UpdateComment code path and keeps CSV writes idempotent.
	state.Comments = slices.Delete(state.Comments, idx, idx+1)
	if err := c.persist(state.Comments); err != nil {
		return state, fmt.Errorf("persist after edit: %w", err)
	}
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	return state, nil
}

// DeleteComment removes the named comment and rewrites the CSV.
func (c *PrereviewController) DeleteComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("deleteComment: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("deleteComment: id %s not found", id)
	}
	state.Comments = slices.Delete(state.Comments, idx, idx+1)
	if err := c.persist(state.Comments); err != nil {
		return state, fmt.Errorf("persist after delete: %w", err)
	}
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// Done is the "I'm finished reviewing" handoff. Writes the CSV one more
// time (defensive — should already be current), then writes the DONE
// marker AFTER the CSV is fsynced + renamed. The skill polls for the
// marker, so writing DONE before the CSV is durable would let the skill
// race and read a half-written file.
//
// Server keeps running afterwards so the user can keep editing.
func (c *PrereviewController) Done(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if err := c.persist(state.Comments); err != nil {
		return state, fmt.Errorf("final csv write: %w", err)
	}
	if err := writeDoneMarker(c.DonePath, c.CSVPath); err != nil {
		return state, fmt.Errorf("write done marker: %w", err)
	}
	state.DoneWritten = true
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// persist converts the in-memory comments to CSV Rows and atomically
// rewrites the CSV file.
func (c *PrereviewController) persist(comments []Comment) error {
	if c.CSVWriter == nil {
		return fmt.Errorf("csv writer not configured")
	}
	rows := make([]csv.Row, 0, len(comments))
	for _, cm := range comments {
		rows = append(rows, csv.Row{
			ID:        cm.ID,
			File:      cm.File,
			FromLine:  cm.FromLine,
			ToLine:    cm.ToLine,
			Side:      cm.Side,
			Body:      cm.Body,
			CreatedAt: cm.Created,
		})
	}
	return c.CSVWriter.Write(rows)
}

// writeDoneMarker writes csvPath into donePath atomically, so a skill that
// reads donePath gets a complete path string (no truncation race).
func writeDoneMarker(donePath, csvPath string) error {
	dir := filepath.Dir(donePath)
	tmp, err := os.CreateTemp(dir, ".prereview-done-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(csvPath + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, donePath); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

// fileInList reports whether path appears among entries.
func fileInList(entries []gitdiff.FileEntry, path string) bool {
	for _, e := range entries {
		if e.Path == path {
			return true
		}
	}
	return false
}

// annotateCommentCounts fills FileEntry.CommentCount from the comments slice.
// Called by Mount each refresh so the left-pane badges stay in sync.
func annotateCommentCounts(files []gitdiff.FileEntry, comments []Comment) []gitdiff.FileEntry {
	counts := map[string]int{}
	for _, c := range comments {
		counts[c.File]++
	}
	for i := range files {
		files[i].CommentCount = counts[files[i].Path]
	}
	return files
}
