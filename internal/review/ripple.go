package review

import (
	"slices"

	"github.com/livetemplate/livetemplate"
)

// ripple.go is #204's ripple group: a change that reaches past its own lines (drop
// a paragraph, rename a term, move a section) arrives as several suggestions that
// each name the comment they follow from (Suggestion.For). They show as one group on
// that comment, so the reviewer can take or refuse the whole change at once.

// RippleGroup is the follow-on suggestions of one comment.
type RippleGroup struct {
	CommentID  string
	Total      int // every suggestion naming the comment
	Open       int // still undecided
	Acceptable int // what Accept all would accept: Open minus the alternatives awaiting a pick
	NeedPick   int // alternative sets (#117) with more than one undecided member
}

// rippleMembers is the in-scope suggestions that follow from commentID.
func (s PrereviewState) rippleMembers(commentID string) []Suggestion {
	var out []Suggestion
	for _, sg := range s.scopedSuggestions() {
		if sg.For == commentID {
			out = append(out, sg)
		}
	}
	return out
}

// rippleAcceptable splits a group's undecided members into the ones Accept all takes
// and the count of alternative sets it leaves for the reviewer to pick from: accepting
// one alternative rejects the others, so taking them all would be a coin toss.
func (s PrereviewState) rippleAcceptable(members []Suggestion, decisions map[string]SuggestionDecision) (take []Suggestion, needPick int) {
	undecided := map[string][]Suggestion{}
	var order []string
	for _, sg := range members {
		if !s.undecidedIn(decisions, sg.ID) {
			continue
		}
		k := sg.groupKey()
		if _, ok := undecided[k]; !ok {
			order = append(order, k)
		}
		undecided[k] = append(undecided[k], sg)
	}
	for _, k := range order {
		if g := undecided[k]; len(g) == 1 {
			take = append(take, g[0])
		} else {
			needPick++
		}
	}
	return take, needPick
}

// RippleByComment maps a comment id to its follow-on group, for the row on the
// comment's card. Zero-arg; nil when no suggestion names a comment.
func (s PrereviewState) RippleByComment() map[string]*RippleGroup {
	var out map[string]*RippleGroup
	byComment := map[string][]Suggestion{}
	for _, sg := range s.scopedSuggestions() {
		if sg.For != "" {
			byComment[sg.For] = append(byComment[sg.For], sg)
		}
	}
	if len(byComment) == 0 {
		return nil
	}
	decisions := s.DecisionsBySuggestion()
	for id, members := range byComment {
		g := &RippleGroup{CommentID: id, Total: len(members)}
		for _, sg := range members {
			if s.undecidedIn(decisions, sg.ID) {
				g.Open++
			}
		}
		take, needPick := s.rippleAcceptable(members, decisions)
		g.Acceptable, g.NeedPick = len(take), needPick
		if out == nil {
			out = map[string]*RippleGroup{}
		}
		out[id] = g
	}
	return out
}

// AcceptRipple accepts every follow-on suggestion of a comment (the button's value),
// except alternative sets still awaiting a pick. Each accept rejects its own
// alternatives, as a single Accept does, and it's all one write.
func (c *PrereviewController) AcceptRipple(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	members := state.rippleMembers(ctx.GetString("value"))
	take, _ := state.rippleAcceptable(members, state.DecisionsBySuggestion())
	if len(take) == 0 {
		return state, nil
	}
	next := slices.Clone(state.Decisions)
	for _, sg := range take {
		next = upsertDecision(next, newDecision(sg, verdictAccept, "", false))
		for _, other := range state.Suggestions {
			if other.ID != sg.ID && other.groupKey() == sg.groupKey() {
				next = upsertDecision(next, newDecision(other, verdictReject, "", true))
			}
		}
	}
	c.commitDecisions(&state, next)
	noteEnqueue(&state) // accepted edits are queued work, as with a single Accept
	return state, nil
}

// RejectRipple rejects every undecided follow-on suggestion of a comment, in one write.
func (c *PrereviewController) RejectRipple(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	decisions := state.DecisionsBySuggestion()
	next := slices.Clone(state.Decisions)
	changed := false
	for _, sg := range state.rippleMembers(ctx.GetString("value")) {
		if state.undecidedIn(decisions, sg.ID) {
			next = upsertDecision(next, newDecision(sg, verdictReject, "", false))
			changed = true
		}
	}
	if changed {
		c.commitDecisions(&state, next)
	}
	return state, nil
}
