package review

import "testing"

// TestAnnotationBadges_StateModel (#165): resolved comments and DECIDED suggestions stay
// in the inline view (they collapse to a badge via CSS, not vanish); the count badges count
// ALL annotations of a kind; and the state maps drive the 3-colour badge — OPEN (undecided
// suggestion / unresolved comment) → yellow, ACCEPTED-but-unapplied → accentuated yellow,
// else green. Open wins over accepted on a mixed line.
func TestAnnotationBadges_StateModel(t *testing.T) {
	fp := func(sg Suggestion) string { return suggestionFingerprint(sg) }
	sgOpen := Suggestion{ID: "sOpen", File: "a.go", FromLine: 4, ToLine: 4, Side: "new", OriginalText: "x", ProposedText: "y"}
	sgAcc := Suggestion{ID: "sAcc", File: "a.go", FromLine: 4, ToLine: 4, Side: "new", OriginalText: "p", ProposedText: "q"}
	sgApp := Suggestion{ID: "sApp", File: "a.go", FromLine: 5, ToLine: 5, Side: "new", OriginalText: "m", ProposedText: "n"}
	s := PrereviewState{
		SelectedFile: "a.go",
		Comments: []Comment{
			{ID: "cOpen", File: "a.go", ToLine: 3, Side: "new", Kind: "line", Body: "open"},
			{ID: "cRes", File: "a.go", ToLine: 3, Side: "new", Kind: "line", Body: "resolved", Resolved: true},
			{ID: "cResOnly", File: "a.go", ToLine: 7, Side: "new", Kind: "line", Body: "resolved", Resolved: true},
		},
		Suggestions: []Suggestion{sgOpen, sgAcc, sgApp},
		// sAcc is accepted, sApp is accepted; only sApp has been APPLIED by the agent.
		Decisions: []SuggestionDecision{
			{SuggestionID: "sAcc", Verdict: verdictAccept, Fingerprint: fp(sgAcc)},
			{SuggestionID: "sApp", Verdict: verdictAccept, Fingerprint: fp(sgApp)},
		},
		Applied: map[string]bool{"sApp": true},
	}

	// Resolved comments render (no vanish) → line 3 has BOTH; line 7 has the resolved one.
	if got := len(s.CommentsByEndLine()[3]); got != 2 {
		t.Errorf("line 3 should render open + resolved comment; got %d", got)
	}
	if got := len(s.CommentsByEndLine()[7]); got != 1 {
		t.Errorf("a resolved-only line must still render (was the vanish bug); got %d", got)
	}
	// Count = all comments; open map = only rows with an unresolved one.
	if s.CommentCountLines()["3-new"] != 2 || s.CommentCountLines()["7-new"] != 1 {
		t.Errorf("comment counts wrong: %v", s.CommentCountLines())
	}
	if !s.CommentOpenLines()["3-new"] {
		t.Error("line 3 has an open comment → yellow")
	}
	if s.CommentOpenLines()["7-new"] {
		t.Error("line 7 is all-resolved → green (not open)")
	}

	// All suggestions render inline (decided ones collapse via CSS, not exclusion); count =
	// all of them. Line 4 = undecided sOpen + accepted-pending sAcc; line 5 = applied sApp.
	if got := len(s.SuggestionsByEndLine()[4]); got != 2 {
		t.Errorf("both line-4 suggestions render inline (decided collapses via CSS, not exclusion); got %d", got)
	}
	if s.SuggestionCountLines()["4-new"] != 2 || s.SuggestionCountLines()["5-new"] != 1 {
		t.Errorf("suggestion counts wrong: %v", s.SuggestionCountLines())
	}
	// OPEN map = undecided only. Line 4 has sOpen (undecided) → open; line 5's sApp is
	// applied → not open.
	if !s.SuggestionOpenLines()["4-new"] {
		t.Error("line 4 has an undecided suggestion → open (yellow)")
	}
	if s.SuggestionOpenLines()["5-new"] {
		t.Error("line 5's only suggestion is applied → not open")
	}
	// ACCEPTED map = accepted-but-unapplied. Line 4 has sAcc → accepted; line 5's sApp is
	// applied (done), not accepted-pending.
	if !s.SuggestionAcceptedLines()["4-new"] {
		t.Error("line 4 has an accepted-but-unapplied suggestion → accepted (accentuated yellow)")
	}
	if s.SuggestionAcceptedLines()["5-new"] {
		t.Error("line 5's suggestion is applied, not accepted-pending")
	}
	// The three predicates are mutually exclusive per suggestion.
	if !s.suggestionUndecided("sOpen") || s.suggestionAcceptedPending("sOpen") {
		t.Error("sOpen is undecided, not accepted-pending")
	}
	if !s.suggestionAcceptedPending("sAcc") || s.suggestionUndecided("sAcc") {
		t.Error("sAcc is accepted-pending, not undecided")
	}
	if s.suggestionUndecided("sApp") || s.suggestionAcceptedPending("sApp") {
		t.Error("sApp is applied → neither undecided nor accepted-pending (it's done)")
	}
	// The unified accepted map (comments have no accepted state) mirrors the suggestion one.
	if !s.AnnotationAcceptedLines()["4-new"] {
		t.Error("line 4 unified badge carries an accepted-pending suggestion")
	}
}
