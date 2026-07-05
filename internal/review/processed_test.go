package review

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProcessed(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadMarkCounts covers the tolerance contract: a missing file is empty,
// blank/torn lines are skipped, valid marks are COUNTED per id (a duplicate
// increments — the re-enqueue math depends on the count, not a set).
func TestLoadMarkCounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProcessedFileName)

	if got := loadMarkCounts(path); len(got) != 0 {
		t.Fatalf("missing file: want empty, got %v", got)
	}

	writeProcessed(t, path, ""+
		`{"id":"a","at":"2026-07-02T10:00:00Z"}`+"\n"+
		"\n"+ // blank line — skip
		`{"id":"b"}`+"\n"+
		`{not json`+"\n"+ // torn line — skip, next is fine
		`{"id":"a"}`+"\n"+ // duplicate — counted
		`{"at":"2026-07-02T10:00:00Z"}`+"\n") // no id — skip

	got := loadMarkCounts(path)
	if got["a"] != 2 || got["b"] != 1 {
		t.Fatalf("want a=2 b=1, got %v", got)
	}
}

// TestApplyProcessed flips Processed on matching comments only, by id, without
// disturbing the rest of the comment.
func TestApplyProcessed(t *testing.T) {
	dir := t.TempDir()
	c := &PrereviewController{CSVPath: filepath.Join(dir, "comments.csv")}
	writeProcessed(t, c.processedPath(), `{"id":"a"}`+"\n"+`{"id":"c"}`+"\n")

	st := PrereviewState{Comments: []Comment{
		{ID: "a"}, {ID: "b"}, {ID: "c", Resolved: true},
	}}
	c.applyProcessed(&st)

	if !st.Comments[0].Processed {
		t.Error("a should be processed")
	}
	if st.Comments[1].Processed {
		t.Error("b should NOT be processed")
	}
	if !st.Comments[2].Processed || !st.Comments[2].Resolved {
		t.Error("c should be processed and stay resolved")
	}
}

// TestAgentSignalFingerprint pins that appending a processed marker changes the
// watcher's combined fingerprint even when llm-status.json is untouched — that's
// what makes a `prereview processed` write fan out a live badge update.
func TestAgentSignalFingerprint(t *testing.T) {
	dir := t.TempDir()
	c := &PrereviewController{CSVPath: filepath.Join(dir, "comments.csv")}

	before := c.agentSignalFingerprint()
	writeProcessed(t, c.processedPath(), `{"id":"a"}`+"\n")
	after := c.agentSignalFingerprint()

	if before == after {
		t.Fatalf("fingerprint unchanged after processed.jsonl write: %q", after)
	}
}
