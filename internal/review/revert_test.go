package review

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadAppliedSet_NetsReverts: "applied" is appliedCount − revertedCount > 0, so
// the accept→apply→revert→re-apply cycle flips the derived flag with pure counting
// (#159 M4.2).
func TestLoadAppliedSet_NetsReverts(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, CommentsFileName)
	appliedPath, revertedPath := AppliedPath(csv), RevertedPath(csv)
	append1 := func(path string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(`{"id":"s1"}` + "\n"); err != nil {
			t.Fatal(err)
		}
	}

	append1(appliedPath) // applied
	if !loadAppliedSet(csv)["s1"] {
		t.Fatal("after apply, s1 should be applied")
	}
	append1(revertedPath) // reverted → nets to not-applied
	if loadAppliedSet(csv)["s1"] {
		t.Fatal("after revert, s1 should NOT be applied (counts net to 0)")
	}
	append1(appliedPath) // re-apply → applied again (2 applied > 1 reverted)
	if !loadAppliedSet(csv)["s1"] {
		t.Fatal("after re-apply, s1 should be applied again")
	}
}

// TestRevertLifecycle drives the server-side revert state machine: applied → revert
// requested (agent sees verdict=revert) → agent reverted (back to undecided). The
// file-restore half is the agent's job (covered end-to-end in e2e); here we pin the
// derivation that tells the agent what to do and cleans up after it.
func TestRevertLifecycle(t *testing.T) {
	sg := Suggestion{ID: "s1", File: "a.go", FromLine: 4, ToLine: 4, Side: "new",
		OriginalText: "old", ProposedText: "new", AnchorStatus: anchorOutdated}
	fp := suggestionFingerprint(sg)
	accept := SuggestionDecision{SuggestionID: "s1", Verdict: verdictAccept, Fingerprint: fp}

	// APPLIED (not reverted): the applied suggestion is out of the agent snapshot.
	applied := PrereviewState{Suggestions: []Suggestion{sg}, Decisions: []SuggestionDecision{accept}, Applied: map[string]bool{"s1": true}}
	if d, ok := applied.DecisionsBySuggestion()["s1"]; !ok || d.Revert {
		t.Fatalf("applied+not-reverting should be a plain accept; got %+v ok=%v", d, ok)
	}
	if got := actionableDecisions(applied.Suggestions, applied.DecisionsBySuggestion(), nil, applied.Applied); len(got) != 0 {
		t.Fatalf("an applied suggestion should not be actionable; got %d", len(got))
	}

	// REVERT REQUESTED (Revert set, still applied): the agent sees it as verdict=revert.
	reverting := applied
	reverting.Decisions = []SuggestionDecision{{SuggestionID: "s1", Verdict: verdictAccept, Revert: true, Fingerprint: fp}}
	if d := reverting.DecisionsBySuggestion()["s1"]; !d.Revert {
		t.Fatal("a revert-pending decision must stay visible (agent still has to act)")
	}
	got := actionableDecisions(reverting.Suggestions, reverting.DecisionsBySuggestion(), nil, reverting.Applied)
	if len(got) != 1 {
		t.Fatalf("a revert-pending suggestion must be actionable despite being applied+outdated; got %d", len(got))
	}
	if got[0].Verdict != "revert" {
		t.Errorf("revert-pending verdict = %q, want %q", got[0].Verdict, "revert")
	}
	if got[0].Original != "old" || got[0].Proposed != "new" {
		t.Errorf("revert must carry original/proposed so the agent can restore; got %q/%q", got[0].Original, got[0].Proposed)
	}

	// REVERTED (agent acked → no longer applied): the revert-complete decision filters
	// back to undecided, and it's no longer actionable.
	done := reverting
	done.Applied = nil // appliedCount == revertedCount
	if _, ok := done.DecisionsBySuggestion()["s1"]; ok {
		t.Fatal("a revert-complete decision must drop back to undecided")
	}
	if got := actionableDecisions(done.Suggestions, done.DecisionsBySuggestion(), nil, done.Applied); len(got) != 0 {
		t.Fatalf("a reverted (undecided) suggestion is not actionable; got %d", len(got))
	}
}
