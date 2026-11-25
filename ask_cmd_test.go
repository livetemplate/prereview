package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/review"
)

// askPayload is a minimal well-formed question set, as JSON.
const askPayload = `{"id":"q-refresh","file":"auth/session.go","from_line":41,"to_line":42,
 "steps":[{"id":"on-fail","prompt":"When a token refresh fails, what should happen?",
   "options":[{"label":"Retry twice, then sign out"},{"label":"Sign out at once"}],"recommended":0}]}`

// askWith runs `prereview ask` with payload against store root and extra args.
func askWith(t *testing.T, root, payload string, extra ...string) runResult {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ask.json")
	if err := os.WriteFile(p, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	return runBin(t, "", append([]string{"ask", "--out", root, "--file", p}, extra...)...)
}

func TestAsk_AppendsAndReports(t *testing.T) {
	root := t.TempDir()
	res := askWith(t, root, askPayload)
	if res.exit != 0 {
		t.Fatalf("exit %d, stderr: %s", res.exit, res.stderr)
	}
	if !strings.Contains(res.stdout, "asked q-refresh (1 question(s)) on auth/session.go:41-42") {
		t.Errorf("stdout = %q", res.stdout)
	}
	sets := review.LoadQuestionSets(filepath.Join(root, ".prereview", review.CommentsFileName))
	if len(sets) != 1 || sets[0].Side != "new" || sets[0].Kind != review.QuestionKindLine {
		t.Fatalf("stored sets = %+v, want one normalized set", sets)
	}
}

// TestAsk_CheckWritesNothing: --check validates and reports, nothing more.
func TestAsk_CheckWritesNothing(t *testing.T) {
	root := t.TempDir()
	res := askWith(t, root, askPayload, "--check")
	if res.exit != 0 || !strings.Contains(res.stdout, "would ask q-refresh") {
		t.Fatalf("exit %d, stdout %q, stderr %s", res.exit, res.stdout, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(root, ".prereview", review.QuestionsFileName)); !os.IsNotExist(err) {
		t.Fatalf("--check wrote questions.jsonl (stat err %v)", err)
	}
}

// TestAsk_RefusesBadPayloads: nothing is written when any set is invalid, or
// names a comment the review doesn't have.
func TestAsk_RefusesBadPayloads(t *testing.T) {
	for _, tc := range []struct{ name, payload, want string }{
		{"one option", strings.Replace(askPayload, `,{"label":"Sign out at once"}`, "", 1), "2 to 4 options"},
		{"bad second set", "[" + askPayload + `,{"file":"a.go","from_line":1,"steps":[]}]`, "question set 2"},
		{"unknown for", strings.Replace(askPayload, `"file"`, `"for":"nope","file"`, 1), "unknown comment id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			seedStore(t, root, []csv.Row{row("c1")})
			res := askWith(t, root, tc.payload)
			if res.exit == 0 || !strings.Contains(res.stderr, tc.want) {
				t.Fatalf("exit %d, stderr %q, want an error mentioning %q", res.exit, res.stderr, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, ".prereview", review.QuestionsFileName)); !os.IsNotExist(err) {
				t.Fatal("a refused payload was written")
			}
		})
	}
}

// TestAsk_DoneAndReplyTakeSetIDs: the agent closes and replies on a set by id,
// like a comment.
func TestAsk_DoneAndReplyTakeSetIDs(t *testing.T) {
	root := t.TempDir()
	seedStore(t, root, []csv.Row{row("c1")})
	if res := askWith(t, root, askPayload); res.exit != 0 {
		t.Fatalf("ask: %s", res.stderr)
	}
	if res := runBin(t, "", "reply", "--out", root, "--body", "Added two retries.", "q-refresh"); res.exit != 0 {
		t.Fatalf("reply on a set: exit %d, %s", res.exit, res.stderr)
	}
	if res := runBin(t, "", "done", "--out", root, "q-refresh", "c1"); res.exit != 0 {
		t.Fatalf("done on a set and a comment: exit %d, %s", res.exit, res.stderr)
	}
	if got := strings.Join(processedIDs(t, root), "\n"); !strings.Contains(got, `"id":"q-refresh"`) || !strings.Contains(got, `"id":"c1"`) {
		t.Errorf("processed = %s, want q-refresh and c1", got)
	}
	if res := runBin(t, "", "done", "--out", root, "q-refresh", "bogus"); res.exit == 0 || !strings.Contains(res.stderr, "bogus") {
		t.Errorf("an unknown id next to a set id must still fail: exit %d, %s", res.exit, res.stderr)
	}
}
