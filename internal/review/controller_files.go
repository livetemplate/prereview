package review

import (
	"fmt"
	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
	"log/slog"
	"strings"
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
		state.CurrentDiff = nil
		state.FileDrawerOpen = true
	} else if state.SelectedFile != "" {
		// Same file is still in the diff — reload it against the new base.
		diff, err := c.loadDiffCached(state.Base, state.SelectedFile)
		if err != nil {
			slog.Warn("reload diff after SetBase", "path", state.SelectedFile, "err", err)
			state.CurrentDiff = nil
		} else {
			state.CurrentDiff = diff
		}
	}
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
	diff, err := c.loadDiffCached(state.Base, path)
	if err != nil {
		return state, fmt.Errorf("load diff %s: %w", path, err)
	}
	state.SelectedFile = path
	state.CurrentDiff = diff
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.SelectionArea = Area{}    // pending image rectangle from the prior file is cancelled
	state.RegionSelectArmed = false // the region-select overlay is per-preview; disarm on file switch
	state.CommentMode = ""          // any file-level / area composer from the prior file is cancelled
	state.URLHashScrollAnchor = ""  // anchor target was for the previous file; let the new file pick its own
	state.FileDrawerOpen = false
	// Picking a file from the drawer while the all-comments view is
	// open implies "leave this overview, go look at that file" — same
	// intent as the Back button on the all-comments view. Without this
	// the user would land on the all-comments view with a freshly-
	// selected (but invisible) file behind it.
	state.ShowAllComments = false
	c.relocateSelected(&state)
	return state, nil
}

// ToggleFiles flips the mobile file-drawer's open state. Bound to the
// hamburger button and to the drawer's backdrop "close" form.
func (c *PrereviewController) ToggleFiles(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileDrawerOpen = !state.FileDrawerOpen
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
	state.SelectedFile = path
	state.CurrentDiff = diff
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.LastDeletedComment = nil
	state.EditingCommentID = ""
	c.relocateSelected(&state)
	return state, nil
}

// NextComment jumps to the next comment in the all-comments order, wrapping
// from the last back to the first. Keyboard counterpart to clicking a comment
// in the overview.
func (c *PrereviewController) NextComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.stepComment(state, +1)
}

// PrevComment jumps to the previous comment, wrapping from the first to the last.
func (c *PrereviewController) PrevComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.stepComment(state, -1)
}

// stepComment walks state.VisibleComments() (same order as the all-comments
// overview) relative to the last-jumped-to comment (ScrollToCommentID),
// switching files when needed and scrolling the target into view — mirroring
// JumpToComment, but stepping by position instead of by id. No-op when there
// are no visible comments.
func (c *PrereviewController) stepComment(state PrereviewState, delta int) (PrereviewState, error) {
	list := state.VisibleComments()
	if len(list) == 0 {
		return state, nil
	}
	cur := -1
	for i, cm := range list {
		if cm.ID == state.ScrollToCommentID {
			cur = i
			break
		}
	}
	n := len(list)
	var next int
	if cur == -1 {
		// Nothing focused yet: Next starts at the first comment, Prev at the last.
		if delta > 0 {
			next = 0
		} else {
			next = n - 1
		}
	} else {
		next = ((cur+delta)%n + n) % n
	}
	target := list[next]
	if target.File != state.SelectedFile {
		diff, err := c.loadDiffCached(state.Base, target.File)
		if err != nil {
			return state, fmt.Errorf("load diff %s: %w", target.File, err)
		}
		state.SelectedFile = target.File
		state.CurrentDiff = diff
	}
	state.ShowAllComments = false
	state.ScrollToCommentID = target.ID
	return state, nil
}

// CloseFiles unconditionally hides the file drawer. Distinct from
// ToggleFiles because the backdrop tap should only close, never open.
func (c *PrereviewController) CloseFiles(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileDrawerOpen = false
	return state, nil
}

// ToggleViewed flips the "reviewed" flag for the file passed via the hidden
// `path` input. Bound to a per-file checkbox/button in the drawer.
func (c *PrereviewController) ToggleViewed(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path := ctx.GetString("path")
	if path == "" {
		return state, fmt.Errorf("toggleViewed: missing path")
	}
	if state.ViewedFiles == nil {
		state.ViewedFiles = map[string]bool{}
	}
	if state.ViewedFiles[path] {
		delete(state.ViewedFiles, path)
	} else {
		state.ViewedFiles[path] = true
	}
	return state, nil
}

// SetFileFilter updates the search filter applied to the file drawer.
// Bound to the search input via lvt-on:input with a debounce modifier.
func (c *PrereviewController) SetFileFilter(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileFilter = ctx.GetString("filter")
	return state, nil
}
