package review

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadThreads_MergesAndSorts writes the agent + reviewer sidecars out of order
// and asserts loadThreads merges them into one chronological slice.
func TestLoadThreads_MergesAndSorts(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, CommentsFileName)
	// reviewer prompt at t=100, agent reply at t=200 — written to separate files.
	writeLines(t, ReviewerRepliesPath(csvPath),
		`{"target_id":"c1","author":"reviewer","body":"please fix","at":100}`)
	writeLines(t, AgentRepliesPath(csvPath),
		`{"target_id":"c1","author":"agent","body":"fixed","at":200}`,
		`{"target_id":"c1","author":"agent","body":"earlier note","at":50}`,
		``, // blank line tolerated
		`{"garbage without a target`) // torn line skipped

	got := loadThreads(csvPath)
	if len(got) != 3 {
		t.Fatalf("want 3 entries (torn+blank skipped), got %d: %+v", len(got), got)
	}
	wantOrder := []int64{50, 100, 200}
	for i, e := range got {
		if e.At != wantOrder[i] {
			t.Errorf("entry %d: At=%d, want %d (sorted); got order %+v", i, e.At, wantOrder[i], got)
		}
	}
}

// TestLoadThreads_TieBreaksAgentAfterReviewer: on an exact-nanosecond tie, the agent
// reply must sort AFTER the reviewer prompt it answers.
func TestLoadThreads_TieBreaksAgentAfterReviewer(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, CommentsFileName)
	writeLines(t, AgentRepliesPath(csvPath), `{"target_id":"c1","author":"agent","body":"resp","at":100}`)
	writeLines(t, ReviewerRepliesPath(csvPath), `{"target_id":"c1","author":"reviewer","body":"ask","at":100}`)

	got := loadThreads(csvPath)
	if len(got) != 2 || got[0].Author != AuthorReviewer || got[1].Author != AuthorAgent {
		t.Fatalf("tie should sort reviewer-before-agent; got %+v", got)
	}
}

func TestThreads_GroupByTarget(t *testing.T) {
	s := PrereviewState{ThreadEntries: []ThreadEntry{
		{TargetID: "a", Author: AuthorAgent, Body: "1", At: 1},
		{TargetID: "b", Author: AuthorAgent, Body: "2", At: 2},
		{TargetID: "a", Author: AuthorAgent, Body: "3", At: 3},
	}}
	m := s.Threads()
	if len(m["a"]) != 2 || len(m["b"]) != 1 {
		t.Fatalf("grouping wrong: a=%d b=%d", len(m["a"]), len(m["b"]))
	}
	if m["a"][0].Body != "1" || m["a"][1].Body != "3" {
		t.Errorf("group 'a' lost order: %+v", m["a"])
	}
	if (PrereviewState{}).Threads() != nil {
		t.Error("empty state should return nil threads")
	}
}

func TestThreadEntry_When(t *testing.T) {
	if (ThreadEntry{At: 0}).When() != "" {
		t.Error("zero At should render empty time")
	}
	if (ThreadEntry{At: 1_600_000_000_000_000_000}).When() == "" {
		t.Error("non-zero At should render a time")
	}
}
