package review

import (
	"fmt"

	"github.com/livetemplate/prereview/gitdiff"
)

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
	// Excerpt is the cited code, shown inline under the question.
	//
	// The quiz replaces the diff, so without this a reviewer had to leave the
	// quiz, find the lines, read them, and come back — by which point the
	// question needed re-reading too. That is a memory test, not a comprehension
	// test. Reported from real use; the fix is to bring the code to the question
	// rather than sending the reader to the code.
	//
	// Empty for an anchorless `decision` question, which is about something that
	// is not in the diff and therefore has nothing to show.
	Excerpt []QuizExcerptLine
}

// QuizExcerptLine is one line of the inline excerpt. Cited marks the lines the
// question actually points at, so surrounding context can be dimmed — the reader
// needs a little context to orient, but must still see what is being asked about.
type QuizExcerptLine struct {
	gitdiff.DiffLine
	Cited bool
}

const (
	// quizExcerptContext is how many lines of orientation to show either side of
	// the cited range. Two is enough to place the code without burying the question.
	quizExcerptContext = 2
	// quizExcerptMax caps the excerpt so a question citing a huge range cannot push
	// the options off the screen — the jump link is still there for full context.
	quizExcerptMax = 14
)

// excerptFor returns the cited lines (plus a little context) from the current
// diff. Nil when the question is anchorless or the diff is not the one the
// question belongs to.
func (s PrereviewState) excerptFor(q Question) []QuizExcerptLine {
	if q.Anchorless() || s.CurrentDiff == nil {
		return nil
	}
	lines := s.CurrentDiff.Lines
	useOld := q.Side == "old"
	first, last := -1, -1
	for i, l := range lines {
		n := l.NewNum
		if useOld {
			n = l.OldNum
		}
		if n >= q.FromLine && n <= max(q.ToLine, q.FromLine) && n > 0 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil // the cited range is not in this diff — the ungrounded case
	}
	lo := max(0, first-quizExcerptContext)
	hi := min(len(lines)-1, last+quizExcerptContext)
	if hi-lo+1 > quizExcerptMax {
		hi = lo + quizExcerptMax - 1
	}
	out := make([]QuizExcerptLine, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, QuizExcerptLine{DiffLine: lines[i], Cited: i >= first && i <= last})
	}
	return out
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
		item := QuizItem{Question: qu, Excerpt: s.excerptFor(qu)}
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

// QuizResults reports every quiz's outcome for the agent snapshot (#191).
//
// It is ADVISORY: it never gates a verdict, it just lets the agent tell "accepted
// after a comprehension check" from "accepted without one". That distinction is
// the whole point — an accept records that the reviewer clicked, and this is the
// only signal that says whether they also understood.
//
// Unlike the view helpers it walks EVERY quiz, not just the selected file's, since
// the snapshot is not scoped to whatever file happens to be open.
func (s PrereviewState) QuizResults() []StreamQuiz {
	if len(s.Quizzes) == 0 {
		return nil
	}
	out := make([]StreamQuiz, 0, len(s.Quizzes))
	for _, q := range s.Quizzes {
		r := StreamQuiz{File: q.File, QuizID: q.ID, Total: len(q.Questions)}
		for _, qu := range q.Questions {
			if a, ok := s.QuizAnswers[answerKey(q.ID, qu.ID)]; ok {
				r.Taken = true
				if a.Choice == qu.Answer {
					r.Score++
				}
			}
		}
		out = append(out, r)
	}
	return out
}

// QuizPending reports that a "Quiz me" request for the selected file is awaiting
// an answer, so the control shows a disabled "Quiz requested…" rather than
// inviting a second tap that would queue a duplicate.
func (s PrereviewState) QuizPending() bool {
	return s.QuizRequestedFile != "" && s.QuizRequestedFile == s.SelectedFile && !s.HasQuiz()
}
