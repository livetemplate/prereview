package review

import "testing"

// groupAlt builds one alternative in a group: alternatives share File/Side/range/
// OriginalText and differ only in ProposedText (#117).
func groupAlt(id, proposed string) Suggestion {
	return Suggestion{
		ID: id, File: "a.md", Side: "new", FromLine: 4, ToLine: 4,
		OriginalText: "return nil", ProposedText: proposed,
	}
}

// TestAcceptSuggestion_AutoRejectsGroup: accepting one alternative auto-rejects the
// others in the same artifact-area group; accepting a different one switches; and
// clearing the accept re-opens the whole group.
func TestAcceptSuggestion_AutoRejectsGroup(t *testing.T) {
	c := decisionController(t)
	st := PrereviewState{Suggestions: []Suggestion{
		groupAlt("a", "return err"), groupAlt("b", "return errors.New(x)"), groupAlt("cc", "return fmt.Errorf(x)"),
	}}
	v := func(s PrereviewState, id string) SuggestionDecision { return s.DecisionsBySuggestion()[id] }

	// Accept a → b, cc auto-rejected.
	st, _ = c.AcceptSuggestion(st, decisionCtx("acceptSuggestion", "a", ""))
	if v(st, "a").Verdict != verdictAccept {
		t.Fatalf("a should be accepted, got %q", v(st, "a").Verdict)
	}
	for _, id := range []string{"b", "cc"} {
		if d := v(st, id); d.Verdict != verdictReject || !d.Auto {
			t.Errorf("%s should be auto-rejected, got verdict=%q auto=%v", id, d.Verdict, d.Auto)
		}
	}
	// Batched: one atomic write holding all three decisions.
	if got := loadDecisions(c.decisionsPath()); len(got) != 3 {
		t.Errorf("grouped accept should persist 3 decisions in one write, got %d", len(got))
	}

	// Accept b → switches: b accepted, a & cc auto-rejected.
	st, _ = c.AcceptSuggestion(st, decisionCtx("acceptSuggestion", "b", ""))
	if v(st, "b").Verdict != verdictAccept {
		t.Fatal("b should now be accepted")
	}
	if d := v(st, "a"); d.Verdict != verdictReject || !d.Auto {
		t.Errorf("a should switch to auto-reject, got verdict=%q auto=%v", d.Verdict, d.Auto)
	}

	// Clear b's accept → the whole group re-opens.
	st, _ = c.ClearSuggestionDecision(st, decisionCtx("clearSuggestionDecision", "b", ""))
	if n := len(st.DecisionsBySuggestion()); n != 0 {
		t.Errorf("clearing the accept should re-open the group (0 decisions), got %d", n)
	}
}

// TestSuggestionGroups: multi-member groups surface a distinct index + total; a
// lone suggestion is not a group.
func TestSuggestionGroups(t *testing.T) {
	st := PrereviewState{Suggestions: []Suggestion{
		groupAlt("a", "x"), groupAlt("b", "y"), // same area → group of 2
		{ID: "lone", File: "a.md", Side: "new", FromLine: 9, ToLine: 9, OriginalText: "foo", ProposedText: "bar"},
	}}
	g := st.SuggestionGroups()
	if g["a"].Total != 2 || g["b"].Total != 2 {
		t.Errorf("a/b should be one group of 2, got %+v", g)
	}
	if g["a"].Index == g["b"].Index {
		t.Errorf("group members need distinct positions, got a=%d b=%d", g["a"].Index, g["b"].Index)
	}
	if _, ok := g["lone"]; ok {
		t.Error("a standalone suggestion must not be reported as grouped")
	}
}

// TestAcceptSuggestion_DoesNotOverGroup: the SAME OriginalText at a DIFFERENT
// location is a distinct area — accepting one must NOT auto-reject the other.
func TestAcceptSuggestion_DoesNotOverGroup(t *testing.T) {
	c := decisionController(t)
	st := PrereviewState{Suggestions: []Suggestion{
		{ID: "here", File: "a.go", Side: "new", FromLine: 4, ToLine: 4, OriginalText: "return nil", ProposedText: "return err"},
		{ID: "far", File: "a.go", Side: "new", FromLine: 40, ToLine: 40, OriginalText: "return nil", ProposedText: "return err2"},
	}}
	st, _ = c.AcceptSuggestion(st, decisionCtx("acceptSuggestion", "here", ""))
	if _, decided := st.DecisionsBySuggestion()["far"]; decided {
		t.Error("same text at a different location must NOT be auto-rejected (over-grouping)")
	}
}

// TestClearAccept_PreservesUnrelatedManualReject: re-opening a group on clear must
// only touch that group's Auto rejects, never a manual reject in another group.
func TestClearAccept_PreservesUnrelatedManualReject(t *testing.T) {
	c := decisionController(t)
	st := PrereviewState{Suggestions: []Suggestion{
		groupAlt("a", "x"), groupAlt("b", "y"),
		{ID: "other", File: "a.md", Side: "new", FromLine: 9, ToLine: 9, OriginalText: "foo", ProposedText: "bar"},
	}}
	st, _ = c.RejectSuggestion(st, decisionCtx("rejectSuggestion", "other", "")) // manual reject, different group
	st, _ = c.AcceptSuggestion(st, decisionCtx("acceptSuggestion", "a", ""))     // auto-rejects b
	st, _ = c.ClearSuggestionDecision(st, decisionCtx("clearSuggestionDecision", "a", ""))
	if d := st.DecisionsBySuggestion()["other"]; d.Verdict != verdictReject || d.Auto {
		t.Errorf("unrelated manual reject must survive the group re-open, got verdict=%q auto=%v", d.Verdict, d.Auto)
	}
}
