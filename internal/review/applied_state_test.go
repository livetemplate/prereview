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
	if len(s.AnnotationOpenLines()) != 0 {
		t.Errorf("AnnotationOpenLines = %v, want empty — an applied suggestion is done, not open",
			s.AnnotationOpenLines())
	}
	// It stays COUNTED (the badge still exists, just green) — collapse is CSS, not exclusion.
	if len(s.AnnotationCountLines()) == 0 {
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

// TestAwaitingPickCount (#206): forks (groups of 2+ alternatives) that still owe the
// reviewer's pick, one row per state in the plan's contract.
func TestAwaitingPickCount(t *testing.T) {
	alt := func(id, file string, line int) Suggestion {
		return Suggestion{ID: id, File: file, FromLine: line, ToLine: line, Side: "new",
			OriginalText: "Fast and simple.", ProposedText: id}
	}
	a, b := alt("c1-a", "doc.md", 3), alt("c1-b", "doc.md", 3)
	lone := Suggestion{ID: "lone", File: "doc.md", FromLine: 9, ToLine: 9, Side: "new", OriginalText: "teh", ProposedText: "the"}
	verdict := func(sg Suggestion, v string) SuggestionDecision {
		return SuggestionDecision{SuggestionID: sg.ID, Verdict: v, Fingerprint: suggestionFingerprint(sg)}
	}
	outdated := b
	outdated.AnchorStatus = anchorOutdated

	for _, tc := range []struct {
		name string
		s    PrereviewState
		want int
	}{
		{"a lone suggestion is not a fork", PrereviewState{Suggestions: []Suggestion{lone}}, 0},
		{"undecided fork", PrereviewState{Suggestions: []Suggestion{a, b, lone}}, 1},
		{"one rejected, one undecided", PrereviewState{Suggestions: []Suggestion{a, b},
			Decisions: []SuggestionDecision{verdict(a, verdictReject)}}, 1},
		{"one accepted (sibling auto-rejected)", PrereviewState{Suggestions: []Suggestion{a, b},
			Decisions: []SuggestionDecision{verdict(a, verdictAccept), verdict(b, verdictReject)}}, 0},
		{"accepted and applied", PrereviewState{Suggestions: []Suggestion{a, b},
			Decisions: []SuggestionDecision{verdict(a, verdictAccept)}, Applied: map[string]bool{"c1-a": true}}, 0},
		{"both rejected: neither, the agent's turn", PrereviewState{Suggestions: []Suggestion{a, b},
			Decisions: []SuggestionDecision{verdict(a, verdictReject), verdict(b, verdictReject)}}, 0},
		{"an outdated member: the cards can't be acted on", PrereviewState{Suggestions: []Suggestion{a, outdated}}, 0},
		{"hidden cards still count (hiding is view-only)", PrereviewState{Suggestions: []Suggestion{a, b},
			Hidden: []HiddenSuggestion{{SuggestionID: "c1-a", Fingerprint: suggestionFingerprint(a)}, {SuggestionID: "c1-b", Fingerprint: suggestionFingerprint(b)}}}, 1},
		{"counts forks, not suggestions; whole review", PrereviewState{SelectedFile: "doc.md",
			Suggestions: []Suggestion{a, b, alt("c2-a", "other.md", 5), alt("c2-b", "other.md", 5), alt("c2-c", "other.md", 5)}}, 2},
	} {
		if got := tc.s.AwaitingPickCount(); got != tc.want {
			t.Errorf("%s: AwaitingPickCount = %d, want %d", tc.name, got, tc.want)
		}
	}
}
