package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/review"
)

// quizPayload is a minimal well-formed quiz, as JSON, for the CLI tests.
const quizPayload = `{
  "file": "a.go",
  "questions": [
    {"probe": "consequence", "prompt": "what breaks?",
     "options": ["nothing", "rows are deleted"], "answer": 1,
     "why": "it is a write-back buffer", "from_line": 10, "to_line": 12}
  ]
}`

// runQuizWith writes payload to a temp file and submits it against store dir out.
func runQuizWith(t *testing.T, out, payload string) error {
	t.Helper()
	p := filepath.Join(t.TempDir(), "quiz.json")
	if err := os.WriteFile(p, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	return runQuiz([]string{"--out", out, "--file", p})
}

// quizLines reads the appended quiz file back as decoded quizzes.
func quizLines(t *testing.T, out string) []review.Quiz {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, ".prereview", review.QuizFileName))
	if err != nil {
		return nil
	}
	var got []review.Quiz
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var q review.Quiz
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			t.Fatalf("the CLI must write valid JSONL the loader can read back, got %q: %v", line, err)
		}
		got = append(got, q)
	}
	return got
}

func TestRunQuiz_AppendsAndMintsIDs(t *testing.T) {
	out := t.TempDir()
	if err := runQuizWith(t, out, quizPayload); err != nil {
		t.Fatalf("a well-formed quiz must submit cleanly, got: %v", err)
	}
	got := quizLines(t, out)
	if len(got) != 1 {
		t.Fatalf("expected 1 quiz on disk, got %d", len(got))
	}
	if got[0].ID == "" || got[0].Questions[0].ID == "" {
		t.Error("ids must be minted on submit, so the quiz can be revised and answers recorded")
	}
	if got[0].Questions[0].Side != "new" {
		t.Errorf("side must default to \"new\", got %q", got[0].Questions[0].Side)
	}
}

// Append, never rewrite — the same ownership rule as suggestions.jsonl, which is
// what keeps the agent's file from racing the server's comments.csv.
func TestRunQuiz_SecondSubmitAppendsRatherThanReplacing(t *testing.T) {
	out := t.TempDir()
	if err := runQuizWith(t, out, quizPayload); err != nil {
		t.Fatal(err)
	}
	if err := runQuizWith(t, out, strings.Replace(quizPayload, `"a.go"`, `"b.go"`, 1)); err != nil {
		t.Fatal(err)
	}
	if got := quizLines(t, out); len(got) != 2 {
		t.Fatalf("a second submit must append, not overwrite; got %d line(s)", len(got))
	}
}

func TestRunQuiz_AcceptsArrayAndJSONL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"array", "[" + quizPayload + "," + quizPayload + "]"},
		{"jsonl", quizPayload + "\n" + quizPayload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			if err := runQuizWith(t, out, tc.payload); err != nil {
				t.Fatalf("%s payload must be accepted, got: %v", tc.name, err)
			}
			if got := quizLines(t, out); len(got) != 2 {
				t.Errorf("expected 2 quizzes from a %s payload, got %d", tc.name, len(got))
			}
		})
	}
}

// The structural contract, enforced at the CLI boundary. These are the failures a
// user-authored prompt could otherwise introduce, so the verb has to catch them
// whatever produced the payload.
func TestRunQuiz_RejectsMalformedQuizNamingTheQuestion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		wantSub string
	}{
		{
			"answer out of range",
			strings.Replace(quizPayload, `"answer": 1`, `"answer": 7`, 1),
			"out of range",
		},
		{
			"missing explanation",
			strings.Replace(quizPayload, `"why": "it is a write-back buffer", `, "", 1),
			"missing \"why\"",
		},
		{
			"unknown probe",
			strings.Replace(quizPayload, `"consequence"`, `"vibes"`, 1),
			"unknown probe",
		},
		{
			"non-decision without an anchor",
			strings.Replace(quizPayload, `"from_line": 10`, `"from_line": 0`, 1),
			"line anchor",
		},
		{"empty payload", "   ", "empty payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			err := runQuizWith(t, out, tc.payload)
			if err == nil {
				t.Fatal("a malformed quiz must fail loudly rather than record garbage")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("the error must name the problem (%q) so the agent can fix it, got: %v", tc.wantSub, err)
			}
			if got := quizLines(t, out); len(got) != 0 {
				t.Errorf("a rejected payload must write nothing, got %d line(s)", len(got))
			}
		})
	}
}

// Validation covers the WHOLE payload before anything is written, so one bad quiz
// in a batch can't leave the good ones half-committed on disk.
func TestRunQuiz_RejectsTheWholeBatchIfAnyQuizIsBad(t *testing.T) {
	out := t.TempDir()
	bad := strings.Replace(quizPayload, `"answer": 1`, `"answer": 9`, 1)
	err := runQuizWith(t, out, "["+quizPayload+","+bad+"]")
	if err == nil {
		t.Fatal("a batch containing a malformed quiz must be rejected")
	}
	if !strings.Contains(err.Error(), "quiz 2") {
		t.Errorf("the error must identify WHICH quiz failed, got: %v", err)
	}
	if got := quizLines(t, out); len(got) != 0 {
		t.Errorf("the good quiz must not be written when a later one fails — a partial\n"+
			"batch would leave the reviewer a quiz the agent thinks it never submitted; got %d line(s)", len(got))
	}
}

// A `decision` question may be about something absent, which has no lines to
// point at. This is the one probe allowed through without an anchor.
func TestRunQuiz_AcceptsAnchorlessDecision(t *testing.T) {
	out := t.TempDir()
	payload := `{"file":"a.go","questions":[
	  {"probe":"decision","prompt":"what did you decide on your own?",
	   "options":["added a retry","skipped the error-path test"],"answer":1,
	   "why":"the request never mentioned tests","from_line":0}]}`
	if err := runQuizWith(t, out, payload); err != nil {
		t.Fatalf("a decision about an omission must be accepted without an anchor, got: %v", err)
	}
	got := quizLines(t, out)
	if len(got) != 1 || got[0].Questions[0].FromLine != 0 {
		t.Errorf("the anchorless decision must round-trip with from_line 0, got %+v", got)
	}
}
