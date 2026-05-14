package main

import (
	"fmt"
	"log/slog"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
)

// PrereviewController holds singleton dependencies. State is cloned per
// session by the framework — never store per-user data on the controller.
type PrereviewController struct {
	// RepoPath and Base are set once by main.go and are read-only.
	RepoPath string
	Base     string
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
