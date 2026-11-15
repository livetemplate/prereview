package review

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/livetemplate/livetemplate"

	"github.com/livetemplate/prereview/internal/library"
)

// Library tabs. "" is Learnings, the first tab.
const (
	libTabLearnings = "learnings"
	libTabChecks    = "checks"
)

// learnings is the folded learnings log, re-read only when the file changes
// (its size/mtime fingerprint). Shared read-only by every tab; another server's
// append shows up on the next watcher tick.
func (c *PrereviewController) learnings() ([]library.Learning, string) {
	if c.LibraryDir == "" {
		return nil, ""
	}
	fp := statusFingerprint(filepath.Join(c.LibraryDir, library.LearningsFile))
	c.learnMu.Lock()
	defer c.learnMu.Unlock()
	if fp != c.learnFP || c.learnCache == nil {
		c.learnCache, c.learnFP = library.LoadLearnings(c.LibraryDir), fp
	}
	return c.learnCache, c.learnFP
}

// applyLibrary refreshes what the Library shows: the toolbar badge (always) and,
// while the Learnings tab is open, the learnings themselves (a closed Library
// carries none, so every action's state clone stays small).
func (c *PrereviewController) applyLibrary(state *PrereviewState) {
	ls, _ := c.learnings()
	state.LearnNew = 0
	for _, l := range ls {
		if l.Status == library.StatusProposed {
			state.LearnNew++
		}
	}
	state.Learnings = nil
	if state.ShowLibrary && state.libraryTab() == libTabLearnings {
		state.Learnings = ls
		state.LearnStore = filepath.Dir(c.CSVPath)
	}
}

// learningsChanged reports whether the log moved since the watcher last looked
// (any server's write), so the watcher fans it out once.
func (c *PrereviewController) learningsChanged() bool {
	if c.LibraryDir == "" {
		return false
	}
	fp := statusFingerprint(filepath.Join(c.LibraryDir, library.LearningsFile))
	c.learnMu.Lock()
	defer c.learnMu.Unlock()
	if fp == c.learnWatchFP {
		return false
	}
	c.learnWatchFP = fp
	return true
}

// LibraryUpdated re-reads the learnings after a write (this server's or
// another's). Dispatched by the watcher to every tab.
func (c *PrereviewController) LibraryUpdated(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.applyLibrary(&state)
	return state, nil
}

// SetLibraryTab switches the Library's tab.
func (c *PrereviewController) SetLibraryTab(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	tab := ctx.GetString("tab")
	if !slices.Contains([]string{libTabLearnings, libTabChecks}, tab) {
		return state, fmt.Errorf("library: unknown tab %q", tab)
	}
	state.LibraryTab = tab
	c.applyLibrary(&state)
	c.applyReviewChecks(&state)
	return state, nil
}

// SetLearnFilter sets the Learnings tab's status chip or scope picker.
func (c *PrereviewController) SetLearnFilter(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if ctx.Has("scope") {
		state.LearnScope = ctx.GetString("scope")
		return state, nil
	}
	v := ctx.GetString("status")
	if !slices.Contains(learnStatuses, v) {
		return state, fmt.Errorf("learnings filter: unknown status %q", v)
	}
	state.LearnStatus = v
	return state, nil
}

// LearningAction is a card's Accept / Reject / Delete / Restore.
func (c *PrereviewController) LearningAction(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.LibraryDir == "" {
		return state, fmt.Errorf("no library folder")
	}
	if err := library.Act(c.LibraryDir, ctx.GetString("id"), ctx.GetString("act")); err != nil {
		return state, err
	}
	return c.libraryWritten(state), nil
}

// EditLearning opens a card's editor (again closes it).
func (c *PrereviewController) EditLearning(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if state.LearnEdit == id {
		id = ""
	}
	state.LearnEdit = id
	return state, nil
}

// SaveLearning saves a card's edited text and scope.
func (c *PrereviewController) SaveLearning(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.LibraryDir == "" {
		return state, fmt.Errorf("no library folder")
	}
	if err := library.Edit(c.LibraryDir, ctx.GetString("id"), ctx.GetString("text"), ctx.GetString("scope")); err != nil {
		return state, err
	}
	state.LearnEdit = ""
	return c.libraryWritten(state), nil
}

// libraryWritten refreshes this tab after its own write and fans it out to the
// others now (the watcher would, a tick later; other servers learn it that way).
func (c *PrereviewController) libraryWritten(state PrereviewState) PrereviewState {
	c.applyLibrary(&state)
	c.learningsChanged() // this server's tabs are told now, not again on the tick
	go c.broadcast("LibraryUpdated")
	return state
}
