package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/review"
)

func revertedMarks(t *testing.T, root string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".prereview", review.RevertedFileName))
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

// TestRunReverted_AppendsAndValidates: `reverted` records into its own reverted.jsonl
// (validated against suggestions, like `applied`), so the applied/reverted counts can
// net out (#159 M4.2).
func TestRunReverted_AppendsAndValidates(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("c1")})
	seedSuggestion(t, root, "s1")

	if err := runReverted([]string{"--out", root, "s1"}); err != nil {
		t.Fatalf("runReverted: %v", err)
	}
	if got := revertedMarks(t, root); len(got) != 1 || !strings.Contains(got[0], `"id":"s1"`) {
		t.Fatalf("want s1 recorded in reverted.jsonl; got %v", got)
	}

	res := runBin(t, "", "reverted", "--out", root, "totally-bogus")
	if res.exit == 0 {
		t.Errorf("bogus suggestion id should exit non-zero; got 0\nstdout: %s", res.stdout)
	}
	if got := revertedMarks(t, root); len(got) != 1 {
		t.Errorf("bogus id must NOT be recorded; reverted.jsonl has: %v", got)
	}
}
