package review

// applyQuestions loads the agent's question sets, the reviewer's answers and
// the sets the agent marked done, and grounds each set's lines against the
// change index, as the quiz does.
func (c *PrereviewController) applyQuestions(state *PrereviewState) {
	state.QuestionsSeen = statusFingerprint(QuestionsPath(c.CSVPath))
	state.QuestionSets = LoadQuestionSets(c.CSVPath)
	c.applyQuestionAnswers(state)
	c.groundQuestions(state, c.lazyCitable(state))
}

// groundQuestions stamps AnchorStatus on every set: both ends of its line range
// must exist in its file. A file set makes no line claim ("absent").
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
		if s.Kind == QuestionKindFile {
			s.AnchorStatus = quizAnchorAbsent
			continue
		}
		s.AnchorStatus = groundRange(files.changes(s.File), AnchorKindLine, s.Side, s.FromLine, s.ToLine)
	}
}

// applyQuestionAnswers reloads what can change without the sets moving: the
// reviewer's answers and the agent's done marks.
func (c *PrereviewController) applyQuestionAnswers(state *PrereviewState) {
	state.QuestionAnswers = loadQuestionAnswers(QuestionAnswersPath(c.CSVPath))
	done := map[string]bool{}
	for id, n := range loadMarkCounts(c.processedPath()) {
		if n > 0 {
			done[id] = true
		}
	}
	state.QuestionsDone = done
}

// actionableQuestions is the snapshot's questions field: the sets in this
// review's scope the agent must act on (actionableQuestions).
func (s PrereviewState) actionableQuestions() []StreamQuestion {
	return actionableQuestions(s.scopedQuestionSets(), s.QuestionAnswers, s.QuestionsDone, s.Threads())
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
