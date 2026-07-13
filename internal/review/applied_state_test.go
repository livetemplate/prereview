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
