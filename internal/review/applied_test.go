package review

import (
	"os"
	"path/filepath"
	"testing"
)

// TestActionableDecisions_SkipsApplied: an accepted suggestion is actionable until the
// agent acks it applied (#159), then it drops from the snapshot.
func TestActionableDecisions_SkipsApplied(t *testing.T) {
	sugs := []Suggestion{{ID: "s1"}}
	decided := map[string]SuggestionDecision{"s1": {SuggestionID: "s1", Verdict: "accept"}}

	if got := actionableDecisions(sugs, decided, nil, nil); len(got) != 1 {
		t.Fatalf("accepted, not-yet-applied suggestion should be actionable; got %d", len(got))
	}
	if got := actionableDecisions(sugs, decided, nil, map[string]bool{"s1": true}); len(got) != 0 {
		t.Errorf("an APPLIED suggestion should drop from the actionable snapshot; got %d", len(got))
	}
}

func TestLoadAppliedSet(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, CommentsFileName)
	if s := loadAppliedSet(csvPath); s != nil {
		t.Errorf("empty/missing applied set should be nil; got %v", s)
	}
	if err := os.WriteFile(AppliedPath(csvPath), []byte(`{"id":"s1"}`+"\n"+`{"id":"s2"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := loadAppliedSet(csvPath)
	if !s["s1"] || !s["s2"] || len(s) != 2 {
		t.Errorf("applied set wrong: %v", s)
	}
}
