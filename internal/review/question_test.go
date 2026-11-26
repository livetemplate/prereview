package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func intp(i int) *int { return &i }

// validSet is a one-question line set that passes validation.
func validSet() QuestionSet {
	return NormalizeQuestionSet(QuestionSet{ID: "q-refresh", File: "auth/session.go", FromLine: 41, ToLine: 42,
		Steps: []QuestionStep{{ID: "on-fail", Prompt: "When a token refresh fails, what should happen?",
			Options:     []QuestionOption{{Label: "Retry twice, then sign out"}, {Label: "Sign out at once"}},
			Recommended: intp(0)}}})
}

func TestValidateQuestions(t *testing.T) {
	if err := ValidateQuestionSet(validSet()); err != nil {
		t.Fatalf("a valid set is refused: %v", err)
	}
	opts := func(n int) []QuestionOption {
		var o []QuestionOption
		for i := range n {
			o = append(o, QuestionOption{Label: strings.Repeat("x", i+1)})
		}
		return o
	}
	for _, tc := range []struct {
		name, want string
		edit       func(*QuestionSet)
	}{
		{"no steps", "1 to 4 questions", func(s *QuestionSet) { s.Steps = nil }},
		{"five steps", "1 to 4 questions", func(s *QuestionSet) {
			for i := range 4 {
				s.Steps = append(s.Steps, QuestionStep{ID: strings.Repeat("s", i+1), Prompt: "p", Options: opts(2)})
			}
		}},
		{"one option", "2 to 4 options", func(s *QuestionSet) { s.Steps[0].Options = opts(1) }},
		{"five options", "2 to 4 options", func(s *QuestionSet) { s.Steps[0].Options = opts(5) }},
		{"empty label", "no \"label\"", func(s *QuestionSet) { s.Steps[0].Options[1].Label = "" }},
		{"recommended out of range", "out of range", func(s *QuestionSet) { s.Steps[0].Recommended = intp(2) }},
		{"no prompt", "missing \"prompt\"", func(s *QuestionSet) { s.Steps[0].Prompt = "" }},
		{"duplicate step id", "duplicate id", func(s *QuestionSet) { s.Steps = append(s.Steps, s.Steps[0]) }},
		{"line without from_line", "from_line", func(s *QuestionSet) { s.FromLine = 0 }},
		{"region kind", "unknown kind", func(s *QuestionSet) { s.Kind = QuestionKindRegion }},
		{"path outside the repo", "inside it", func(s *QuestionSet) { s.File = "../etc/passwd" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validSet()
			tc.edit(&s)
			err := ValidateQuestionSet(s)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
	file := validSet()
	file.Kind, file.FromLine, file.ToLine = QuestionKindFile, 0, 0
	if err := ValidateQuestionSet(file); err != nil {
		t.Errorf("a file set needs no lines: %v", err)
	}
}

// TestLoadQuestionSets_LastWriteWins: re-asking a set id revises it.
func TestLoadQuestionSets_LastWriteWins(t *testing.T) {
	csvPath := filepath.Join(t.TempDir(), "comments.csv")
	first, second := validSet(), validSet()
	second.Steps[0].Prompt = "Revised"
	var b strings.Builder
	for _, s := range []QuestionSet{first, second} {
		line, _ := json.Marshal(s)
		b.Write(line)
		b.WriteString("\n")
	}
	b.WriteString("{torn\n")
	if err := os.WriteFile(QuestionsPath(csvPath), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadQuestionSets(csvPath)
	if len(got) != 1 || got[0].Steps[0].Prompt != "Revised" {
		t.Fatalf("got %+v, want the one revised set", got)
	}
}

func TestQuestionAnswers_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), QuestionAnswersFileName)
	want := map[string]QuestionAnswer{
		questionAnswerKey("q1", "a"): {SetID: "q1", StepID: "a", Picked: []int{0, 2}, Sent: true, At: 1},
		questionAnswerKey("q1", "b"): {SetID: "q1", StepID: "b", Other: "typed", At: 2},
	}
	if err := saveQuestionAnswers(path, want); err != nil {
		t.Fatal(err)
	}
	got := loadQuestionAnswers(path)
	if len(got) != 2 || !slices.Equal(got[questionAnswerKey("q1", "a")].Picked, []int{0, 2}) || got[questionAnswerKey("q1", "b")].Other != "typed" {
		t.Fatalf("round trip: got %+v", got)
	}
}

// TestActionableQuestions: a set ships once every step is sent, with labels,
// until the agent marks it done; a reviewer reply brings it back either way.
func TestActionableQuestions(t *testing.T) {
	s := validSet()
	two := validSet()
	two.ID = "q-two"
	two.Steps = append(two.Steps, QuestionStep{ID: "second", Prompt: "And?", Options: []QuestionOption{{Label: "A"}, {Label: "B"}}})
	sets := []QuestionSet{s, two}
	answers := map[string]QuestionAnswer{
		questionAnswerKey("q-refresh", "on-fail"): {SetID: "q-refresh", StepID: "on-fail", Picked: []int{0}, Sent: true},
		// q-two: one of two steps answered, not sent
		questionAnswerKey("q-two", "on-fail"): {SetID: "q-two", StepID: "on-fail", Picked: []int{1}},
	}
	got := actionableQuestions(sets, answers, nil, nil, nil)
	if len(got) != 1 || got[0].ID != "q-refresh" {
		t.Fatalf("got %+v, want only the sent set", got)
	}
	if a := got[0].Answers; len(a) != 1 || !slices.Equal(a[0].Picked, []string{"Retry twice, then sign out"}) {
		t.Fatalf("answers = %+v, want the picked label", a)
	}
	if got := actionableQuestions(sets, answers, nil, map[string]bool{"q-refresh": true}, nil); len(got) != 0 {
		t.Fatalf("a set marked done still ships: %+v", got)
	}
	reply := map[string][]ThreadEntry{"q-two": {{TargetID: "q-two", Author: AuthorReviewer, Body: "None of these fit", At: 1}}}
	got = actionableQuestions(sets, answers, nil, map[string]bool{"q-refresh": true}, reply)
	if len(got) != 1 || got[0].ID != "q-two" || len(got[0].Answers) != 0 || len(got[0].Thread) != 1 {
		t.Fatalf("a reviewer reply on an unsent set: got %+v, want it with its thread and no answers", got)
	}
	// Resolving an unanswered set tells the agent, until it marks the set done.
	resolved := map[string]bool{"q-two": true}
	got = actionableQuestions(sets, answers, resolved, map[string]bool{"q-refresh": true}, nil)
	if len(got) != 1 || got[0].ID != "q-two" || !got[0].Resolved || len(got[0].Answers) != 0 {
		t.Fatalf("a resolved unanswered set: got %+v, want it once, resolved, with no answers", got)
	}
	if got := actionableQuestions(sets, answers, resolved, map[string]bool{"q-refresh": true, "q-two": true}, nil); len(got) != 0 {
		t.Fatalf("a resolved set the agent marked done still ships: %+v", got)
	}
}

// TestStream_QuestionLifecycle runs a set through the real emit path: absent
// until sent, in the snapshot once sent, gone after `prereview done`.
func TestStream_QuestionLifecycle(t *testing.T) {
	old := emitDebounce
	emitDebounce = time.Hour
	t.Cleanup(func() { emitDebounce = old })
	c, buf, _ := newStreamController(t)
	t.Cleanup(c.stopPendingEmit)
	questions := func() []StreamQuestion {
		t.Helper()
		buf.Reset()
		c.emitSnapshot()
		evs := decodeEvents(t, buf.Bytes())
		if len(evs) != 1 {
			t.Fatalf("want 1 snapshot, got %d", len(evs))
		}
		return evs[0].Questions
	}
	line, _ := json.Marshal(validSet())
	if err := os.WriteFile(QuestionsPath(c.CSVPath), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := questions(); len(got) != 0 {
		t.Fatalf("an unanswered set reached the agent: %+v", got)
	}
	answers := map[string]QuestionAnswer{questionAnswerKey("q-refresh", "on-fail"): {SetID: "q-refresh", StepID: "on-fail", Picked: []int{1}, Sent: true, At: 1}}
	if err := saveQuestionAnswers(QuestionAnswersPath(c.CSVPath), answers); err != nil {
		t.Fatal(err)
	}
	if got := questions(); len(got) != 1 || got[0].Answers[0].Picked[0] != "Sign out at once" {
		t.Fatalf("after Send: got %+v, want the set with its pick", got)
	}
	if err := os.WriteFile(c.processedPath(), []byte(`{"id":"q-refresh"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := questions(); len(got) != 0 {
		t.Fatalf("after done: got %+v, want none", got)
	}
}

// TestThreadViews_CardInTimeOrder: a set asked in a thread sits among the
// thread's replies by when it was asked; one with no time goes last.
func TestThreadViews_CardInTimeOrder(t *testing.T) {
	at := func(s string) int64 { tm, _ := time.Parse(time.RFC3339Nano, s); return tm.UnixNano() }
	s := PrereviewState{
		Comments: []Comment{{ID: "c1", File: "a.go", FromLine: 1, ToLine: 1}},
		ThreadEntries: []ThreadEntry{
			{TargetID: "c1", Author: AuthorReviewer, Body: "before", At: at("2026-11-26T10:00:00.2Z")},
			{TargetID: "c1", Author: AuthorAgent, Body: "after", At: at("2026-11-26T10:00:00.6Z")},
		},
		QuestionSets: []QuestionSet{
			{ID: "q1", For: "c1", File: "a.go", CreatedAt: "2026-11-26T10:00:00.4Z", Steps: []QuestionStep{{ID: "s", Prompt: "p", Options: []QuestionOption{{Label: "a"}}}}},
			{ID: "q2", For: "c1", File: "a.go", Steps: []QuestionStep{{ID: "s", Prompt: "p", Options: []QuestionOption{{Label: "a"}}}}},
		},
	}
	var got []string
	for _, it := range s.ThreadViews()["c1"] {
		if it.Question != nil {
			got = append(got, it.Question.ID)
		} else {
			got = append(got, it.Body)
		}
	}
	if want := []string{"before", "q1", "after", "q2"}; !slices.Equal(got, want) {
		t.Errorf("thread order = %v, want %v", got, want)
	}
}
