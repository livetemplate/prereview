package review

import "time"

// loadQuestions loads the agent's question sets, the reviewer's answers and
// the sets the agent marked done. Grounding is separate (groundQuestions):
// Mount grounds once with the quizzes, after the file list is in.
func (c *PrereviewController) loadQuestions(state *PrereviewState) {
	state.QuestionsSeen = statusFingerprint(QuestionsPath(c.CSVPath))
	state.QuestionSets = LoadQuestionSets(c.CSVPath)
	c.applyQuestionAnswers(state)
}

// applyQuestions loads and grounds the question sets: the watcher's and the
// emit's path, where no later grounding pass follows.
func (c *PrereviewController) applyQuestions(state *PrereviewState) {
	c.loadQuestions(state)
	c.groundQuestions(state, c.lazyCitable(state))
}

// groundQuestions stamps AnchorStatus on every set against the change index:
// a file set's file must be in the review; a line or text set's range must
// exist in its file.
func (c *PrereviewController) groundQuestions(state *PrereviewState, citableFiles func() *citable) {
	if len(state.QuestionSets) == 0 {
		return
	}
	files := citableFiles()
	if files == nil {
		return // Mount loads before the file list, and grounds again after
	}
	for i := range state.QuestionSets {
		s := &state.QuestionSets[i]
		s.AnchorStatus = groundRange(files.changes(s.File), s.Kind, s.Side, s.FromLine, s.ToLine)
	}
}

// applyQuestionAnswers reloads what can change without the sets moving: the
// reviewer's answers and the agent's done marks. A review with no sets reads
// neither file.
func (c *PrereviewController) applyQuestionAnswers(state *PrereviewState) {
	if len(state.QuestionSets) == 0 {
		state.QuestionAnswers, state.QuestionsDone = nil, nil
		return
	}
	state.QuestionAnswers = loadQuestionAnswers(QuestionAnswersPath(c.CSVPath))
	state.QuestionsDone = doneMarks(c.CSVPath)
}

// emitSnapshotOf emits state's snapshot: the one argument list both the
// debounced emit and the final flush send.
func (c *PrereviewController) emitSnapshotOf(st *PrereviewState) error {
	threads := st.Threads()
	questions := actionableQuestions(st.scopedQuestionSets(), st.QuestionAnswers, st.QuestionsDone, threads)
	return c.Emitter.EmitSnapshot(st.scopedComments(), st.scopedSuggestions(), st.DecisionsBySuggestion(), threads, st.Applied, st.QuizResults(), questions, c.WriteWith(), c.isPaused(), time.Now())
}

// scopedQuestionSets is QuestionSets limited to the review's scope, like
// scopedComments: a single-file review hands the agent that file's sets only.
func (s PrereviewState) scopedQuestionSets() []QuestionSet {
	if s.SingleFile == "" {
		return s.QuestionSets
	}
	var out []QuestionSet
	for _, q := range s.QuestionSets {
		if s.inScope(q.File) {
			out = append(out, q)
		}
	}
	return out
}
