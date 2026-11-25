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

// questionTarget reads the set and step a card's form names and returns them,
// with the step's index.
func questionTarget(state *PrereviewState, setID, stepID string) (*QuestionSet, int, error) {
	for i := range state.QuestionSets {
		set := &state.QuestionSets[i]
		if set.ID != setID {
			continue
		}
		for j, st := range set.Steps {
			if st.ID == stepID {
				return set, j, nil
			}
		}
		return nil, 0, fmt.Errorf("question set %s has no step %q", setID, stepID)
	}
	return nil, 0, fmt.Errorf("no question set %q", setID)
}

// setQuestionView moves set's card to step i (len(steps) is the Review list).
func setQuestionView(state *PrereviewState, setID string, i int) {
	view := maps.Clone(state.QuestionView)
	if view == nil {
		view = map[string]int{}
	}
	view[setID] = i
	state.QuestionView = view
}

// saveAnswer records a for a set's step and saves the answers file. A one-tap set
// (one step, pick one) is sent at once; any other moves on to the next step,
// or to the Review list after the last.
func (c *PrereviewController) saveAnswer(state *PrereviewState, set *QuestionSet, step int, a QuestionAnswer, advance bool) error {
	answers := maps.Clone(state.QuestionAnswers)
	if answers == nil {
		answers = map[string]QuestionAnswer{}
	}
	a.SetID, a.StepID, a.At = set.ID, set.Steps[step].ID, time.Now().UnixNano()
	oneTap := len(set.Steps) == 1 && !set.Steps[0].Multi
	a.Sent = oneTap
	answers[questionAnswerKey(set.ID, a.StepID)] = a
	if err := saveQuestionAnswers(QuestionAnswersPath(c.CSVPath), answers); err != nil {
		return err
	}
	state.QuestionAnswers = answers
	state.QuestionOther = maps.Clone(state.QuestionOther) // never mutate a rendered map in place
	delete(state.QuestionOther, set.ID)
	switch {
	case oneTap:
		state.QuestionView = maps.Clone(state.QuestionView)
		delete(state.QuestionView, set.ID)
		c.scheduleEmit()
	case advance:
		setQuestionView(state, set.ID, step+1)
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
		return state, c.saveAnswer(&state, set, step, QuestionAnswer{Picked: []int{choice}}, true)
	}
	picked := slices.Clone(state.QuestionAnswers[questionAnswerKey(set.ID, set.Steps[step].ID)].Picked)
	if i := slices.Index(picked, choice); i >= 0 {
		picked = slices.Delete(picked, i, i+1)
	} else {
		picked = append(picked, choice)
		slices.Sort(picked)
	}
	return state, c.saveAnswer(&state, set, step, QuestionAnswer{Picked: picked}, false)
}

// OpenQuestionOther and CloseQuestionOther show and hide a step's "Something
// else" box.
func (c *PrereviewController) OpenQuestionOther(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	set, step, err := questionTarget(&state, ctx.GetString("set"), ctx.GetString("step"))
	if err != nil {
		return state, err
	}
	other := maps.Clone(state.QuestionOther)
	if other == nil {
		other = map[string]string{}
	}
	other[set.ID] = set.Steps[step].ID
	state.QuestionOther = other
	return state, nil
}

func (c *PrereviewController) CloseQuestionOther(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.QuestionOther = maps.Clone(state.QuestionOther)
	delete(state.QuestionOther, ctx.GetString("set"))
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
	return state, c.saveAnswer(&state, set, step, QuestionAnswer{Other: body}, true)
}

// QuestionStepTo moves a card to a step (Back, a chip, Change on the Review
// list), or with step = the step count, to the Review list.
func (c *PrereviewController) QuestionStepTo(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	setID := ctx.GetString("set")
	i := ctx.GetInt("index")
	for _, set := range state.QuestionSets {
		if set.ID == setID {
			if i < 0 || i > len(set.Steps) {
				return state, fmt.Errorf("step %d is out of range", i)
			}
			setQuestionView(&state, setID, i)
			return state, nil
		}
	}
	return state, fmt.Errorf("no question set %q", setID)
}

// SendQuestionAnswers sends a set's answers to the agent: every step must be
// answered.
func (c *PrereviewController) SendQuestionAnswers(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.markQuestionSent(state, ctx.GetString("set"), true)
}

// ChangeQuestionAnswers takes a sent set back, until the agent marks it done:
// the card reopens on its Review list, and Send delivers the new answers.
func (c *PrereviewController) ChangeQuestionAnswers(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	setID := ctx.GetString("set")
	if state.QuestionsDone[setID] {
		return state, fmt.Errorf("the agent has already handled %s", setID)
	}
	state, err := c.markQuestionSent(state, setID, false)
	if err == nil {
		for _, set := range state.QuestionSets {
			if set.ID == setID {
				setQuestionView(&state, setID, len(set.Steps))
			}
		}
	}
	return state, err
}

// markQuestionSent sets or clears Sent on every step of a set, saves, and
// re-emits so the agent's snapshot follows.
func (c *PrereviewController) markQuestionSent(state PrereviewState, setID string, sent bool) (PrereviewState, error) {
	i := slices.IndexFunc(state.QuestionSets, func(s QuestionSet) bool { return s.ID == setID })
	if i < 0 {
		return state, fmt.Errorf("no question set %q", setID)
	}
	set := state.QuestionSets[i]
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
	if sent {
		state.QuestionView = maps.Clone(state.QuestionView)
		delete(state.QuestionView, setID)
	}
	c.scheduleEmit()
	return state, nil
}
