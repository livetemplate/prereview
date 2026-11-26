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
		{"id taken by a comment", strings.Replace(askPayload, `"id":"q-refresh"`, `"id":"c1"`, 1), "already a comment"},
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
	if res := runBin(t, "", "done", "--out", root, "q-refresh", "c1"); res.exit != 0 || !strings.Contains(res.stdout, "marked 1 comment(s) and 1 question set(s)") {
		t.Fatalf("done on a set and a comment: exit %d, stdout %q, %s", res.exit, res.stdout, res.stderr)
	}
	if got := strings.Join(processedIDs(t, root), "\n"); !strings.Contains(got, `"id":"q-refresh"`) || !strings.Contains(got, `"id":"c1"`) {
		t.Errorf("processed = %s, want q-refresh and c1", got)
	}
	if res := runBin(t, "", "done", "--out", root, "q-refresh", "bogus"); res.exit == 0 || !strings.Contains(res.stderr, "bogus") {
		t.Errorf("an unknown id next to a set id must still fail: exit %d, %s", res.exit, res.stderr)
	}
}

// TestQuestions_ListsWhatToActOn: `prereview questions --json` is the polling
// read of a snapshot's questions: nothing until the reviewer sends a set, then
// the set with its labels, and --all lists unanswered sets too.
func TestQuestions_ListsWhatToActOn(t *testing.T) {
	root := t.TempDir()
	if res := askWith(t, root, askPayload); res.exit != 0 {
		t.Fatalf("ask: %s", res.stderr)
	}
	read := func(args ...string) []review.StreamQuestion {
		t.Helper()
		res := runBin(t, "", append([]string{"questions", "--out", root, "--json"}, args...)...)
		if res.exit != 0 {
			t.Fatalf("questions: exit %d, %s", res.exit, res.stderr)
		}
		var qs []review.StreamQuestion
		if err := json.Unmarshal([]byte(res.stdout), &qs); err != nil {
			t.Fatalf("questions --json is not a JSON array: %v\n%s", err, res.stdout)
		}
		return qs
	}
	if qs := read(); len(qs) != 0 {
		t.Fatalf("an unanswered set is listed as work: %+v", qs)
	}
	if qs := read("--all"); len(qs) != 1 || qs[0].ID != "q-refresh" || len(qs[0].Answers) != 0 {
		t.Fatalf("--all = %+v, want the unanswered set", qs)
	}
	ans := `{"set_id":"q-refresh","step_id":"on-fail","picked":[1],"sent":true,"at":1}` + "\n"
	if err := os.WriteFile(filepath.Join(root, ".prereview", review.QuestionAnswersFileName), []byte(ans), 0o644); err != nil {
		t.Fatal(err)
	}
	if qs := read(); len(qs) != 1 || qs[0].Answers[0].Picked[0] != "Sign out at once" {
		t.Fatalf("after Submit = %+v, want the set with its label", qs)
	}
}
