package review

import (
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
			state.ScrollToCommentID = pos.ID
			state.focusPreviewBlock(pos.Line)
		case navSuggestion:
			state.ScrollToItemKey = "sg:" + pos.ID
			state.focusPreviewBlock(pos.Line)
		case navQuiz:
			state.ScrollToItemKey = "qz:" + pos.ID
		}
		revealItem(&state, *it)
	}
	state.NavAt = &pos
	state.NavSeen = true
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

// SetNavKind filters the bar to one kind ("" = all). The cursor stays, so the next
// step continues from the same place in the document.
func (c *PrereviewController) SetNavKind(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if k := ctx.GetString("kind"); navKindIndex(k) >= 0 {
		state.NavKind = k
	}
	return state, nil
}

// DismissNav puts the bar away for this session.
func (c *PrereviewController) DismissNav(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.NavDismissed = true
	return state, nil
}
