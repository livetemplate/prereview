package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/review"
)

func appliedMarks(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".prereview", review.AppliedFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestRunApplied_AppendsAndValidates(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("c1")})
	seedSuggestion(t, root, "s1")

	if err := runApplied([]string{"--out", root, "s1"}); err != nil {
		t.Fatalf("runApplied: %v", err)
	}
	if got := appliedMarks(t, root); len(got) != 1 || !strings.Contains(got[0], `"id":"s1"`) {
		t.Fatalf("want s1 recorded; got %v", got)
	}
	// Idempotent append (two accept snapshots before the ack lands is fine).
	if err := runApplied([]string{"--out", root, "s1"}); err != nil {
		t.Fatalf("runApplied 2: %v", err)
	}
	if got := appliedMarks(t, root); len(got) != 2 {
		t.Fatalf("want 2 lines after re-ack, got %d", len(got))
	}
}

func TestApplied_BogusIDExitsNonZero(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("c1")})
	seedSuggestion(t, root, "s1")

	res := runBin(t, "", "applied", "--out", root, "totally-bogus")
	if res.exit == 0 {
		t.Errorf("bogus suggestion id should exit non-zero; got 0\nstdout: %s", res.stdout)
	}
	if !strings.Contains(res.stderr, "totally-bogus") {
		t.Errorf("stderr should name the unknown id; got: %s", res.stderr)
	}
	if got := appliedMarks(t, root); len(got) != 0 {
		t.Errorf("bogus id must NOT be recorded; applied.jsonl has: %v", got)
	}
}

func TestApplied_NoSuggestionsErrors(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("c1")}) // comments, but no suggestions
	if err := runApplied([]string{"--out", root, "s1"}); err == nil {
		t.Error("applied with no suggestions in the store should error (likely wrong --out)")
	}
}
