package review

import (
	"log/slog"
	"slices"
	"strings"
	"time"

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

// setDecision upserts a verdict for a suggestion, stamping the current content
// fingerprint, then persists. On a write error it rolls the in-memory slice back
// so disk and memory never diverge. A missing suggestion (raced away) is a no-op.
func (c *PrereviewController) setDecision(state *PrereviewState, id, verdict, note string) {
	sg := state.findSuggestion(id)
	if sg == nil {
		slog.Warn("decision on unknown suggestion", "id", id, "verdict", verdict)
		return
	}
	prev := state.Decisions
	// Clone first so the rollback below can restore prev untouched: DeleteFunc
	// drops any existing verdict for this id (upsert), keeping other suggestions'.
	next := slices.DeleteFunc(slices.Clone(prev), func(d SuggestionDecision) bool {
		return d.SuggestionID == id
	})
	next = append(next, SuggestionDecision{
		SuggestionID: id,
		Verdict:      verdict,
		Note:         note,
		Fingerprint:  suggestionFingerprint(*sg),
		Created:      time.Now().UTC(),
	})
	state.Decisions = next
	if err := writeDecisions(c.decisionsPath(), next); err != nil {
		state.Decisions = prev // rollback
		slog.Warn("persist decision", "id", id, "verdict", verdict, "err", err)
	}
}

// AcceptSuggestion records an "accept" verdict.
func (c *PrereviewController) AcceptSuggestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RevisingSuggestionID = ""
	c.setDecision(&state, ctx.GetString("id"), verdictAccept, "")
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
// note is submitted (SubmitRevision).
func (c *PrereviewController) RequestRevision(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RevisingSuggestionID = ctx.GetString("id")
	state.RevisionDraft = ""
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

// ClearSuggestionDecision removes a recorded decision (undo), returning the
// suggestion to undecided. Persist + rollback like setDecision.
func (c *PrereviewController) ClearSuggestionDecision(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	prev := state.Decisions
	// Clone so the rollback restores prev untouched (DeleteFunc mutates in place).
	next := slices.DeleteFunc(slices.Clone(prev), func(d SuggestionDecision) bool {
		return d.SuggestionID == id
	})
	if len(next) == len(prev) {
		return state, nil // nothing to clear
	}
	state.Decisions = next
	if err := writeDecisions(c.decisionsPath(), next); err != nil {
		state.Decisions = prev // rollback
		slog.Warn("clear decision", "id", id, "err", err)
	}
	return state, nil
}
