package review

import (
	"fmt"
	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
	"log/slog"
	"strings"
	"time"
)

// SetBase changes the comparison ref. The picker is a dropdown of refs
// we enumerated this Mount (HEAD~N presets + local/remote branches), so
// the value is already a valid ref. The rev-parse check stays as cheap
// defense against a race (a branch deleted between Mount and select);
// on a miss we just no-op and keep the current base.
//
// On success, rebuilds the file list against the new base. If the
// previously selected file no longer exists in the new file list,
// SelectedFile is cleared.
func (c *PrereviewController) SetBase(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.NoGit {
		// No refs in no-git mode; the picker is hidden. Guard anyway so a
		// stale persisted client can't shell git for a meaningless ref.
		return state, nil
	}
	ref := strings.TrimSpace(ctx.GetString("ref"))
	if ref == "" || !gitdiff.IsValidRef(c.RepoPath, ref) {
		return state, nil
	}
	state.Base = ref

	files, err := gitdiff.ListFiles(c.RepoPath, state.Base)
	if err != nil {
		return state, fmt.Errorf("list files for base %q: %w", ref, err)
	}
	state.Files = annotateCommentCounts(files, state.Comments)

	// If the previously selected file isn't in the new diff range, reset
	// so we don't render a stale viewer. Drawer reopens so the user can
	// pick from the new file list.
	if state.SelectedFile != "" && !fileInList(state.Files, state.SelectedFile) {
		state.SelectedFile = ""
		c.showDiff(&state, nil)
		state.FileDrawerOpen = true
	} else if state.SelectedFile != "" {
		// Same file is still in the diff — reload it against the new base.
		diff, err := c.loadDiffCached(state.Base, state.SelectedFile)
		if err != nil {
			slog.Warn("reload diff after SetBase", "path", state.SelectedFile, "err", err)
			c.showDiff(&state, nil)
		} else {
			c.showDiff(&state, diff)
		}
	}
	c.applyReviewChecks(&state) // a new base is a new set of changed files
	return state, nil
}

// SelectFile loads the diff for the file path supplied as a hidden form
// input (`name="path"`). Resets any line selection. Auto-closes the
// mobile file drawer so tapping a file goes straight to the diff —
// the user doesn't have to also close the drawer manually.
func (c *PrereviewController) SelectFile(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path := ctx.GetString("path")
	if path == "" {
		return state, fmt.Errorf("selectFile: missing path")
	}
	// Keep any unsaved composer text as a draft before leaving this file (#105) —
	// navigating away isn't clicking Save, so the note shouldn't be lost.
	state = c.materializeDraft(state)
	diff, err := c.loadDiffCached(state.Base, path)
	if err != nil {
		return state, fmt.Errorf("load diff %s: %w", path, err)
	}
	c.enterFile(&state, path, diff)
	// #128: re-open at the last read location — stamp a one-shot scroll on the
	// line the reviewer last had at the top of the viewport for this file.
	state.ScrollToReadKey = state.LastReadTopKey[path]
	return state, nil
}

// enterFile makes path (with its loaded diff) the open file and drops what
// belonged to the previous one: the selection, any open composer, the cursor,
// a search reveal, the full-page views. It re-anchors the file's comments and
// refreshes its Versions panel (#183: a switch that skipped this showed the
// previous file's versions). SelectFile and the jumps (comments, suggestions,
// check findings) all switch through it; each sets its own scroll target after.
func (c *PrereviewController) enterFile(state *PrereviewState, path string, diff *gitdiff.FileDiff) {
	c.openFile(state, path, diff)
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.SelectionArea = Area{}    // pending image rectangle from the prior file is cancelled
	state.RegionSelectArmed = false // the region-select overlay is per-preview; disarm on file switch
	state.FocusedHTMLBlock = ""     // a marker tap belongs to the preview it was on
	state.CommentMode = ""          // any file-level / area composer from the prior file is cancelled
	state.URLHashScrollAnchor = ""  // anchor target was for the previous file; let the new file pick its own
	state.CursorKey = ""            // line cursor was for the previous file's lines
	state.RevealFile = ""           // a search-jump's full-file reveal was for the previous file
	state.FileDrawerOpen = false
	// Going to a file from a full-page view (all comments, quiz, Library) means
	// "leave this overview, look at that file"; otherwise the file would load
	// invisibly behind it.
	closeFullViews(state)
	c.relocateSelected(state)
	c.applyVersionList(state) // per-file version timeline (#90) — refresh for the new file
}

// openFile selects path and shows its diff on the Files surface: every path that
// opens a file for the reviewer goes through it, so in a combined session
// (#114) the file never loads hidden behind the live site. Mount's own reload
// of the selected file doesn't: a reconnect must keep the surface it had.
func (c *PrereviewController) openFile(state *PrereviewState, path string, diff *gitdiff.FileDiff) {
	state.SelectedFile = path
	c.showDiff(state, diff)
	state.LiveView = false
	c.recordLook(state)
}

// ToggleFiles flips the mobile file-drawer's open state. Bound to the
// hamburger button and to the drawer's backdrop "close" form.
func (c *PrereviewController) ToggleFiles(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileDrawerOpen = !state.FileDrawerOpen
	return state, nil
}

// ToggleTOC flips the desktop table-of-contents sidebar (the right column).
// Bound to the bar's contents button and the "t" shortcut. Independent of the
// file drawer (left) and of Focus mode, which hides both at once (#137). A
// durable view preference like Focus mode, so it's saved to disk to survive a
// relaunch.
func (c *PrereviewController) ToggleTOC(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.TocCollapsed = !state.TocCollapsed
	c.savePrefs(state)
	return state, nil
}

// NextFile selects the next file in state.Files relative to SelectedFile.
// Wraps to the first file from the last. If no file is selected, picks the
// first file. Falls back to no-op for an empty file list.
func (c *PrereviewController) NextFile(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.stepFile(state, +1)
}

// PrevFile selects the previous file. Wraps to the last file from the first.
func (c *PrereviewController) PrevFile(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.stepFile(state, -1)
}

func (c *PrereviewController) stepFile(state PrereviewState, delta int) (PrereviewState, error) {
	files := state.scopedFiles()
	if len(files) == 0 {
		return state, nil
	}
	state = c.materializeDraft(state) // keep unsaved composer text on file switch (#105)
	cur := -1
	for i, f := range files {
		if f.Path == state.SelectedFile {
			cur = i
			break
		}
	}
	next := cur + delta
	n := len(files)
	// Wrap. (-1+1)%n = 0 (lands on first file when nothing selected and Next).
	next = ((next % n) + n) % n
	path := files[next].Path
	diff, err := c.loadDiffCached(state.Base, path)
	if err != nil {
		return state, fmt.Errorf("load diff %s: %w", path, err)
	}
	c.openFile(&state, path, diff)
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	clearUndo(&state)
	state.EditingCommentID = ""
	state.CursorKey = "" // new file, new lines — drop the line cursor
	c.relocateSelected(&state)
	c.applyVersionList(&state) // per-file version timeline (#90)
	return state, nil
}

// CloseFiles unconditionally hides the file drawer. Distinct from
// ToggleFiles because the backdrop tap should only close, never open.
func (c *PrereviewController) CloseFiles(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileDrawerOpen = false
	return state, nil
}

// ToggleViewed marks the file passed via the hidden `path` input viewed, with
// its content now, or clears the mark (#115 B3). A file that changed since it
// was viewed is marked again with its new content. Bound to the drawer's
// per-file button.
func (c *PrereviewController) ToggleViewed(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path := ctx.GetString("path")
	if path == "" {
		return state, fmt.Errorf("toggleViewed: missing path")
	}
	marks := loadFileMarks(c.viewedPath())
	if state.ViewedFiles[path] {
		delete(marks, path)
	} else if sha := c.fileSHA(path); sha != "" {
		marks[path] = FileMark{SHA: sha, At: time.Now().UTC()}
		delete(state.SinceLook, path) // viewing it settles what changed since
		c.applySinceLook(&state)
	}
	if err := saveFileMarks(c.viewedPath(), marks); err != nil {
		return state, fmt.Errorf("toggleViewed: %w", err)
	}
	c.applyViewed(&state)
	return state, nil
}

// SetFileFilter updates the search filter applied to the file drawer.
// Bound to the search input via lvt-on:input with a debounce modifier.
func (c *PrereviewController) SetFileFilter(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileFilter = ctx.GetString("filter")
	return state, nil
}
