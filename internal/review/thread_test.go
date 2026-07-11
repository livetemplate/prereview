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

// TestThreadActionable is the #149 unread-model truth table: a fresh comment is
// actionable iff unresolved; a thread the reviewer spoke last on re-surfaces even when
// resolved; a thread the agent spoke last on drops out (handled, awaiting the reviewer).
func TestThreadActionable(t *testing.T) {
	agentLast := []ThreadEntry{{Author: AuthorReviewer, At: 1}, {Author: AuthorAgent, At: 2}}
	reviewerLast := []ThreadEntry{{Author: AuthorAgent, At: 1}, {Author: AuthorReviewer, At: 2}}
	cases := []struct {
		name     string
		resolved bool
		thread   []ThreadEntry
		want     bool
	}{
		{"fresh unresolved", false, nil, true},
		{"fresh resolved", true, nil, false},
		{"agent-last, unresolved (handled, awaiting reviewer)", false, agentLast, false},
		{"agent-last, resolved", true, agentLast, false},
		{"reviewer-last, unresolved", false, reviewerLast, true},
		{"reviewer-last, resolved (re-surface)", true, reviewerLast, true},
	}
	for _, c := range cases {
		if got := threadActionable(c.resolved, c.thread); got != c.want {
			t.Errorf("%s: threadActionable=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestActionableComments_UnreadOverlay checks the wire: a resolved comment with an
// unread reviewer reply re-appears in the snapshot, carrying its thread; an
// agent-last one does not.
func TestActionableComments_UnreadOverlay(t *testing.T) {
	comments := []Comment{
		{ID: "resurface", Resolved: true},
		{ID: "handled", Resolved: false},
		{ID: "fresh", Resolved: false},
	}
	threads := map[string][]ThreadEntry{
		"resurface": {{Author: AuthorAgent, At: 1}, {Author: AuthorReviewer, Body: "one more thing", At: 2}},
		"handled":   {{Author: AuthorReviewer, At: 1}, {Author: AuthorAgent, At: 2}},
	}
	got := actionableComments(comments, threads)
	ids := map[string]StreamComment{}
	for _, c := range got {
		ids[c.ID] = c
	}
	if _, ok := ids["resurface"]; !ok {
		t.Error("a resolved comment with an unread reviewer reply must re-surface")
	}
	if len(ids["resurface"].Thread) != 2 || ids["resurface"].Thread[1].Author != AuthorReviewer {
		t.Errorf("re-surfaced comment must carry its thread; got %+v", ids["resurface"].Thread)
	}
	if _, ok := ids["handled"]; ok {
		t.Error("an unresolved comment the agent replied to last must NOT be actionable (awaiting reviewer)")
	}
	if _, ok := ids["fresh"]; !ok {
		t.Error("a fresh unresolved comment must be actionable")
	}
}

// TestActionableDecisions_UnreadOverlay: a suggestion with an unread reviewer reply is
// actionable even when undecided, and carries its thread.
func TestActionableDecisions_UnreadOverlay(t *testing.T) {
	sugs := []Suggestion{{ID: "s1"}, {ID: "s2"}}
	decided := map[string]SuggestionDecision{} // neither decided
	threads := map[string][]ThreadEntry{
		"s1": {{Author: AuthorAgent, At: 1}, {Author: AuthorReviewer, Body: "tweak it", At: 2}},
	}
	got := actionableDecisions(sugs, decided, threads, nil)
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("only the reviewer-replied suggestion should be actionable; got %+v", got)
	}
	if len(got[0].Thread) != 2 || got[0].Thread[1].Author != AuthorReviewer {
		t.Errorf("actionable suggestion must carry its thread; got %+v", got[0].Thread)
	}
}

// TestAwaitingLines maps the awaiting-agent set onto the diff rows carrying those
// comments, so the #151 badge can show an unread dot on the right row.
func TestAwaitingLines(t *testing.T) {
	s := PrereviewState{
		SelectedFile: "f",
		Comments: []Comment{
			{ID: "c1", File: "f", ToLine: 4, Side: "new"},
			{ID: "c2", File: "f", ToLine: 9, Side: "new"},
		},
		ThreadEntries: []ThreadEntry{
			{TargetID: "c1", Author: AuthorAgent, At: 1},
			{TargetID: "c1", Author: AuthorReviewer, At: 2}, // c1 awaiting
		},
	}
	lines := s.AwaitingLines()
	if !lines["4-new"] {
		t.Errorf("line 4 (c1, reviewer-last) should be awaiting; got %v", lines)
	}
	if lines["9-new"] {
		t.Errorf("line 9 (c2, no thread) should NOT be awaiting; got %v", lines)
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
