package review

import (
	"fmt"
	"strconv"
	"time"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
)

// controller_quiz.go is the server half of the #191 comprehension quiz: it loads
// the agent-written quizzes, enforces the GROUNDING half of the question contract
// (the half the CLI verb cannot check, because only the server holds the diff),
// and toggles the quiz view.
//
// Grounding is the anti-slop lever. A question claims a line range; prereview
// owns the diff, so it can verify that range actually exists. A hallucinated
// question about code that isn't there is caught here and rendered with a
// warning, rather than being presented to the reviewer as fact.

// quizPath is the .prereview/quiz.jsonl path for this session's store.
func (c *PrereviewController) quizPath() string { return QuizPath(c.CSVPath) }

// quizAnswersPath is the .prereview/quiz-answers.jsonl path for this session's
// store — the server-owned half of the pair.
func (c *PrereviewController) quizAnswersPath() string { return QuizAnswersPath(c.CSVPath) }

// applyQuiz loads the agent's quizzes plus the reviewer's answers into state and
// grounds every question. QuizSeen is quiz.jsonl's fingerprint at this load, so
// the watcher reloads only when the file moved (grounding builds the change
// index, too costly for every tick).
func (c *PrereviewController) applyQuiz(state *PrereviewState) {
	state.QuizSeen = statusFingerprint(c.quizPath())
	state.Quizzes = loadQuizzes(c.quizPath())
	state.QuizAnswers = loadQuizAnswers(c.quizAnswersPath())
	c.groundQuizzes(state, c.lazyCitable(state))
}

// groundQuizzes stamps AnchorStatus on every question, against the change index
// the walkthrough grounds on: both ends of a cited range must exist in the
// question's file. Every file in the review, open or not, so the Quiz tab can
// flag a question about a file the reviewer hasn't opened. A question that makes
// no line claim (file/area/region) is "absent", which is deliberately not the
// same state as a bad anchor.
func (c *PrereviewController) groundQuizzes(state *PrereviewState, citableFiles func() *citable) {
	if len(state.Quizzes) == 0 {
		return
	}
	files := citableFiles()
	if files == nil {
		return // Mount loads quizzes before the file list, and grounds again after
	}
	for qi := range state.Quizzes {
		q := &state.Quizzes[qi]
		for i := range q.Questions {
			qu := &q.Questions[i]
			if qu.Anchorless() {
				qu.AnchorStatus = quizAnchorAbsent
				continue
			}
			qu.AnchorStatus = groundRange(files.changes(q.fileOf(*qu)), AnchorKindLine, qu.Side, qu.FromLine, qu.EndLine())
		}
	}
}

// findDiffLine returns the diff line numbered n on the given side. It matches the
// line NUMBERS the diff actually carries rather than indexing a slice by position,
// so it stays correct for a hunked diff whose numbers are sparse.
func findDiffLine(diff *gitdiff.FileDiff, side string, n int) (gitdiff.DiffLine, bool) {
	if diff == nil || n < 1 {
		return gitdiff.DiffLine{}, false
	}
	useOld := side == "old"
	for _, l := range diff.Lines {
		num := l.NewNum
		if useOld {
			num = l.OldNum
		}
		if num == n {
			return l, true
		}
	}
	return gitdiff.DiffLine{}, false
}

// RequestQuiz asks the agent for a quiz on the whole change (#115 A′5), by queuing
// a request comment on the open file whose body is the quiz prompt plus the
// change's scope, as RequestExplain does.
//
// It sends at once rather than pre-filling the composer the way PickPrompt does
// (#147): "Quiz me" should be one tap, and there is rarely anything to tweak. The
// request row is visible in the reviewer's queue, a record of what was asked
// with the agent's reply threaded under it.
func (c *PrereviewController) RequestQuiz(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if state.SelectedFile == "" {
		return state, fmt.Errorf("requestQuiz: no file selected")
	}
	body := promptBody(state.QuizPrompts, ctx.GetString("slug"))
	if body == "" {
		return state, nil // no prompts, or a stale slug from an old render
	}
	state, err := c.queueRequest(state, csv.RequestQuiz, body+c.changeScope(&state))
	if err != nil {
		return state, err
	}
	// Say so: on a phone the queue is behind a menu, and silence reads as "it
	// didn't work".
	state.Flash = "Quiz requested — the agent will answer shortly"
	return state, nil
}

// promptBody picks the requested prompt's body, falling back to the first
// when no slug was sent — which is the common case, since a single-prompt library
// renders a plain button with nothing to choose.
func promptBody(prompts []Prompt, slug string) string {
	if len(prompts) == 0 {
		return ""
	}
	if slug == "" {
		return prompts[0].Body
	}
	for _, p := range prompts {
		if p.Slug == slug {
			return p.Body
		}
	}
	return ""
}

// AnswerQuestion records the reviewer's choice for one question and reveals its
// explanation. Answers are the reviewer's, so they go in the server-owned file —
// never into the agent's append-only quiz.jsonl.
//
// An out-of-range choice is rejected rather than stored: the index is used to read
// Options in the template, so a bad one would render nothing (or panic a future
// caller) with no clue why.
func (c *PrereviewController) AnswerQuestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	quizID, questionID := ctx.GetString("quizId"), ctx.GetString("questionId")
	if quizID == "" || questionID == "" {
		return state, fmt.Errorf("answerQuestion: missing quizId/questionId")
	}
	choice, err := strconv.Atoi(ctx.GetString("choice"))
	if err != nil {
		return state, fmt.Errorf("answerQuestion: bad choice: %w", err)
	}
	q := findQuestion(state.Quizzes, quizID, questionID)
	if q == nil {
		return state, fmt.Errorf("answerQuestion: unknown question %s/%s", quizID, questionID)
	}
	if choice < 0 || choice >= len(q.Options) {
		return state, fmt.Errorf("answerQuestion: choice %d out of range for %s", choice, questionID)
	}
	if state.QuizAnswers == nil {
		state.QuizAnswers = map[string]QuizAnswer{}
	}
	state.QuizAnswers[answerKey(quizID, questionID)] = QuizAnswer{
		QuizID: quizID, QuestionID: questionID, Choice: choice, At: time.Now().UnixNano(),
	}
	if err := saveQuizAnswers(c.quizAnswersPath(), state.QuizAnswers); err != nil {
		return state, fmt.Errorf("persist quiz answer: %w", err)
	}
	// Keep this card open with its explanation for the rest of the visit (#212).
	if state.QuizJustAnswered == nil {
		state.QuizJustAnswered = map[string]bool{}
	}
	state.QuizJustAnswered[answerKey(quizID, questionID)] = true
	c.scheduleEmit() // the agent sees the answer in its next snapshot
	return state, nil
}

// RetakeQuiz clears the answers to every question the review shows, so the
// reviewer can run the quiz again — retrieval practice works by repetition, and a
// quiz you can only take once is a test, not a study tool.
func (c *PrereviewController) RetakeQuiz(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	items := state.allQuizItems()
	if len(items) == 0 {
		return state, nil
	}
	for _, it := range items {
		delete(state.QuizAnswers, answerKey(it.QuizID, it.ID))
		delete(state.QuizJustAnswered, answerKey(it.QuizID, it.ID))
	}
	if err := saveQuizAnswers(c.quizAnswersPath(), state.QuizAnswers); err != nil {
		return state, fmt.Errorf("persist quiz retake: %w", err)
	}
	c.scheduleEmit()
	return state, nil
}

// JumpToQuizLine leaves the quiz and scrolls to the lines a question cites, so
// "go look at the code" is one tap. It deliberately closes the quiz view (like
// every other jump handler) rather than trying to show both: the reviewer asked
// to see the code, and toggling back is one tap the other way.
func (c *PrereviewController) JumpToQuizLine(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	raw := ctx.GetString("line")
	line, err := strconv.Atoi(raw)
	if err != nil || line < 1 {
		return state, fmt.Errorf("jumpToQuizLine: bad line %q", raw)
	}
	file := ctx.GetString("file")
	if file == "" {
		return state, fmt.Errorf("jumpToQuizLine: missing file")
	}
	return c.jumpToLine(state, file, ctx.GetString("side"), line)
}

// findQuestion locates a question by (quiz id, question id). Question ids are
// only unique within their quiz, so both are required.
func findQuestion(quizzes []Quiz, quizID, questionID string) *Question {
	for i := range quizzes {
		if quizzes[i].ID != quizID {
			continue
		}
		for j := range quizzes[i].Questions {
			if quizzes[i].Questions[j].ID == questionID {
				return &quizzes[i].Questions[j]
			}
		}
	}
	return nil
}
