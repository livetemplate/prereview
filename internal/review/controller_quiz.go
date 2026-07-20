package review

import (
	"fmt"
	"strconv"
	"time"

	"github.com/livetemplate/livetemplate"
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
// derives each question's AnchorStatus against the live diff. Cheap and safe to
// call from both Mount and the watcher fan-out (LLMStatusChanged), mirroring
// applySuggestions.
func (c *PrereviewController) applyQuiz(state *PrereviewState) {
	state.Quizzes = loadQuizzes(c.quizPath())
	state.QuizAnswers = loadQuizAnswers(c.quizAnswersPath())
	// The request has been answered — stop showing "Quiz requested…" and let the
	// control invite a fresh one again.
	if state.QuizRequestedFile != "" && quizForFile(state.Quizzes, state.QuizRequestedFile) {
		state.QuizRequestedFile = ""
	}
	// Grounding is NOT done here. Mount calls applyQuiz long before it loads
	// CurrentDiff, so grounding at load time would judge every question against a
	// nil diff. Callers ground once they actually hold the diff.
	c.groundQuizzes(state)
}

// groundQuizzes stamps AnchorStatus on every question of the SELECTED file's
// quizzes — the only ones that render — by checking the claimed line against the
// current diff.
//
// Only the selected file is grounded because CurrentDiff is the one diff in hand;
// questions for other files keep an empty status until their file is opened. That
// mirrors relocateSuggestionsSelected, and it is why a quiz for an unopened file
// never shows a spurious "ungrounded" badge derived from the wrong diff.
func (c *PrereviewController) groundQuizzes(state *PrereviewState) {
	if state.SelectedFile == "" {
		return
	}
	for qi := range state.Quizzes {
		if state.Quizzes[qi].File != state.SelectedFile {
			continue
		}
		for i := range state.Quizzes[qi].Questions {
			q := &state.Quizzes[qi].Questions[i]
			switch {
			case q.Anchorless():
				// file/area/region questions make no line claim, so there is nothing
				// to falsify. Deliberate, and deliberately NOT the same state as a
				// bad anchor.
				q.AnchorStatus = quizAnchorAbsent
			case diffHasLine(state.CurrentDiff, q.Side, q.FromLine):
				q.AnchorStatus = quizAnchorOK
			default:
				q.AnchorStatus = quizAnchorUngrounded
			}
		}
	}
}

// findDiffLine returns the diff line numbered n on the given side. It matches the
// line NUMBERS the diff actually carries rather than indexing a slice by position,
// so it stays correct for a hunked diff whose numbers are sparse — and a number
// that isn't there is exactly the hallucinated anchor grounding exists to catch.
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

// diffHasLine reports whether the cited line exists — the grounding predicate.
func diffHasLine(diff *gitdiff.FileDiff, side string, n int) bool {
	_, ok := findDiffLine(diff, side, n)
	return ok
}

// RequestQuiz asks the agent for a quiz about the selected file, by SAVING a
// file-level comment whose body is the quiz prompt.
//
// It deliberately sends immediately rather than pre-filling the composer the way
// PickPrompt does (#147). "Quiz me" should be one tap — this tool is mostly used
// from a phone — and unlike a suggestions prompt there is rarely anything to
// tweak. The cost is that the request is VISIBLE in the reviewer's own queue: an
// earlier design hoped to hide it with Comment.Hidden, but that flag only applies
// to RESOLVED comments ("Hidden is meaningless on them", state.go), so there is
// no way to hide it and no reason to invent one. A saved Prompt comment already
// behaves exactly this way, and the visible row doubles as a record of what was
// asked, with the agent's reply threaded under it.
func (c *PrereviewController) RequestQuiz(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if state.SelectedFile == "" {
		return state, fmt.Errorf("requestQuiz: no file selected")
	}
	body := quizPromptBody(state.QuizPrompts, ctx.GetString("slug"))
	if body == "" {
		return state, nil // no prompts, or a stale slug from an old render
	}
	resetToFileComment(&state)
	state.DraftBody = ""
	state, err := c.addFileLevelComment(state, body)
	if err != nil {
		return state, err
	}
	// Say so. Saving the comment changes nothing the reviewer can see — especially
	// on a phone, where the queue is behind a menu — so without this the only
	// feedback is silence, and silence reads as "it didn't work".
	state.QuizRequestedFile = state.SelectedFile
	state.Flash = "Quiz requested — the agent will answer shortly"
	return state, nil
}

// quizForFile reports whether any quiz targets file.
func quizForFile(quizzes []Quiz, file string) bool {
	for _, q := range quizzes {
		if q.File == file {
			return true
		}
	}
	return false
}

// quizPromptBody picks the requested prompt's body, falling back to the first
// when no slug was sent — which is the common case, since a single-prompt library
// renders a plain button with nothing to choose.
func quizPromptBody(prompts []Prompt, slug string) string {
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
	// Answering moves "you are here" too, so the highlight follows the question
	// being worked on rather than only the last one reached via the strip. The
	// scroll nudge is cleared: the reviewer is already looking at this card, and
	// re-firing it would yank the page.
	state.SelectedQuizID = questionID
	state.ScrollToQuizID = ""
	if err := saveQuizAnswers(c.quizAnswersPath(), state.QuizAnswers); err != nil {
		return state, fmt.Errorf("persist quiz answer: %w", err)
	}
	return state, nil
}

// RetakeQuiz clears every answer for the current quiz so the reviewer can run it
// again — retrieval practice works by repetition, and a quiz you can only take
// once is a test, not a study tool.
func (c *PrereviewController) RetakeQuiz(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	q := state.CurrentQuiz()
	if q == nil {
		return state, nil
	}
	for _, qu := range q.Questions {
		delete(state.QuizAnswers, answerKey(q.ID, qu.ID))
	}
	if err := saveQuizAnswers(c.quizAnswersPath(), state.QuizAnswers); err != nil {
		return state, fmt.Errorf("persist quiz retake: %w", err)
	}
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
	state.ShowQuiz = false
	// Park the line cursor on the cited line; the template stamps `is-cursor` and
	// scrolls it into view. Same mechanism the search palette uses to land on a
	// hit (controller_search.go), so quiz jumps and search jumps behave alike.
	// The key needs BOTH the old and new numbers, so it is derived from the diff
	// line itself rather than assembled from the question's single number.
	if l, ok := findDiffLine(state.CurrentDiff, ctx.GetString("side"), line); ok {
		state.CursorKey = lineCursorKey(l)
	}
	return state, nil
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

// JumpToQuestion scrolls a question's card into view from the navigator. The
// questions live inline in the diff now, so "go to question 4" is a scroll, not a
// screen change — the reviewer keeps their place in the code.
func (c *PrereviewController) JumpToQuestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("questionId")
	if id == "" {
		return state, fmt.Errorf("jumpToQuestion: missing questionId")
	}
	// Leaving the overview is part of the jump: the card being scrolled to is in
	// the diff, so staying on the overview screen would scroll nothing.
	state.ShowQuiz = false
	state.ScrollToQuizID = id
	state.SelectedQuizID = id
	return state, nil
}

// DismissQuizNav puts the navigator away for this session.
func (c *PrereviewController) DismissQuizNav(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.QuizNavDismissed = true
	return state, nil
}

// ToggleQuiz flips between the diff viewer and the comprehension-quiz view for
// the selected file. Mirrors ToggleCommentList: closes the overflow menu so the
// result is visible immediately, and is not persisted — reopening the browser
// starts back in the diff.
func (c *PrereviewController) ToggleQuiz(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ShowQuiz = !state.ShowQuiz
	state.MoreMenuOpen = false
	if state.ShowQuiz {
		// Re-ground on open: the agent may have edited the file since the quiz was
		// generated, which can turn a valid anchor stale.
		c.groundQuizzes(&state)
	}
	return state, nil
}
