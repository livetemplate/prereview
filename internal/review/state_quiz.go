package review

import "fmt"

// state_quiz.go holds the quiz view's read helpers.
//
// Every one is ZERO-ARG on purpose: livetemplate pre-computes zero-arg state
// methods, and a method that takes an argument silently breaks rendering rather
// than failing loudly. So instead of `{{$.QuizItem .ID}}` the template ranges
// over a prepared slice — QuizItems() carries everything a question row needs.

// QuizItem is one question plus the reviewer's progress on it: a flattened view
// row, so the template needs no lookups and no per-row logic.
type QuizItem struct {
	Question
	Answered bool
	Choice   int // the option the reviewer picked; meaningless unless Answered
	Correct  bool
}

// CurrentQuiz returns the quiz for the selected file, or nil. The agent can
// revise a quiz by re-appending its id (the loader keeps the last write), and can
// also submit a genuinely new one for the same file — in which case the newest
// wins, since that is the one the reviewer just asked for.
func (s PrereviewState) CurrentQuiz() *Quiz {
	if s.SelectedFile == "" {
		return nil
	}
	for i := len(s.Quizzes) - 1; i >= 0; i-- {
		if s.Quizzes[i].File == s.SelectedFile {
			return &s.Quizzes[i]
		}
	}
	return nil
}

// HasQuiz reports whether the selected file has a quiz to show — the gate for
// offering the quiz entry in the menus at all.
func (s PrereviewState) HasQuiz() bool { return s.CurrentQuiz() != nil }

// QuizItems is the current quiz's questions with the reviewer's progress folded
// in. Empty when there is no quiz, so the template can range over it directly.
func (s PrereviewState) QuizItems() []QuizItem {
	q := s.CurrentQuiz()
	if q == nil {
		return nil
	}
	out := make([]QuizItem, 0, len(q.Questions))
	for _, qu := range q.Questions {
		item := QuizItem{Question: qu}
		if a, ok := s.QuizAnswers[answerKey(q.ID, qu.ID)]; ok {
			item.Answered = true
			item.Choice = a.Choice
			item.Correct = a.Choice == qu.Answer
		}
		out = append(out, item)
	}
	return out
}

// QuizQuestionCount is the number of questions in the current quiz.
func (s PrereviewState) QuizQuestionCount() int {
	if q := s.CurrentQuiz(); q != nil {
		return len(q.Questions)
	}
	return 0
}

// QuizAnsweredCount is how many of them have been answered.
func (s PrereviewState) QuizAnsweredCount() int {
	n := 0
	for _, it := range s.QuizItems() {
		if it.Answered {
			n++
		}
	}
	return n
}

// QuizCorrectCount is how many were answered correctly.
func (s PrereviewState) QuizCorrectCount() int {
	n := 0
	for _, it := range s.QuizItems() {
		if it.Correct {
			n++
		}
	}
	return n
}

// QuizComplete reports that every question has been answered.
func (s PrereviewState) QuizComplete() bool {
	n := s.QuizQuestionCount()
	return n > 0 && s.QuizAnsweredCount() == n
}

// QuizScoreLabel renders the running score for the quiz header, e.g. "2/5".
func (s PrereviewState) QuizScoreLabel() string {
	if s.QuizQuestionCount() == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", s.QuizCorrectCount(), s.QuizQuestionCount())
}

// AnchorLabel is the badge for a question's line reference: "L42", "L42-L48", or
// empty when the question is deliberately about something absent.
func (q Question) AnchorLabel() string {
	if q.Anchorless() {
		return ""
	}
	if q.ToLine > q.FromLine {
		return fmt.Sprintf("L%d-L%d", q.FromLine, q.ToLine)
	}
	return fmt.Sprintf("L%d", q.FromLine)
}

// Jumpable reports whether this question offers a "jump to the code" link — only
// when it both claims an anchor AND that anchor resolved in the current diff.
// An ungrounded question must never offer a jump: the link would land on code
// that has nothing to do with the question.
func (q Question) Jumpable() bool {
	return !q.Anchorless() && q.AnchorStatus == quizAnchorOK
}
