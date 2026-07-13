package review

import "testing"

// #171: applied != decided.
//
// The agent can apply an edit the reviewer never accepted — `prereview applied <id>`
// with no verdict in suggestion-decisions.jsonl. The edit is then already IN the file,
// but the card rendered as an open proposal forever: suggestionUndecided ignored
// s.Applied (so the badge stayed amber "open"), and the CSS collapse rules keyed off
// sg-accept/sg-reject, which an un-decided suggestion doesn't have.
//
// Applied is terminal: not open, and collapsed (via the .is-applied CSS rule).
func TestApplied_WithoutAVerdictIsDoneNotOpen(t *testing.T) {
	sg := Suggestion{ID: "s1", File: "doc.md", FromLine: 1, ToLine: 1, Side: "new",
		OriginalText: "teh", ProposedText: "the"}
	s := PrereviewState{
		SelectedFile: "doc.md",
		Suggestions:  []Suggestion{sg},
		Applied:      map[string]bool{"s1": true}, // agent wrote it to the file...
		Decisions:    nil,                         // ...but the reviewer never clicked accept
	}

	if s.suggestionUndecided("s1") {
		t.Error("an APPLIED suggestion must not read as undecided — the edit is already in the " +
			"file, so an amber 'open' badge invites the reviewer to accept what is already done")
	}
	if s.suggestionAcceptedPending("s1") {
		t.Error("applied is not accepted-pending — there is nothing left to apply")
	}
	if len(s.SuggestionOpenLines()) != 0 {
		t.Errorf("SuggestionOpenLines = %v, want empty — an applied suggestion is done, not open",
			s.SuggestionOpenLines())
	}
	// It stays COUNTED (the badge still exists, just green) — collapse is CSS, not exclusion.
	if len(s.SuggestionCountLines()) == 0 {
		t.Error("an applied suggestion must still be counted — it collapses behind its badge, " +
			"it does not vanish")
	}

	// The badge ladder is unchanged for the states that already worked.
	undecided := PrereviewState{SelectedFile: "doc.md", Suggestions: []Suggestion{sg}}
	if !undecided.suggestionUndecided("s1") {
		t.Error("a genuinely undecided suggestion must still read as open")
	}

	accepted := PrereviewState{
		SelectedFile: "doc.md",
		Suggestions:  []Suggestion{sg},
		Decisions: []SuggestionDecision{
			{SuggestionID: "s1", Verdict: verdictAccept, Fingerprint: suggestionFingerprint(sg)},
		},
	}
	if !accepted.suggestionAcceptedPending("s1") {
		t.Error("accepted-but-unapplied must still read as accepted-pending (the amber badge)")
	}
	if accepted.suggestionUndecided("s1") {
		t.Error("an accepted suggestion is decided")
	}
}

// #171: AwaitingApplyCount is the count that makes "accepted but never applied" impossible
// to ignore — the state where the reviewer said yes, the agent's turn ended, and nothing
// will ever write the edit to the file. It walks the accept → apply → revert cycle.
func TestAwaitingApplyCount_TracksTheAcceptApplyCycle(t *testing.T) {
	sg := Suggestion{ID: "s1", File: "doc.md", FromLine: 1, ToLine: 1, Side: "new",
		OriginalText: "teh", ProposedText: "the"}
	accept := SuggestionDecision{SuggestionID: "s1", Verdict: verdictAccept,
		Fingerprint: suggestionFingerprint(sg)}

	// Undecided — nothing is owed.
	s := PrereviewState{SelectedFile: "doc.md", Suggestions: []Suggestion{sg}}
	if got := s.AwaitingApplyCount(); got != 0 {
		t.Errorf("undecided: AwaitingApplyCount = %d, want 0", got)
	}

	// Accepted — the agent owes us a file write. THIS is the state that used to go quiet.
	s.Decisions = []SuggestionDecision{accept}
	if got := s.AwaitingApplyCount(); got != 1 {
		t.Errorf("accepted: AwaitingApplyCount = %d, want 1 — an accepted edit nobody applies "+
			"leaves the document inconsistent, and the card has already collapsed to a badge", got)
	}

	// Applied — the edit is in the file; nothing is owed.
	s.Applied = map[string]bool{"s1": true}
	if got := s.AwaitingApplyCount(); got != 0 {
		t.Errorf("applied: AwaitingApplyCount = %d, want 0", got)
	}

	// Reverted — loadAppliedSet nets reverts, so the id drops out of Applied and the
	// accept is owed again. (#159's revert path must not strand the count at 0.)
	s.Applied = map[string]bool{}
	if got := s.AwaitingApplyCount(); got != 1 {
		t.Errorf("after revert: AwaitingApplyCount = %d, want 1 — an un-applied accept is "+
			"pending again", got)
	}

	// Out-of-scope work never counts (#171 scoping): another file's accepted suggestion
	// must not show up in this review's awaiting-apply count.
	other := Suggestion{ID: "s2", File: "other.md", FromLine: 1, ToLine: 1, Side: "new",
		OriginalText: "a", ProposedText: "b"}
	s.SingleFile = "doc.md"
	s.Suggestions = append(s.Suggestions, other)
	s.Decisions = append(s.Decisions, SuggestionDecision{SuggestionID: "s2",
		Verdict: verdictAccept, Fingerprint: suggestionFingerprint(other)})
	if got := s.AwaitingApplyCount(); got != 1 {
		t.Errorf("AwaitingApplyCount = %d, want 1 — other.md's accepted suggestion is not "+
			"part of this review", got)
	}
}
