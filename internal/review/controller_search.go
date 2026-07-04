package review

import (
	"fmt"
	"strconv"

	"github.com/livetemplate/livetemplate"
)

// controller_search.go drives the cmd+k search palette (issue #91). The overlay
// is server-state (SearchOpen), the input is live-debounced (SetSearchQuery, like
// the drawer filter), and a result click jumps + reveals the line
// (JumpToSearchResult). See search.go for the scan and state.go for the fields.

// OpenSearch shows the search palette. Bound to the Mod+k keybinding and the
// toolbar magnifier. Closes the overflow menu so the two overlays don't stack.
// Leaves SearchQuery/SearchHits intact so reopening shows the last search.
func (c *PrereviewController) OpenSearch(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.SearchOpen = true
	state.MoreMenuOpen = false
	return state, nil
}

// CloseSearch hides the palette (the close button; Esc also closes it via
// ClearSelection). Keeps the query so a reopen resumes where the user left off.
func (c *PrereviewController) CloseSearch(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.SearchOpen = false
	return state, nil
}

// SetSearchQuery updates the live query and recomputes the hit list. Bound to the
// palette input via lvt-on:input + a debounce modifier (mirrors SetFileFilter).
func (c *PrereviewController) SetSearchQuery(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.SearchQuery = ctx.GetString("q")
	state.SearchHits = c.computeSearch(state)
	return state, nil
}

// ToggleSearchScope flips the search between the changed set (default) and all
// files, recomputing against the new scope.
func (c *PrereviewController) ToggleSearchScope(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.SearchScopeAll = !state.SearchScopeAll
	state.SearchHits = c.computeSearch(state)
	return state, nil
}

// JumpToSearchResult selects the hit's file and, for a content hit, reveals the
// full raw file and scrolls to the matched line (RevealFile + CursorKey — mirrors
// JumpToComment). A filename hit (no line) just opens the file. Closes the palette.
func (c *PrereviewController) JumpToSearchResult(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	file := ctx.GetString("file")
	if file == "" {
		return state, fmt.Errorf("jumpToSearchResult: missing file")
	}
	if file != state.SelectedFile || state.CurrentDiff == nil {
		diff, err := c.loadDiffCached(state.Base, file)
		if err != nil {
			return state, fmt.Errorf("load diff %s: %w", file, err)
		}
		state.SelectedFile = file
		state.CurrentDiff = diff
		c.applyVersionList(&state) // per-file version timeline (#90)
	}
	// A content hit carries a working-tree line (new>0) to reveal + scroll to; a
	// filename hit doesn't, so just open the file at the top in its normal view.
	oldNum, _ := strconv.Atoi(ctx.GetString("old"))
	newNum, _ := strconv.Atoi(ctx.GetString("new"))
	if newNum > 0 {
		state.CursorKey = fmt.Sprintf("L%d-%d", oldNum, newNum)
		state.RevealFile = file // full raw view so the target line row is in the DOM
	} else {
		state.CursorKey = ""
		state.RevealFile = ""
	}
	state.SearchOpen = false
	state.ShowAllComments = false
	state.ScrollToCommentID = ""
	state.ScrollToHeadingID = ""
	return state, nil
}
