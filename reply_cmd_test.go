package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/review"
)

// agentReplies reads the agent-replies.jsonl thread entries under <root>/.prereview.
func agentReplies(t *testing.T, root string) []review.ThreadEntry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".prereview", review.AgentRepliesFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []review.ThreadEntry
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var e review.ThreadEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parse reply line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

// seedSuggestion appends a minimal valid suggestion to suggestions.jsonl so a reply
// can target it (a reply targets a comment OR a suggestion).
func seedSuggestion(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, ".prereview")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"id":"` + id + `","file":"f","from_line":1,"to_line":1,"original":"a","proposed":"b"}` + "\n"
	f, err := os.OpenFile(filepath.Join(dir, review.SuggestionFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func TestRunReply_AppendsEntry(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []csv.Row{row("c1")})

	if err := runReply([]string{"--out", dir, "--body", "did the thing", "c1"}); err != nil {
		t.Fatalf("runReply: %v", err)
	}
	entries := agentReplies(t, dir)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.TargetID != "c1" || e.Author != review.AuthorAgent || e.Body != "did the thing" || e.At == 0 {
		t.Errorf("bad entry: %+v", e)
	}

	// Append-only: a second reply adds a line, never rewrites.
	if err := runReply([]string{"--out", dir, "--body", "and more", "c1"}); err != nil {
		t.Fatalf("runReply 2: %v", err)
	}
	if got := agentReplies(t, dir); len(got) != 2 {
		t.Fatalf("want 2 entries after append, got %d", len(got))
	}
}

func TestRunReply_EmptyBodyErrors(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []csv.Row{row("c1")})
	if err := runReply([]string{"--out", dir, "c1"}); err == nil {
		t.Error("empty body should error")
	}
}

func TestRunReply_RequiresExactlyOneID(t *testing.T) {
	dir := t.TempDir()
	seedStore(t, dir, []csv.Row{row("c1"), row("c2")})
	if err := runReply([]string{"--out", dir, "--body", "x", "c1", "c2"}); err == nil {
		t.Error("two ids should error (reply takes exactly one)")
	}
}

func TestReply_BogusIDExitsNonZero(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("real-1")})

	res := runBin(t, "", "reply", "--out", root, "--body", "x", "totally-bogus-xyz")
	if res.exit == 0 {
		t.Errorf("bogus id should exit non-zero; got 0\nstdout: %s", res.stdout)
	}
	if !strings.Contains(res.stderr, "totally-bogus-xyz") {
		t.Errorf("stderr should name the unknown id; got: %s", res.stderr)
	}
	if got := agentReplies(t, root); len(got) != 0 {
		t.Errorf("bogus id must NOT be recorded; agent-replies.jsonl has: %+v", got)
	}
}

func TestReply_ValidCommentID(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("real-1")})
	res := runBin(t, "", "reply", "--out", root, "--body", "handled it", "real-1")
	if res.exit != 0 {
		t.Fatalf("valid comment id should exit 0; got %d\nstderr: %s", res.exit, res.stderr)
	}
	if got := agentReplies(t, root); len(got) != 1 || got[0].TargetID != "real-1" {
		t.Errorf("expected reply on real-1; got: %+v", got)
	}
}

func TestReply_ValidSuggestionID(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("c1")})
	seedSuggestion(t, root, "s1")
	res := runBin(t, "", "reply", "--out", root, "--body", "revised as asked", "s1")
	if res.exit != 0 {
		t.Fatalf("valid suggestion id should exit 0; got %d\nstderr: %s", res.exit, res.stderr)
	}
	if got := agentReplies(t, root); len(got) != 1 || got[0].TargetID != "s1" {
		t.Errorf("expected reply on suggestion s1; got: %+v", got)
	}
}

func TestReply_BodyFromStdin(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("c1")})
	res := runBin(t, "note from stdin\n", "reply", "--out", root, "--file", "-", "c1")
	if res.exit != 0 {
		t.Fatalf("stdin body should exit 0; got %d\nstderr: %s", res.exit, res.stderr)
	}
	got := agentReplies(t, root)
	if len(got) != 1 || got[0].Body != "note from stdin" {
		t.Errorf("expected stdin body trimmed; got: %+v", got)
	}
}
