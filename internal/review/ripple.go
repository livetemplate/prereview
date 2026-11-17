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

func (s PrereviewState) rippleMembers(commentID string) []Suggestion {
	var out []Suggestion
	for _, sg := range s.scopedSuggestions() {
		if sg.For == commentID {
			out = append(out, sg)
		}
	}
	return out
}

// rippleSplit sorts a group's undecided members: the ones Accept all takes, and how
// many alternative sets (#117) it leaves for the reviewer to pick from. A set is
// judged across the whole review, not just the group: accepting one alternative
// rejects the others, so taking a member whose set still has another undecided
// alternative, linked to this comment or not, would decide the pick for them.
func (s PrereviewState) rippleSplit(members []Suggestion, decisions map[string]SuggestionDecision) (take []Suggestion, open, needPick int) {
	undecidedAlts := map[string]int{}
	for _, g := range groupByKey(s.scopedSuggestions()) {
		for _, sg := range g {
			if s.undecidedIn(decisions, sg.ID) {
				undecidedAlts[sg.groupKey()]++
			}
		}
	}
	pick := map[string]bool{}
	for _, sg := range members {
		if !s.undecidedIn(decisions, sg.ID) {
			continue
		}
		open++
		if k := sg.groupKey(); undecidedAlts[k] > 1 {
			pick[k] = true
		} else {
			take = append(take, sg)
		}
	}
	return take, open, len(pick)
}

// RippleByComment maps a comment id to its follow-on group, for the row on the
// comment's card. Zero-arg; nil when no suggestion names a comment.
func (s PrereviewState) RippleByComment() map[string]*RippleGroup {
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
	out := make(map[string]*RippleGroup, len(byComment))
	for id, members := range byComment {
		take, open, needPick := s.rippleSplit(members, decisions)
		out[id] = &RippleGroup{CommentID: id, Total: len(members), Open: open, Acceptable: len(take), NeedPick: needPick}
	}
	return out
}

// AcceptRipple accepts every follow-on suggestion of a comment (the button's value),
// except alternative sets still awaiting a pick, in one write.
func (c *PrereviewController) AcceptRipple(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	take, _, _ := state.rippleSplit(state.rippleMembers(ctx.GetString("value")), state.DecisionsBySuggestion())
	if len(take) == 0 {
		return state, nil
	}
	next := slices.Clone(state.Decisions)
	for _, sg := range take {
		next = acceptWithSiblings(next, sg, state.Suggestions)
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
