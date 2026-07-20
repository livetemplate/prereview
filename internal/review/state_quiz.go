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
	// QuizID is carried on the row so the card partial is SELF-CONTAINED: it can
	// build its answer form without reaching for root state, which keeps it
	// renderable from the diff view, the file head and the overview alike.
	QuizID string
	// ThreadID is this question's global identity for the #149 conversation
	// machinery, and Thread the conversation so far. Carried on the row so the card
	// partial stays self-contained.
	ThreadID   string
	Thread     []ThreadEntry
	Replying   bool
	ReplyDraft string
	Answered   bool
	Choice     int // the option the reviewer picked; meaningless unless Answered
	Correct    bool
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
	threads := s.Threads()
	out := make([]QuizItem, 0, len(q.Questions))
	for _, qu := range q.Questions {
		tid := QuizThreadID(q.ID, qu.ID)
		item := QuizItem{Question: qu, QuizID: q.ID, ThreadID: tid, Thread: threads[tid]}
		if s.ReplyingID == tid {
			item.Replying = true
			item.ReplyDraft = s.ReplyDraft
		}
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

// QuizByEndLine groups the current quiz's LINE-anchored questions by the line
// they end on, so the diff view can render each one inline right under the code
// it asks about — the same shape CommentsByEndLine and SuggestionsByEndLine use.
//
// This is what makes a quiz question just another annotation. The first version
// put the quiz on its own screen and then had to reproduce the cited code inside
// it, which was a worse re-implementation of the diff view; anchoring the
// question to the line makes the code its own context.
func (s PrereviewState) QuizByEndLine() map[int][]QuizItem {
	if s.SelectedFile == "" {
		return nil
	}
	out := map[int][]QuizItem{}
	for _, it := range s.QuizItems() {
		// Ungrounded questions have no line that exists, so they cannot render
		// here; FileQuizItems picks them up at the file head instead.
		if !it.LineAnchored() || it.Ungrounded() {
			continue
		}
		end := it.ToLine
		if end < it.FromLine {
			end = it.FromLine
		}
		out[end] = append(out[end], it)
	}
	return out
}

// FileQuizItems are the questions that have no valid position inside the diff, so
// they render at the file head — exactly where a kind=file COMMENT renders.
//
// Two groups land here, for different reasons:
//
//   - kind=file: about the change as a whole, or about something absent from it.
//     It never had a line, by design.
//   - UNGROUNDED: it claims a line the diff does not contain, so there is nowhere
//     to anchor it. These MUST still render. Anchoring questions inline means an
//     unresolvable anchor has no home, and quietly dropping it would HIDE a
//     hallucinated question instead of surfacing it — undoing the entire point of
//     the grounding check. The reviewer needs to see that the agent asked about
//     code that isn't there.
func (s PrereviewState) FileQuizItems() []QuizItem {
	var out []QuizItem
	for _, it := range s.QuizItems() {
		if it.Kind == QuestionKindFile || it.Ungrounded() {
			out = append(out, it)
		}
	}
	return out
}
