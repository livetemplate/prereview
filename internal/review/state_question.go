package review

import (
	"fmt"
	"slices"
	"strings"
)

// QuestionCard is a question set as its card renders it: the question on
// screen with a tab per question and Submit, or the folded answer once sent.
type QuestionCard struct {
	QuestionSet
	// StepIndex is the question on screen.
	StepIndex int
	Step      QuestionStep
	Chips     []QuestionChip
	Options   []QuestionOptionView
	// Other is the step's typed answer; OtherOpen shows its text box.
	Other     string
	OtherOpen bool
	// Complete: every question is answered, so Submit can send the set.
	Complete bool
	// Sent: the reviewer sent the answers; Done: the agent marked the set done.
	// QueueState is the set's queue row state (queued, done), "" before Submit.
	Sent       bool
	Done       bool
	QueueState string
	// Resolved and Hidden are the reviewer's resolve and hide (QuestionStatus).
	// InThread: the card renders in its comment's thread, where the comment's
	// own Resolve, Reply and Hide stand in for the card's.
	Resolved bool
	Hidden   bool
	InThread bool
	// Answer is the sent answers in one line, for the folded card.
	Answer string

	Thread []ThreadEntry
	// ReplyingID and ReplyDraft are set while the card's reply form is open;
	// the shared replyForm partial reads them.
	ReplyingID string
	ReplyDraft string
}

// QuestionChip is one question's tab on a set of several.
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

// FirstPrompt is the set's first question, shown on a resolved, unanswered card.
func (c QuestionCard) FirstPrompt() string { return c.Steps[0].Prompt }

// StepCount is how many questions the set holds.
func (c QuestionCard) StepCount() int { return len(c.Steps) }


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

// questionStepAt is the question the card shows: the tab the reviewer chose,
// or else the first unanswered question, or else the first.
func (s PrereviewState) questionStepAt(set QuestionSet) int {
	if i, ok := s.QuestionView[set.ID]; ok && i >= 0 && i < len(set.Steps) {
		return i
	}
	return max(nextUnanswered(set, s.QuestionAnswers, -1), 0)
}

// nextUnanswered is the first unanswered question after index from, wrapping
// round to the start, or -1 when every question is answered.
func nextUnanswered(set QuestionSet, answers map[string]QuestionAnswer, from int) int {
	n := len(set.Steps)
	for k := 1; k <= n; k++ {
		i := (from + k + n) % n
		if !answers[questionAnswerKey(set.ID, set.Steps[i].ID)].answered() {
			return i
		}
	}
	return -1
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
func (s PrereviewState) questionCard(set QuestionSet, threads map[string][]ThreadEntry, awaiting map[string]bool) QuestionCard {
	c := QuestionCard{QuestionSet: set, Sent: questionSetSent(set, s.QuestionAnswers),
		Done: s.QuestionsDone[set.ID], Thread: threads[set.ID],
		QueueState: reopenIfReplied(s.questionQueueState(set), set.ID, awaiting),
		Resolved:   s.questionResolved(set), Hidden: s.QuestionStatus[set.ID].Hidden,
		InThread:   s.inThread(set)}
	if s.ReplyingID == set.ID {
		c.ReplyingID, c.ReplyDraft = set.ID, s.ReplyDraft
	}
	if !c.Sent {
		c.StepIndex = s.questionStepAt(set)
	}
	var answers []string
	c.Complete = true
	for i, st := range set.Steps {
		a := s.QuestionAnswers[questionAnswerKey(set.ID, st.ID)]
		answers = append(answers, answerText(st, a))
		chip := st.Header
		if chip == "" {
			chip = fmt.Sprintf("%d", i+1)
		}
		c.Chips = append(c.Chips, QuestionChip{Index: i, Label: chip, Current: i == c.StepIndex, Answered: a.answered()})
		c.Complete = c.Complete && a.answered()
	}
	c.Answer = strings.Join(answers, " · ")
	if c.Sent {
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

// questionCards is the cards for the open file's sets that stand on their own:
// a set asked `for` a comment renders in that comment's thread instead
// (QuestionsByComment).
func (s PrereviewState) questionCards() []QuestionCard {
	if s.SelectedFile == "" || len(s.QuestionSets) == 0 {
		return nil
	}
	return s.questionCardsWhere(func(set QuestionSet) bool {
		return set.File == s.SelectedFile && !s.inThread(set)
	})
}

// QuestionsByComment is the question cards asked `for` each comment, by comment
// id: the comment card renders them after its thread, as the agent's reply.
// Every file's, so the All comments view has them too.
func (s PrereviewState) QuestionsByComment() map[string][]QuestionCard {
	if len(s.QuestionSets) == 0 {
		return nil
	}
	out := map[string][]QuestionCard{}
	for _, c := range s.questionCardsWhere(s.inThread) {
		out[c.For] = append(out[c.For], c)
	}
	return out
}

// inThread reports whether set renders in a comment's thread: it names a
// comment that exists.
func (s PrereviewState) inThread(set QuestionSet) bool {
	return set.For != "" && slices.ContainsFunc(s.Comments, func(c Comment) bool { return c.ID == set.For })
}

// questionResolved reports whether set is closed: the reviewer resolved it, or
// it is in a comment's thread and that comment is resolved (the comment's
// Resolve covers the question asked on it).
func (s PrereviewState) questionResolved(set QuestionSet) bool {
	if s.QuestionStatus[set.ID].Resolved {
		return true
	}
	return set.For != "" && slices.ContainsFunc(s.Comments, func(c Comment) bool { return c.ID == set.For && c.Resolved })
}

// questionsResolved is questionResolved for every in-scope set, by id: what the
// snapshot reports as "resolved".
func (s PrereviewState) questionsResolved() map[string]bool {
	out := map[string]bool{}
	for _, set := range s.scopedQuestionSets() {
		if s.questionResolved(set) {
			out[set.ID] = true
		}
	}
	return out
}

// questionCardsWhere builds the cards for the in-scope sets keep accepts. A
// resolved set follows "Show resolved", as a resolved comment does.
func (s PrereviewState) questionCardsWhere(keep func(QuestionSet) bool) []QuestionCard {
	threads, awaiting := s.Threads(), s.AwaitingAgent()
	var out []QuestionCard
	for _, set := range s.scopedQuestionSets() {
		if keep(set) && (!s.questionResolved(set) || s.ShowResolved) {
			out = append(out, s.questionCard(set, threads, awaiting))
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

// QuestionCountLines counts the question cards on each diff row, keyed
// "<line>-<side>" like CommentCountLines, so a question adds to its row's badge,
// folds with the row, and pins the row out of the diff's folds.
func (s PrereviewState) QuestionCountLines() map[string]int {
	out := map[string]int{}
	for end, cards := range s.QuestionsByEndLine() {
		for _, c := range cards {
			countRowSides(out, end, c.Side)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// QuestionOpenLines marks rows with an unresolved question: open, as an
// unresolved comment is, so the row's badge reads open.
func (s PrereviewState) QuestionOpenLines() map[string]bool {
	out := map[string]bool{}
	for end, cards := range s.QuestionsByEndLine() {
		for _, c := range cards {
			if !c.Resolved {
				for _, k := range rowKeysFor(end, c.Side) {
					out[k] = true
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// BlockQuestions is the open file's line-grounded question cards grouped by
// rendered block (Markdown or the HTML preview), like BlockComments, so they
// render under their block and count toward its marker.
func (s PrereviewState) BlockQuestions() map[string][]QuestionCard {
	var cards []QuestionCard
	for _, c := range s.questionCards() {
		if c.LineGrounded() {
			cards = append(cards, c)
		}
	}
	return groupByBlock(s, cards, func(c QuestionCard) int { return c.ToLine })
}
