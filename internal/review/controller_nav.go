package review

import (
	"fmt"

	"github.com/livetemplate/livetemplate"
)

// NavNext / NavPrev step the open-items bar (#193), and n/p call them too. They open
// the item's file and scroll the item into view.
func (c *PrereviewController) NavNext(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.navGo(state, +1)
}

func (c *PrereviewController) NavPrev(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.navGo(state, -1)
}

func (c *PrereviewController) navGo(state PrereviewState, delta int) (PrereviewState, error) {
	it := state.navStep(delta)
	if it == nil {
		return state, nil
	}
	return c.goToItem(state, *it)
}

// JumpToAnnotation opens an item from its row on the Annotations page (form
// kind and id), as the stepper would, and closes the page.
func (c *PrereviewController) JumpToAnnotation(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	kind, id := ctx.GetString("kind"), ctx.GetString("id")
	for _, e := range state.annotations() {
		if e.Pos.Kind == kind && e.Pos.ID == id {
			return c.goToItem(state, e.NavItem)
		}
	}
	return state, fmt.Errorf("jumpToAnnotation: no %s %q", kind, id)
}

// goToItem opens the item's file (or the live site), scrolls it into view, and moves
// the stepper's cursor there.
func (c *PrereviewController) goToItem(state PrereviewState, it NavItem) (PrereviewState, error) {
	pos := it.Pos
	if it.Region {
		state = focusLive(state, pos.ID)
	} else {
		var err error
		if state, err = c.selectFileForJump(state, it.File); err != nil {
			return state, err
		}
		switch pos.Kind {
		case navComment:
			state.scrollToComment(pos.ID, pos.Line)
		case navSuggestion:
			state.scrollToSuggestion(pos.ID, pos.Line)
		case navQuiz:
			state.ScrollToItemKey = "qz:" + pos.ID
		case navQuestion:
			state.scrollToQuestion(pos.ID, pos.Line)
		}
		revealItem(&state, it)
	}
	state.NavAt = &pos
	state.NavSeen = true
	state.NavSteps++
	return state, nil
}

// revealItem opens the row, block or file-head group a target sits in if the
// reviewer had collapsed it (#174's badge), so the scroll lands on a visible card.
func revealItem(state *PrereviewState, it NavItem) {
	if len(state.ToggledRows) == 0 {
		return
	}
	if !it.Pos.Body {
		delete(state.ToggledRows, FileHeadRowKey)
		return
	}
	line := it.Pos.Line
	for _, k := range rowKeysFor(line, it.Side) {
		delete(state.ToggledRows, k)
	}
	for _, b := range state.RenderedMarkdown() {
		if line >= b.StartLine && line <= b.EndLine {
			delete(state.ToggledRows, markdownBlockKey(b.StartLine, b.EndLine))
		}
	}
	if state.CurrentDiff != nil && !state.CurrentDiff.IsBinary && state.pastEndRule()(it.Side, line) {
		delete(state.ToggledRows, PastEndRowKey)
	}
}

// DismissNav collapses the row to its chip for this session (NavCollapsed).
func (c *PrereviewController) DismissNav(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.NavDismissed = true
	return state, nil
}

// ExpandNav brings the collapsed row back.
func (c *PrereviewController) ExpandNav(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.NavDismissed = false
	return state, nil
}
