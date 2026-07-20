package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validQuestion is a minimal question that passes ValidateQuiz, so each test can
// mutate exactly the one field it is about and the failure is unambiguous.
func validQuestion() Question {
	return Question{
		ID:       "q1",
		Probe:    ProbeConsequence,
		Prompt:   "What breaks if the buffer is filtered at load?",
		Options:  []string{"Nothing", "Rows are deleted from disk"},
		Answer:   1,
		Why:      "It is a write-back buffer.",
		FromLine: 10,
		ToLine:   12,
		Side:     "new",
	}
}

func validQuiz() Quiz {
	return Quiz{ID: "z1", File: "a.go", Questions: []Question{validQuestion()}}
}

func TestValidateQuiz_AcceptsAWellFormedQuiz(t *testing.T) {
	if err := ValidateQuiz(validQuiz()); err != nil {
		t.Fatalf("a well-formed quiz must validate, got: %v", err)
	}
}

// The structural half of the question contract. Each case is a way a generated
// quiz can be useless or malformed; all of them must be caught HERE, because this
// is the only checkpoint that runs no matter which prompt authored the quiz.
func TestValidateQuiz_RejectsMalformedQuestions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Quiz)
		wantSub string
	}{
		{"no file", func(q *Quiz) { q.File = "" }, "file"},
		{"no questions", func(q *Quiz) { q.Questions = nil }, "no questions"},
		{"missing question id", func(q *Quiz) { q.Questions[0].ID = "" }, "missing \"id\""},
		{"duplicate question id", func(q *Quiz) {
			q.Questions = append(q.Questions, validQuestion())
		}, "duplicate id"},
		{"unknown probe", func(q *Quiz) { q.Questions[0].Probe = "vibes" }, "unknown probe"},
		{"no prompt", func(q *Quiz) { q.Questions[0].Prompt = "" }, "missing \"prompt\""},
		{"single option", func(q *Quiz) { q.Questions[0].Options = []string{"only one"} }, "at least 2 options"},
		{"empty option", func(q *Quiz) { q.Questions[0].Options = []string{"a", ""} }, "option 2 is empty"},
		{"answer past the end", func(q *Quiz) { q.Questions[0].Answer = 2 }, "out of range"},
		{"negative answer", func(q *Quiz) { q.Questions[0].Answer = -1 }, "out of range"},
		// The explanation is the learning payload — a quiz that only scores you
		// teaches nothing, so an empty `why` is a hard failure, not a warning.
		{"no explanation", func(q *Quiz) { q.Questions[0].Why = "" }, "missing \"why\""},
		{"inverted range", func(q *Quiz) { q.Questions[0].ToLine = 1 }, "precedes"},
		{"negative from_line", func(q *Quiz) { q.Questions[0].FromLine = -3 }, ">= 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := validQuiz()
			tc.mutate(&q)
			err := ValidateQuiz(q)
			if err == nil {
				t.Fatalf("%s must be rejected — this is the only check that runs for a\n"+
					"user-authored prompt, so anything it lets through reaches the reviewer", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error should name the problem (%q) so the agent can fix it, got: %v", tc.wantSub, err)
			}
		})
	}
}

// Anchorlessness is a privilege of `decision`, whose subject can be an omission
// with no lines to point at. If any probe could drop its anchor, the server-side
// grounding check would be trivially bypassable — so this pair of cases is what
// keeps that check meaningful.
func TestValidateQuiz_OnlyDecisionMayOmitItsAnchor(t *testing.T) {
	t.Run("decision may", func(t *testing.T) {
		q := validQuiz()
		q.Questions[0].Probe = ProbeDecision
		q.Questions[0].FromLine = 0
		q.Questions[0].ToLine = 0
		if err := ValidateQuiz(q); err != nil {
			t.Fatalf("a decision about an omission has nothing to anchor to; it must be allowed, got: %v", err)
		}
	})
	for _, probe := range []string{ProbeChangeType, ProbeLocalization, ProbeConsequence, ProbeRationale} {
		t.Run(probe+" may not", func(t *testing.T) {
			q := validQuiz()
			q.Questions[0].Probe = probe
			q.Questions[0].FromLine = 0
			q.Questions[0].ToLine = 0
			if err := ValidateQuiz(q); err == nil {
				t.Fatalf("%s must carry a line anchor — letting it go anchorless would let any\n"+
					"question skip the grounding check by claiming to be about an absence", probe)
			}
		})
	}
}

func TestNormalizeQuiz_FillsDefaults(t *testing.T) {
	q := NormalizeQuiz(Quiz{
		File:      "a.go",
		Questions: []Question{{Probe: ProbeRationale, Prompt: "why?", Options: []string{"a", "b"}, Why: "because", FromLine: 5}},
	})
	if q.ID == "" {
		t.Error("a quiz without an id must get one minted, or it can't be revised later")
	}
	if q.Questions[0].ID == "" {
		t.Error("question ids are how answers are recorded; one must be minted")
	}
	if got := q.Questions[0].Side; got != "new" {
		t.Errorf("side must default to \"new\", got %q", got)
	}
	if got := q.Questions[0].ToLine; got != 5 {
		t.Errorf("an omitted to_line must collapse to from_line, got %d", got)
	}
}

// A `decision` question about an omission must survive normalization with its
// zero from_line intact — if NormalizeQuiz "helpfully" defaulted it to 1, the
// question would silently acquire a bogus anchor.
func TestNormalizeQuiz_KeepsAnchorlessDecisionAnchorless(t *testing.T) {
	q := NormalizeQuiz(Quiz{
		File:      "a.go",
		Questions: []Question{{Probe: ProbeDecision, Prompt: "what did you decide?", Options: []string{"a", "b"}, Why: "because"}},
	})
	if !q.Questions[0].Anchorless() {
		t.Errorf("an anchorless decision must stay anchorless through normalization, got from_line=%d",
			q.Questions[0].FromLine)
	}
}

func TestLoadQuizzes_MissingFileIsNotAnError(t *testing.T) {
	if got := loadQuizzes(filepath.Join(t.TempDir(), "nope.jsonl")); got != nil {
		t.Errorf("a missing quiz file means no quizzes, not a failure; got %v", got)
	}
}

// The agent appends, so the file accumulates revisions of the same id. Last write
// must win (mirroring suggestions) or a corrected quiz would never replace the
// wrong one.
func TestLoadQuizzes_LastWritePerIDWinsAndOrderIsStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, QuizFileName)
	writeQuizFile(t, path, `{"id":"a","file":"a.go","questions":[{"id":"q1","prompt":"first"}]}
{"id":"b","file":"b.go","questions":[]}
{"id":"a","file":"a.go","questions":[{"id":"q1","prompt":"revised"}]}
`)
	got := loadQuizzes(path)
	if len(got) != 2 {
		t.Fatalf("two distinct ids must collapse to two quizzes, got %d", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("order must stay first-seen so the UI doesn't reshuffle each poll, got %s,%s", got[0].ID, got[1].ID)
	}
	if got[0].Questions[0].Prompt != "revised" {
		t.Errorf("re-appending an id must revise it (last write wins), got %q", got[0].Questions[0].Prompt)
	}
}

// The file is agent-appended and may be read mid-write, so a torn line must never
// take down the review.
func TestLoadQuizzes_SkipsTornAndIdlessLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), QuizFileName)
	writeQuizFile(t, path, `{"id":"a","file":"a.go","questions":[]}
{"id":"b","file":  <-- torn
{"file":"c.go","questions":[]}
{"id":"d","questions":[]}

{"id":"e","file":"e.go","questions":[]}
`)
	got := loadQuizzes(path)
	if len(got) != 2 {
		t.Fatalf("torn, id-less, file-less and blank lines must be skipped, keeping the 2 good ones; got %d", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "e" {
		t.Errorf("expected the two well-formed quizzes a and e, got %s,%s", got[0].ID, got[1].ID)
	}
}

// quiz.jsonl is agent-owned and append-only, so a line can arrive without going
// through `prereview quiz` (which would have defaulted Side). The loader must
// default it too — a question with no side puts its jump link on the wrong row of
// a modified line, which is the exact failure Side exists to prevent.
func TestLoadQuizzes_DefaultsSideOnDirectlyAppendedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), QuizFileName)
	writeQuizFile(t, path, `{"id":"a","file":"a.go","questions":[{"id":"q1","from_line":4},{"id":"q2","from_line":9,"side":"old"}]}
`)
	got := loadQuizzes(path)
	if len(got) != 1 {
		t.Fatalf("expected 1 quiz, got %d", len(got))
	}
	if s := got[0].Questions[0].Side; s != "new" {
		t.Errorf("a question with no side must load as \"new\", got %q", s)
	}
	if s := got[0].Questions[1].Side; s != "old" {
		t.Errorf("an explicit side must be preserved, got %q", s)
	}
}

func TestLoadQuizAnswers_KeyedByQuizAndQuestion(t *testing.T) {
	path := filepath.Join(t.TempDir(), QuizAnswersFileName)
	writeQuizFile(t, path, `{"quiz_id":"z1","question_id":"q1","choice":2}
{"quiz_id":"z2","question_id":"q1","choice":0}
{"quiz_id":"z1","question_id":"q1","choice":3}
`)
	got := loadQuizAnswers(path)
	if len(got) != 2 {
		t.Fatalf("question ids are only unique WITHIN a quiz, so z1/q1 and z2/q1 are different answers; got %d", len(got))
	}
	if got[answerKey("z1", "q1")].Choice != 3 {
		t.Errorf("the latest answer for a question must win, got %d", got[answerKey("z1", "q1")].Choice)
	}
}

// A retake writes choice=-1 to clear an answer; load must drop it rather than
// surfacing a negative index that would panic an options lookup.
func TestLoadQuizAnswers_NegativeChoiceClearsTheAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), QuizAnswersFileName)
	writeQuizFile(t, path, `{"quiz_id":"z1","question_id":"q1","choice":2}
{"quiz_id":"z1","question_id":"q1","choice":-1}
`)
	if got := loadQuizAnswers(path); len(got) != 0 {
		t.Errorf("a cleared answer must not survive the load, got %v", got)
	}
}

func TestLoadQuizPrompts_ShipsBuiltinsWithoutAUserDir(t *testing.T) {
	got := LoadQuizPrompts("")
	if len(got) == 0 {
		t.Fatal("the built-in quiz prompt must ship embedded, so the feature works with no user config")
	}
	for _, p := range got {
		if p.Body == "" || p.Title == "" {
			t.Errorf("prompt %q must have both a title and a body", p.Slug)
		}
	}
}

// User-authored quiz prompts are first-class: a user file overrides a built-in of
// the same slug and new slugs are added.
func TestLoadQuizPrompts_UserOverlayOverridesAndExtends(t *testing.T) {
	dir := t.TempDir()
	writeQuizFile(t, filepath.Join(dir, "comprehension.md"), "# Mine\n\nmy replacement body\n")
	writeQuizFile(t, filepath.Join(dir, "security.md"), "# Security\n\nquiz me like a security reviewer\n")

	bySlug := map[string]Prompt{}
	for _, p := range LoadQuizPrompts(dir) {
		bySlug[p.Slug] = p
	}
	if got := bySlug["comprehension"].Body; got != "my replacement body" {
		t.Errorf("a user file must override the built-in of the same slug, got %q", got)
	}
	if _, ok := bySlug["security"]; !ok {
		t.Error("a new slug must be added to the library, not ignored")
	}
}

// The library must never be able to break a review, so junk is skipped silently.
func TestLoadQuizPrompts_ToleratesJunkInTheUserDir(t *testing.T) {
	dir := t.TempDir()
	writeQuizFile(t, filepath.Join(dir, "empty.md"), "# Title only\n")
	writeQuizFile(t, filepath.Join(dir, "notmarkdown.txt"), "ignored")
	if err := os.Mkdir(filepath.Join(dir, "subdir.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if len(LoadQuizPrompts(dir)) == 0 {
		t.Error("junk in the user dir must be skipped, leaving the built-ins intact")
	}
}

func writeQuizFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
