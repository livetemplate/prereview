package review

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/livetemplate/livetemplate"
)

// loadQuestions loads the agent's question sets, the reviewer's answers and
// the sets the agent marked done. Grounding is separate (groundQuestions):
// Mount grounds once with the quizzes, after the file list is in.
func (c *PrereviewController) loadQuestions(state *PrereviewState) {
	state.QuestionsSeen = statusFingerprint(QuestionsPath(c.CSVPath))
	loadQuestionState(c.CSVPath, state)
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
	loadQuestionAnswerState(c.CSVPath, state)
}

// loadQuestionState loads the store's question sets and everything said about
// them: the one loader the live review and `prereview questions` share.
func loadQuestionState(csvPath string, state *PrereviewState) {
	state.QuestionSets = LoadQuestionSets(csvPath)
	loadQuestionAnswerState(csvPath, state)
}

// loadQuestionAnswerState loads what changes while the sets stay put: the
// reviewer's answers and status marks, and the agent's done marks. A review with
// no sets reads none of them.
func loadQuestionAnswerState(csvPath string, state *PrereviewState) {
	if len(state.QuestionSets) == 0 {
		state.QuestionAnswers, state.QuestionsDone, state.QuestionStatus = nil, nil, nil
		return
	}
	state.QuestionAnswers = loadQuestionAnswers(QuestionAnswersPath(csvPath))
	state.QuestionsDone = doneMarks(csvPath)
	state.QuestionStatus = loadQuestionStatus(QuestionStatusPath(csvPath))
}

// streamQuestions is the sets the agent must act on, as it reads them: the
// snapshot's questions field and `prereview questions`.
func (s PrereviewState) streamQuestions(threads map[string][]ThreadEntry) []StreamQuestion {
	return actionableQuestions(s.scopedQuestionSets(), s.QuestionAnswers, s.questionsResolved(), s.QuestionsDone, threads)
}

// emitSnapshotOf emits state's snapshot: the one argument list both the
// debounced emit and the final flush send.
func (c *PrereviewController) emitSnapshotOf(st *PrereviewState) error {
	threads := st.Threads()
	questions := st.streamQuestions(threads)
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

// findQuestionSet returns the set with id.
func findQuestionSet(state *PrereviewState, id string) (*QuestionSet, error) {
	i := slices.IndexFunc(state.QuestionSets, func(s QuestionSet) bool { return s.ID == id })
	if i < 0 {
		return nil, fmt.Errorf("no question set %q", id)
	}
	return &state.QuestionSets[i], nil
}

// questionTarget returns the set and the index of the step a card's form names.
func questionTarget(state *PrereviewState, setID, stepID string) (*QuestionSet, int, error) {
	set, err := findQuestionSet(state, setID)
	if err != nil {
		return nil, 0, err
	}
	j := slices.IndexFunc(set.Steps, func(st QuestionStep) bool { return st.ID == stepID })
	if j < 0 {
		return nil, 0, fmt.Errorf("question set %s has no step %q", setID, stepID)
	}
	return set, j, nil
}

// withEntry and withoutEntry return a copy of m with k set or removed: a map
// the page rendered from is never changed in place, or livetemplate can't see
// the change.
func withEntry[V any](m map[string]V, k string, v V) map[string]V {
	out := maps.Clone(m)
	if out == nil {
		out = map[string]V{}
	}
	out[k] = v
	return out
}

func withoutEntry[V any](m map[string]V, k string) map[string]V {
	out := maps.Clone(m)
	delete(out, k)
	return out
}

// saveAnswer records a for a set's question and saves the answers file. A
// one-tap set is sent at once. Otherwise a pick-one answer or a typed one moves
// the card to the next unanswered question, if any; a pick-any toggle stays.
func (c *PrereviewController) saveAnswer(state *PrereviewState, set *QuestionSet, step int, a QuestionAnswer) error {
	a.SetID, a.StepID, a.At = set.ID, set.Steps[step].ID, time.Now().UnixNano()
	a.Sent = set.OneTap()
	answers := withEntry(state.QuestionAnswers, questionAnswerKey(set.ID, a.StepID), a)
	if err := saveQuestionAnswers(QuestionAnswersPath(c.CSVPath), answers); err != nil {
		return err
	}
	state.QuestionAnswers = answers
	state.QuestionOther = withoutEntry(state.QuestionOther, set.ID)
	switch {
	case set.OneTap():
		state.QuestionView = withoutEntry(state.QuestionView, set.ID)
		c.scheduleEmit()
	case !set.Steps[step].Multi || a.Other != "":
		if next := nextUnanswered(*set, answers, step); next >= 0 {
			state.QuestionView = withEntry(state.QuestionView, set.ID, next)
		}
	}
	return nil
}

// PickQuestionOption answers a tap on an option. On a pick-one step it is the
// answer, and the card moves on; on a pick-any step it toggles the option.
func (c *PrereviewController) PickQuestionOption(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	set, step, err := questionTarget(&state, ctx.GetString("set"), ctx.GetString("step"))
	if err != nil {
		return state, err
	}
	choice := ctx.GetInt("choice")
	if choice < 0 || choice >= len(set.Steps[step].Options) {
		return state, fmt.Errorf("option %d is out of range", choice)
	}
	if !set.Steps[step].Multi {
		return state, c.saveAnswer(&state, set, step, QuestionAnswer{Picked: []int{choice}})
	}
	picked := slices.Clone(state.QuestionAnswers[questionAnswerKey(set.ID, set.Steps[step].ID)].Picked)
	if i := slices.Index(picked, choice); i >= 0 {
		picked = slices.Delete(picked, i, i+1)
	} else {
		picked = append(picked, choice)
		slices.Sort(picked)
	}
	return state, c.saveAnswer(&state, set, step, QuestionAnswer{Picked: picked})
}

// OpenQuestionOther and CloseQuestionOther show and hide a step's "Something
// else" box.
func (c *PrereviewController) OpenQuestionOther(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	set, step, err := questionTarget(&state, ctx.GetString("set"), ctx.GetString("step"))
	if err != nil {
		return state, err
	}
	state.QuestionOther = withEntry(state.QuestionOther, set.ID, set.Steps[step].ID)
	return state, nil
}

func (c *PrereviewController) CloseQuestionOther(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.QuestionOther = withoutEntry(state.QuestionOther, ctx.GetString("set"))
	return state, nil
}

// AnswerQuestionOther records a typed answer to a step, in place of a pick.
func (c *PrereviewController) AnswerQuestionOther(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	set, step, err := questionTarget(&state, ctx.GetString("set"), ctx.GetString("step"))
	if err != nil {
		return state, err
	}
	body := strings.TrimSpace(ctx.GetString("body"))
	if body == "" {
		return state, fmt.Errorf("the answer is empty")
	}
	return state, c.saveAnswer(&state, set, step, QuestionAnswer{Other: body})
}

// QuestionStepTo shows a set's question by index: a tap on its tab.
func (c *PrereviewController) QuestionStepTo(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	set, err := findQuestionSet(&state, ctx.GetString("set"))
	if err != nil {
		return state, err
	}
	i := ctx.GetInt("index")
	if i < 0 || i >= len(set.Steps) {
		return state, fmt.Errorf("step %d is out of range", i)
	}
	state.QuestionView = withEntry(state.QuestionView, set.ID, i)
	return state, nil
}

// SendQuestionAnswers is Submit: it sends a set's answers to the agent, once
// every question is answered.
func (c *PrereviewController) SendQuestionAnswers(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.markQuestionSent(state, ctx.GetString("set"), true)
}

// ChangeQuestionAnswers takes a sent set back, until the agent marks it done:
// the card reopens on its first question, and Submit sends the new answers.
func (c *PrereviewController) ChangeQuestionAnswers(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	setID := ctx.GetString("set")
	if state.QuestionsDone[setID] {
		return state, fmt.Errorf("the agent has already handled %s", setID)
	}
	return c.markQuestionSent(state, setID, false)
}

// markQuestionSent sets or clears Sent on every step of a set, saves, and
// re-emits so the agent's snapshot follows. Either way the card's tab resets:
// a sent card folds, and one taken back opens on its first question.
func (c *PrereviewController) markQuestionSent(state PrereviewState, setID string, sent bool) (PrereviewState, error) {
	set, err := findQuestionSet(&state, setID)
	if err != nil {
		return state, err
	}
	answers := maps.Clone(state.QuestionAnswers)
	for _, st := range set.Steps {
		k := questionAnswerKey(set.ID, st.ID)
		a, ok := answers[k]
		if sent && (!ok || !a.answered()) {
			return state, fmt.Errorf("answer every question before sending")
		}
		if ok {
			a.Sent = sent
			answers[k] = a
		}
	}
	if err := saveQuestionAnswers(QuestionAnswersPath(c.CSVPath), answers); err != nil {
		return state, err
	}
	state.QuestionAnswers = answers
	state.QuestionView = withoutEntry(state.QuestionView, setID)
	c.scheduleEmit()
	return state, nil
}

// setQuestionStatus changes one set's status with edit, saves the file and
// re-emits, so a resolve reaches the agent.
func (c *PrereviewController) setQuestionStatus(state PrereviewState, id string, edit func(*QuestionStatus)) (PrereviewState, error) {
	set, err := findQuestionSet(&state, id)
	if err != nil {
		return state, err
	}
	st := state.QuestionStatus[set.ID]
	st.SetID = set.ID
	edit(&st)
	st.At = time.Now().UnixNano()
	next := withEntry(state.QuestionStatus, set.ID, st)
	if err := saveJSONLMap(QuestionStatusPath(c.CSVPath), next); err != nil {
		return state, err
	}
	state.QuestionStatus = next
	c.scheduleEmit()
	return state, nil
}

// ToggleQuestionResolved is Resolve and Reopen on a question card, as on a
// comment's. A resolved set leaves the queue; the agent is told once (until it
// marks the set done) so it stops waiting on it.
func (c *PrereviewController) ToggleQuestionResolved(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.setQuestionStatus(state, ctx.GetString("id"), func(st *QuestionStatus) { st.Resolved = !st.Resolved })
}

// HideQuestion and ShowQuestion take a card out of view and bring it back, as
// for a suggestion. View-only: the agent is not told.
func (c *PrereviewController) HideQuestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.setQuestionStatus(state, ctx.GetString("id"), func(st *QuestionStatus) { st.Hidden = true })
}

func (c *PrereviewController) ShowQuestion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.setQuestionStatus(state, ctx.GetString("id"), func(st *QuestionStatus) { st.Hidden = false })
}
