package review

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/livetemplate/livetemplate"
)

func decisionController(t *testing.T) *PrereviewController {
	t.Helper()
	// decisionsPath derives from CSVPath's directory; a real temp dir lets the
	// atomic writer round-trip to disk.
	return &PrereviewController{CSVPath: filepath.Join(t.TempDir(), "comments.csv")}
}

func decisionCtx(action, id, note string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), action,
		map[string]interface{}{"id": id, "note": note})
}

func TestDecisions_RoundTripAndTolerant(t *testing.T) {
	path := filepath.Join(t.TempDir(), SuggestionDecisionFileName)
	// Missing file → nil.
	if got := loadDecisions(path); got != nil {
		t.Fatalf("missing file: want nil, got %v", got)
	}
	in := []SuggestionDecision{
		{SuggestionID: "s1", Verdict: verdictAccept, Fingerprint: "fp1"},
		{SuggestionID: "s2", Verdict: verdictRevise, Note: "reword, please", Fingerprint: "fp2"},
	}
	if err := writeDecisions(path, in); err != nil {
		t.Fatalf("writeDecisions: %v", err)
	}
	got := loadDecisions(path)
	if len(got) != 2 || got[0].SuggestionID != "s1" || got[1].Note != "reword, please" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestDecisions_DedupLastWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), SuggestionDecisionFileName)
	// Append-style history collapsed by the loader: last verdict for an id wins.
	writeDecisions(path, []SuggestionDecision{{SuggestionID: "s1", Verdict: verdictAccept, Fingerprint: "fp"}})
	// Simulate a second, superseding write for the same id.
	writeDecisions(path, []SuggestionDecision{
		{SuggestionID: "s1", Verdict: verdictAccept, Fingerprint: "fp"},
		{SuggestionID: "s1", Verdict: verdictReject, Fingerprint: "fp"},
	})
	got := loadDecisions(path)
	if len(got) != 1 || got[0].Verdict != verdictReject {
		t.Fatalf("dedup last-wins: want single reject, got %+v", got)
	}
}

func TestSuggestionFingerprint_ChangeSensitive(t *testing.T) {
	base := Suggestion{OriginalText: "a", ProposedText: "b"}
	if suggestionFingerprint(base) != suggestionFingerprint(Suggestion{OriginalText: "a", ProposedText: "b"}) {
		t.Error("identical content must fingerprint identically")
	}
	if suggestionFingerprint(base) == suggestionFingerprint(Suggestion{OriginalText: "a", ProposedText: "c"}) {
		t.Error("changed proposed text must change the fingerprint")
	}
	if suggestionFingerprint(base) == suggestionFingerprint(Suggestion{OriginalText: "x", ProposedText: "b"}) {
		t.Error("changed original text must change the fingerprint")
	}
}

func TestDecisionsBySuggestion_FingerprintGating(t *testing.T) {
	sg := Suggestion{ID: "s1", OriginalText: "old", ProposedText: "new"}
	st := PrereviewState{
		Suggestions: []Suggestion{sg},
		Decisions: []SuggestionDecision{
			{SuggestionID: "s1", Verdict: verdictAccept, Fingerprint: suggestionFingerprint(sg)},
			{SuggestionID: "orphan", Verdict: verdictReject, Fingerprint: "x"}, // no such suggestion
		},
	}
	// Matching fingerprint → decision applies; orphan dropped.
	m := st.DecisionsBySuggestion()
	if len(m) != 1 || m["s1"].Verdict != verdictAccept {
		t.Fatalf("want only s1=accept, got %+v", m)
	}
	if st.DecisionCount() != 1 {
		t.Errorf("DecisionCount = %d, want 1", st.DecisionCount())
	}

	// Simulate a same-id revision: the suggestion's proposed text changes, so the
	// stored decision's fingerprint no longer matches → the suggestion reads as
	// undecided (the stale accept must NOT ride the new proposal).
	st.Suggestions[0].ProposedText = "revised!"
	if len(st.DecisionsBySuggestion()) != 0 {
		t.Error("a revised suggestion must drop its stale decision")
	}
}

func TestDecisionActions_AcceptRejectReviseUndo(t *testing.T) {
	c := decisionController(t)
	sg := Suggestion{ID: "s1", File: "a.md", OriginalText: "o", ProposedText: "p"}
	base := PrereviewState{Suggestions: []Suggestion{sg}}

	// Accept, then reject (upsert), verifying persistence each time.
	st, _ := c.AcceptSuggestion(base, decisionCtx("acceptSuggestion", "s1", ""))
	if st.DecisionsBySuggestion()["s1"].Verdict != verdictAccept {
		t.Fatal("accept not recorded")
	}
	if got := loadDecisions(c.decisionsPath()); len(got) != 1 || got[0].Verdict != verdictAccept {
		t.Fatalf("accept not persisted: %+v", got)
	}
	st, _ = c.RejectSuggestion(st, decisionCtx("rejectSuggestion", "s1", ""))
	if st.DecisionsBySuggestion()["s1"].Verdict != verdictReject {
		t.Fatal("reject did not supersede accept")
	}
	if got := loadDecisions(c.decisionsPath()); len(got) != 1 {
		t.Fatalf("upsert should keep one decision, got %d", len(got))
	}

	// Undo clears it.
	st, _ = c.ClearSuggestionDecision(st, decisionCtx("clearSuggestionDecision", "s1", ""))
	if len(st.DecisionsBySuggestion()) != 0 {
		t.Error("undo should clear the decision")
	}
	if got := loadDecisions(c.decisionsPath()); len(got) != 0 {
		t.Errorf("undo should persist an empty set, got %+v", got)
	}
}

func TestRequestRevision_NoteFlow(t *testing.T) {
	c := decisionController(t)
	sg := Suggestion{ID: "s1", OriginalText: "o", ProposedText: "p"}
	base := PrereviewState{Suggestions: []Suggestion{sg}}

	// Opening the form arms RevisingSuggestionID but records nothing yet.
	st, _ := c.RequestRevision(base, decisionCtx("requestRevision", "s1", ""))
	if st.RevisingSuggestionID != "s1" {
		t.Fatal("RequestRevision should open the form")
	}
	if len(st.DecisionsBySuggestion()) != 0 {
		t.Error("opening the form must not record a decision")
	}

	// Submitting an empty note keeps the form open, no decision.
	st, _ = c.SubmitRevision(st, decisionCtx("submitRevision", "s1", "   "))
	if st.RevisingSuggestionID != "s1" || len(st.DecisionsBySuggestion()) != 0 {
		t.Error("empty note must keep the form open and record nothing")
	}

	// A real note records a revise verdict with the note and closes the form.
	st, _ = c.SubmitRevision(st, decisionCtx("submitRevision", "s1", "please soften the tone"))
	d := st.DecisionsBySuggestion()["s1"]
	if d.Verdict != verdictRevise || d.Note != "please soften the tone" {
		t.Fatalf("revise not recorded with note: %+v", d)
	}
	if st.RevisingSuggestionID != "" {
		t.Error("form should close after a successful submit")
	}
}
