package review

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/livetemplate/livetemplate"
)

// controller_suggestions.go holds the reviewer's decision actions on LLM
// suggestions (issue #98 Phase 2): accept / reject / request-revision (with a
// note), plus undo. Each follows the same mutate-in-memory → persist → rollback
// contract as the comment actions, writing the server-owned
// .prereview/suggestion-decisions.jsonl. Nothing is applied to the reviewed files
// here — a decision is PENDING until the reviewer hands off (Phase 3).

// findSuggestion returns a pointer to the current suggestion with the given id, or
// nil. The pointer is used only to fingerprint the content the decision is made
// against (so the decision auto-invalidates if the suggestion later changes).
func (s *PrereviewState) findSuggestion(id string) *Suggestion {
	for i := range s.Suggestions {
		if s.Suggestions[i].ID == id {
			return &s.Suggestions[i]
		}
	}
	return nil
}

// commitDecisions sets state.Decisions to next and persists it in ONE atomic
// write, so a grouped accept (accept + N sibling auto-rejects) or a group re-open
// is a single write / emit / rollback — never a half-decided group on disk. #119:
// a decision change re-arms the snapshot emit.
func (c *PrereviewController) commitDecisions(state *PrereviewState, next []SuggestionDecision) {
	prev := state.Decisions
	state.Decisions = next
	if err := writeDecisions(c.decisionsPath(), next); err != nil {
		state.Decisions = prev // rollback
		slog.Warn("persist decisions", "err", err)
		return
	}
	c.scheduleEmit()
}

// setDecision upserts a single verdict for a suggestion (reject / revise). A
// missing suggestion (raced away) is a no-op.
func (c *PrereviewController) setDecision(state *PrereviewState, id, verdict, note string) {
	sg := state.findSuggestion(id)
	if sg == nil {
		slog.Warn("decision on unknown suggestion", "id", id, "verdict", verdict)
		return
	}
	c.commitDecisions(state, upsertDecision(slices.Clone(state.Decisions), newDecision(*sg, verdict, note, false)))
}

// AcceptSuggestion records an "accept" AND auto-rejects every OTHER suggestion in
// the same artifact-area group (#117): alternatives for the same text are mutually
// exclusive, like a radio button, so the reviewer no longer rejects the rest by
// hand. The sibling rejects override whatever verdict they had and are marked Auto
// (so clearing the accept re-opens the group — see ClearSuggestionDecision). All
// batched into one persist.
func (c *PrereviewController) AcceptSuggestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RevisingSuggestionID = ""
	id := ctx.GetString("id")
	sg := state.findSuggestion(id)
	if sg == nil {
		slog.Warn("accept on unknown suggestion", "id", id)
		return state, nil
	}
	next := upsertDecision(slices.Clone(state.Decisions), newDecision(*sg, verdictAccept, "", false))
	key := sg.groupKey()
	for i := range state.Suggestions {
		if other := state.Suggestions[i]; other.ID != id && other.groupKey() == key {
			next = upsertDecision(next, newDecision(other, verdictReject, "", true))
		}
	}
	c.commitDecisions(&state, next)
	// An accepted suggestion is now queued work for the agent (apply the edit), so
	// it rides the exact same feedback adding a comment gets (#159): pulse the
	// toolbar Queue button + bump its count. No toast — matching AddComment, which
	// is silent too (the top-right toast slot is already crowded), and the accepted
	// suggestion box stays put until the agent applies.
	noteEnqueue(&state)
	return state, nil
}

// RequestRevert asks the agent to UNDO an already-applied accept (#159 M4.2): it sets
// the Revert flag on the suggestion's accept decision, so the next snapshot carries it
// to the agent as verdict="revert". The agent restores the original text on disk and
// acks with `prereview reverted <id>` — that drops the suggestion out of the applied
// set, at which point DecisionsBySuggestion filters the now-revert-complete decision
// back to undecided. prereview still never writes the file itself; the agent does.
func (c *PrereviewController) RequestRevert(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("requestRevert: missing id")
	}
	next := slices.Clone(state.Decisions)
	found := false
	for i := range next {
		if next[i].SuggestionID == id {
			next[i].Revert = true
			found = true
		}
	}
	if !found {
		slog.Warn("revert on suggestion with no decision", "id", id)
		return state, nil
	}
	c.commitDecisions(&state, next)
	return state, nil
}

// RejectSuggestion records a "reject" verdict.
func (c *PrereviewController) RejectSuggestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RevisingSuggestionID = ""
	c.setDecision(&state, ctx.GetString("id"), verdictReject, "")
	return state, nil
}

// RequestRevision opens the inline note form on a suggestion (mirrors
// EditComment arming EditingCommentID). The verdict is only recorded once the
// note is submitted (SubmitRevision). If a revision note was already recorded for
// this suggestion, the form opens pre-filled with it so the reviewer can EDIT the
// note in place rather than undo-and-retype.
func (c *PrereviewController) RequestRevision(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	state.RevisingSuggestionID = id
	state.RevisionDraft = ""
	for _, d := range state.Decisions {
		if d.SuggestionID == id && d.Verdict == verdictRevise {
			state.RevisionDraft = d.Note // edit the existing note
			break
		}
	}
	return state, nil
}

// SaveRevisionDraft keeps the in-progress note across the debounced re-renders
// (the textarea is lvt-form:preserve, but persisting the draft means it also
// survives a reconnect) — mirrors SaveDraft for comments.
func (c *PrereviewController) SaveRevisionDraft(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RevisionDraft = ctx.GetString("note")
	return state, nil
}

// SubmitRevision records a "revise" verdict with the reviewer's note and closes
// the form. An empty note is rejected (the note IS the requested change), leaving
// the form open so the reviewer can type one.
func (c *PrereviewController) SubmitRevision(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	note := strings.TrimSpace(ctx.GetString("note"))
	if note == "" {
		state.RevisingSuggestionID = id // keep the form open
		state.RevisionDraft = ctx.GetString("note")
		return state, nil
	}
	c.setDecision(&state, id, verdictRevise, note)
	state.RevisingSuggestionID = ""
	state.RevisionDraft = ""
	return state, nil
}

// CancelRevision closes the note form without recording anything.
func (c *PrereviewController) CancelRevision(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RevisingSuggestionID = ""
	state.RevisionDraft = ""
	return state, nil
}

// commitHidden sets state.Hidden to next and persists it in ONE atomic write, so
// hiding a whole group (N entries) is a single write / rollback — never a
// half-hidden group on disk. Hiding is view-only, so it does NOT scheduleEmit:
// the LLM's snapshot is unaffected (a hidden suggestion is still an open
// suggestion, not a reject).
func (c *PrereviewController) commitHidden(state *PrereviewState, next []HiddenSuggestion) {
	prev := state.Hidden
	state.Hidden = next
	if err := writeHidden(c.hiddenPath(), next); err != nil {
		state.Hidden = prev // rollback
		slog.Warn("persist hidden suggestions", "err", err)
	}
}

// upsertHidden replaces any existing hide for h.SuggestionID with h (re-pinning
// its fingerprint), keeping the rest.
func upsertHidden(list []HiddenSuggestion, h HiddenSuggestion) []HiddenSuggestion {
	list = slices.DeleteFunc(list, func(x HiddenSuggestion) bool { return x.SuggestionID == h.SuggestionID })
	return append(list, h)
}

// HideSuggestion hides a single suggestion from view (pinned to its current
// content, so a later revision un-hides it). A pure declutter — the suggestion
// stays open and still reaches the LLM on hand-off.
func (c *PrereviewController) HideSuggestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	sg := state.findSuggestion(ctx.GetString("id"))
	if sg == nil {
		return state, nil // raced away
	}
	c.commitHidden(&state, upsertHidden(slices.Clone(state.Hidden), newHidden(*sg)))
	return state, nil
}

// HideSuggestionGroup hides every alternative in the same artifact-area group as
// the given suggestion (#117) in one atomic write — the "hide these alternatives"
// affordance. A lone suggestion (no group) hides just itself.
func (c *PrereviewController) HideSuggestionGroup(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	sg := state.findSuggestion(ctx.GetString("id"))
	if sg == nil {
		return state, nil
	}
	key := sg.groupKey()
	next := slices.Clone(state.Hidden)
	for i := range state.Suggestions {
		if state.Suggestions[i].groupKey() == key {
			next = upsertHidden(next, newHidden(state.Suggestions[i]))
		}
	}
	c.commitHidden(&state, next)
	return state, nil
}

// ShowHiddenSuggestions clears the SELECTED file's hides (the "show N hidden"
// recovery), bringing them back into view. Other files' hides are untouched, so
// the count next to the affordance stays scoped to the page. Mirrors
// UnhideAllResolved for comments.
func (c *PrereviewController) ShowHiddenSuggestions(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if len(state.Hidden) == 0 {
		return state, nil
	}
	// Which hidden IDs belong to the selected file?
	onFile := make(map[string]bool)
	for _, sg := range state.Suggestions {
		if sg.File == state.SelectedFile {
			onFile[sg.ID] = true
		}
	}
	next := slices.DeleteFunc(slices.Clone(state.Hidden), func(h HiddenSuggestion) bool {
		return onFile[h.SuggestionID]
	})
	c.commitHidden(&state, next)
	return state, nil
}

// ClearSuggestionDecision removes a recorded decision (undo), returning the
// suggestion to undecided. If the cleared decision was an ACCEPT, the whole group
// it auto-rejected re-opens too (#117): clear every Auto reject on a sibling in
// the same group, so undoing the accept restores the group to undecided rather
// than leaving it permanently all-rejected. Batched into one persist.
func (c *PrereviewController) ClearSuggestionDecision(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	var cleared *SuggestionDecision
	for i := range state.Decisions {
		if state.Decisions[i].SuggestionID == id {
			cleared = &state.Decisions[i]
			break
		}
	}
	if cleared == nil {
		return state, nil // nothing to clear
	}
	next := slices.DeleteFunc(slices.Clone(state.Decisions), func(d SuggestionDecision) bool {
		return d.SuggestionID == id
	})
	// Re-open the group the accept had auto-rejected.
	if cleared.Verdict == verdictAccept {
		if sg := state.findSuggestion(id); sg != nil {
			key := sg.groupKey()
			next = slices.DeleteFunc(next, func(d SuggestionDecision) bool {
				if !d.Auto {
					return false
				}
				sib := state.findSuggestion(d.SuggestionID)
				return sib != nil && sib.groupKey() == key
			})
		}
	}
	c.commitDecisions(&state, next)
	return state, nil
}
