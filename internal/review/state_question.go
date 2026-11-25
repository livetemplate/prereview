package review

import (
	"fmt"
	"slices"
	"strings"
)

// QuestionCard is a question set as its card renders it: the step on screen,
// or the Review list, or the folded answer once sent.
type QuestionCard struct {
	QuestionSet
	// StepIndex is the step on screen; len(Steps) is the Review list.
	StepIndex int
	Step      QuestionStep
	Chips     []QuestionChip
	Options   []QuestionOptionView
	// Other is the step's typed answer; OtherOpen shows its text box.
	Other     string
	OtherOpen bool
	Summary   []QuestionSummaryRow
	// Sent: the reviewer sent the answers; Done: the agent marked the set done.
	Sent bool
	Done bool
	// Answer is the sent answers in one line, for the folded card.
	Answer string

	Thread []ThreadEntry
	// ReplyingID and ReplyDraft are set while the card's reply form is open;
	// the shared replyForm partial reads them.
	ReplyingID string
	ReplyDraft string
}

// QuestionChip is one step's chip on a set of several steps.
type QuestionChip struct {
	Index    int
	Label    string
	Answered bool
	Current  bool
}

// QuestionOptionView is one option of the step on screen.
type QuestionOptionView struct {
	Index       int
	Label       string
	Detail      string
	Recommended bool
	Picked      bool
}

// QuestionSummaryRow is one line of the Review list.
type QuestionSummaryRow struct {
	Index  int
	Label  string
	Answer string
}

// StepCount is how many questions the set holds.
func (c QuestionCard) StepCount() int { return len(c.Steps) }

// StepNum is the step on screen counted from 1, which is also the index Next
// goes to (past the last, the Review list); PrevIndex is where Back goes.
func (c QuestionCard) StepNum() int   { return c.StepIndex + 1 }
func (c QuestionCard) PrevIndex() int { return c.StepIndex - 1 }

// IsLast reports whether the step on screen is the set's last one.
func (c QuestionCard) IsLast() bool { return c.StepIndex == len(c.Steps)-1 }

// Reviewing reports whether the card shows the Review list.
func (c QuestionCard) Reviewing() bool { return c.StepIndex == len(c.Steps) }

// StepAnswered reports whether the step on screen has an answer, which is what
// lets a pick-any step move on.
func (c QuestionCard) StepAnswered() bool {
	return c.Other != "" || slices.ContainsFunc(c.Options, func(o QuestionOptionView) bool { return o.Picked })
}

// LineGrounded reports whether the set renders under a line of the diff: a
// line or text set whose lines exist. The rest render at the file head.
func (s QuestionSet) LineGrounded() bool {
	return s.Kind != QuestionKindFile && s.AnchorStatus != anchorUngrounded
}

// Ungrounded reports a set whose lines are not in the diff.
func (s QuestionSet) Ungrounded() bool { return s.AnchorStatus == anchorUngrounded }

// AnchorLabel is where the set points, in the comment cards' format: "L41-L42",
// "L41" or "file".
func (s QuestionSet) AnchorLabel() string {
	switch {
	case s.Kind == QuestionKindFile:
		return "file"
	case s.ToLine > s.FromLine:
		return fmt.Sprintf("L%d-L%d", s.FromLine, s.ToLine)
	}
	return fmt.Sprintf("L%d", s.FromLine)
}

// questionStepAt is the step the card shows: the one the reviewer moved to, or
// else the first unanswered step, or else the Review list.
func (s PrereviewState) questionStepAt(set QuestionSet) int {
	if i, ok := s.QuestionView[set.ID]; ok && i >= 0 && i <= len(set.Steps) {
		return i
	}
	for i, st := range set.Steps {
		if !s.QuestionAnswers[questionAnswerKey(set.ID, st.ID)].answered() {
			return i
		}
	}
	return len(set.Steps)
}

// answerText is one step's answer as the reviewer reads it.
func answerText(st QuestionStep, a QuestionAnswer) string {
	if a.Other != "" {
		return a.Other
	}
	var labels []string
	for _, i := range a.Picked {
		if i >= 0 && i < len(st.Options) {
			labels = append(labels, st.Options[i].Label)
		}
	}
	return strings.Join(labels, ", ")
}

// questionCard builds set's card from the review state.
func (s PrereviewState) questionCard(set QuestionSet, threads map[string][]ThreadEntry) QuestionCard {
	c := QuestionCard{QuestionSet: set, Sent: questionSetSent(set, s.QuestionAnswers),
		Done: s.QuestionsDone[set.ID], Thread: threads[set.ID]}
	if s.ReplyingID == set.ID {
		c.ReplyingID, c.ReplyDraft = set.ID, s.ReplyDraft
	}
	if !c.Sent {
		c.StepIndex = s.questionStepAt(set)
	}
	var answers []string
	for i, st := range set.Steps {
		a := s.QuestionAnswers[questionAnswerKey(set.ID, st.ID)]
		text := answerText(st, a)
		answers = append(answers, text)
		label, chip := st.Header, st.Header
		if label == "" {
			label, chip = st.Prompt, fmt.Sprintf("%d", i+1)
		}
		c.Summary = append(c.Summary, QuestionSummaryRow{Index: i, Label: label, Answer: text})
		c.Chips = append(c.Chips, QuestionChip{Index: i, Label: chip, Current: i == c.StepIndex, Answered: a.answered()})
	}
	c.Answer = strings.Join(answers, " · ")
	if c.Sent || c.Reviewing() {
		return c
	}
	c.Step = set.Steps[c.StepIndex]
	a := s.QuestionAnswers[questionAnswerKey(set.ID, c.Step.ID)]
	c.Other = a.Other
	c.OtherOpen = s.QuestionOther[set.ID] == c.Step.ID
	for i, o := range c.Step.Options {
		c.Options = append(c.Options, QuestionOptionView{Index: i, Label: o.Label, Detail: o.Detail,
			Recommended: c.Step.Recommended != nil && *c.Step.Recommended == i,
			Picked:      slices.Contains(a.Picked, i)})
	}
	return c
}

// questionCards is the cards for the open file's sets.
func (s PrereviewState) questionCards() []QuestionCard {
	if s.SelectedFile == "" || len(s.QuestionSets) == 0 {
		return nil
	}
	threads := s.Threads()
	var out []QuestionCard
	for _, set := range s.QuestionSets {
		if set.File == s.SelectedFile {
			out = append(out, s.questionCard(set, threads))
		}
	}
	return out
}

// QuestionsByEndLine is the open file's line-grounded question cards by the
// line they end on, like QuizByEndLine.
func (s PrereviewState) QuestionsByEndLine() map[int][]QuestionCard {
	out := map[int][]QuestionCard{}
	for _, c := range s.questionCards() {
		if c.LineGrounded() {
			out[c.ToLine] = append(out[c.ToLine], c)
		}
	}
	return out
}

// FileQuestionCards is the open file's other question cards, for the file
// head: file sets, and sets whose lines are not in the diff (shown with a
// warning, never dropped). The exact complement of QuestionsByEndLine.
func (s PrereviewState) FileQuestionCards() []QuestionCard {
	var out []QuestionCard
	for _, c := range s.questionCards() {
		if !c.LineGrounded() {
			out = append(out, c)
		}
	}
	return out
}
