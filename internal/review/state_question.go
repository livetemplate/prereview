package review

import (
	"fmt"
	"slices"
	"strings"
	"time"
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
	// InThread: the card renders in a comment's, suggestion's or quiz
	// question's thread. That card's Reply stands in for this one's, and a
	// comment's Resolve covers it (questionResolved); elsewhere, answering
	// closes it.
	// Replaced: a newer set in the same thread took its place before the
	// reviewer submitted it (questionReplaced).
	Resolved bool
	Hidden   bool
	InThread bool
	Replaced bool
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
		QueueState: s.questionRowState(set, awaiting),
		Resolved:   s.questionResolved(set), Hidden: s.QuestionStatus[set.ID].Hidden,
		InThread: s.inThread(set), Replaced: s.questionReplaced(set)}
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
// a set asked `for` a thread renders in that thread instead (ThreadViews).
func (s PrereviewState) questionCards() []QuestionCard {
	if s.SelectedFile == "" || len(s.QuestionSets) == 0 {
		return nil
	}
	return s.questionCardsWhere(func(set QuestionSet) bool {
		return set.File == s.SelectedFile && !s.inThread(set)
	})
}

// lineQuestionCards is the open file's question cards with a line in the diff:
// the ones a row or a rendered block shows.
func (s PrereviewState) lineQuestionCards() []QuestionCard {
	var out []QuestionCard
	for _, c := range s.questionCards() {
		if c.LineGrounded() {
			out = append(out, c)
		}
	}
	return out
}

// ThreadViews is every thread as its card renders it, by target id: the
// replies, with each question set the agent asked in the thread (`ask --for`)
// as the agent's reply. `ask` writes an agent entry naming the set, so the
// card takes that entry's place; a set with no entry goes in by when it was
// asked. Every file's, so the All comments view has them too.
func (s PrereviewState) ThreadViews() map[string][]ThreadItem {
	threads := s.Threads()
	var list []QuestionCard
	if len(s.QuestionSets) > 0 {
		list = s.questionCardsWhere(s.inThread)
	}
	cards := make(map[string]*QuestionCard, len(list))
	for i := range list {
		cards[list[i].ID] = &list[i]
	}
	out := make(map[string][]ThreadItem, len(threads))
	placed := map[string]bool{}
	for id, th := range threads {
		items := make([]ThreadItem, 0, len(th))
		for _, e := range th {
			it := ThreadItem{ThreadEntry: e}
			if c := cards[e.Question]; c != nil && c.For == id {
				if placed[c.ID] {
					continue // re-asked to revise: the card keeps its first place
				}
				it.Question, placed[c.ID] = c, true
			}
			items = append(items, it)
		}
		out[id] = items
	}
	for i := range list {
		c := &list[i]
		if placed[c.ID] {
			continue
		}
		items := out[c.For]
		at := c.askedAt()
		i := slices.IndexFunc(items, func(it ThreadItem) bool { return it.At > at })
		if i < 0 || at == 0 {
			i = len(items)
		}
		out[c.For] = slices.Insert(items, i, ThreadItem{
			ThreadEntry: ThreadEntry{TargetID: c.For, Author: AuthorAgent, At: at}, Question: c})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// askedAt is when the agent asked the set, in nanoseconds; 0 when unknown.
func (s QuestionSet) askedAt() int64 {
	t, err := time.Parse(time.RFC3339, s.CreatedAt)
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

// When is when the agent asked the set, as a thread entry shows its time.
func (c QuestionCard) When() string {
	return ThreadEntry{At: c.askedAt()}.When()
}

// forComment is the comment a set was asked for, if it exists.
func (s PrereviewState) forComment(set QuestionSet) (*Comment, bool) {
	if set.For == "" {
		return nil, false
	}
	i := slices.IndexFunc(s.Comments, func(c Comment) bool { return c.ID == set.For })
	if i < 0 {
		return nil, false
	}
	return &s.Comments[i], true
}

// inThread reports whether set renders in a thread: it names a comment, a
// suggestion or a quiz question that exists (the targets `prereview reply`
// takes).
func (s PrereviewState) inThread(set QuestionSet) bool {
	if set.For == "" {
		return false
	}
	if _, ok := s.forComment(set); ok {
		return true
	}
	if slices.ContainsFunc(s.Suggestions, func(sg Suggestion) bool { return sg.ID == set.For }) {
		return true
	}
	if qid, quest, ok := SplitQuizThreadID(set.For); ok {
		return slices.ContainsFunc(s.Quizzes, func(q Quiz) bool {
			return q.ID == qid && slices.ContainsFunc(q.Questions, func(qu Question) bool { return qu.ID == quest })
		})
	}
	return false
}

// questionResolved reports whether set is closed: the reviewer resolved it, or
// it is in a comment's thread and that comment is resolved (the comment's
// Resolve covers the question asked on it).
func (s PrereviewState) questionResolved(set QuestionSet) bool {
	if s.QuestionStatus[set.ID].Resolved {
		return true
	}
	c, ok := s.forComment(set)
	return ok && c.Resolved
}

// questionAwaitingAnswer reports whether set waits on the reviewer: not
// submitted, resolved, hidden or replaced. The stepper and the End-session
// warning share it.
func (s PrereviewState) questionAwaitingAnswer(set QuestionSet) bool {
	return !questionSetSent(set, s.QuestionAnswers) && !s.questionResolved(set) &&
		!s.QuestionStatus[set.ID].Hidden && !s.questionReplaced(set)
}

// questionReplaced reports whether set was replaced: it sits in a thread, the
// reviewer has not submitted it, and the agent has since asked a newer set in
// that thread. Its options answer a question the agent has moved past, so the
// card drops them.
func (s PrereviewState) questionReplaced(set QuestionSet) bool {
	if questionSetSent(set, s.QuestionAnswers) || !s.inThread(set) {
		return false
	}
	at := set.askedAt()
	return slices.ContainsFunc(s.QuestionSets, func(o QuestionSet) bool {
		return o.ID != set.ID && o.For == set.For && o.askedAt() > at
	})
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
// resolved card is kept, as a resolved inline comment is: the page folds it
// behind its row's badge, and "Show resolved" opens it.
func (s PrereviewState) questionCardsWhere(keep func(QuestionSet) bool) []QuestionCard {
	var (
		out      []QuestionCard
		threads  map[string][]ThreadEntry
		awaiting map[string]bool
	)
	for _, set := range s.scopedQuestionSets() {
		if !keep(set) {
			continue
		}
		if threads == nil {
			threads, awaiting = s.Threads(), s.AwaitingAgent()
		}
		out = append(out, s.questionCard(set, threads, awaiting))
	}
	return out
}

// QuestionsByEndLine is the open file's line-grounded question cards by the
// line they end on, like QuizByEndLine.
func (s PrereviewState) QuestionsByEndLine() map[int][]QuestionCard {
	out := map[int][]QuestionCard{}
	for _, c := range s.lineQuestionCards() {
		out[c.ToLine] = append(out[c.ToLine], c)
	}
	return out
}

// FileQuestionCards is the open file's other question cards, for the file
// head: file sets, and sets whose lines are not in the diff (shown with a
// warning, never dropped). The complement of QuestionsByEndLine, except that a
// resolved one follows "Show resolved", as a file-level comment does.
func (s PrereviewState) FileQuestionCards() []QuestionCard {
	var out []QuestionCard
	for _, c := range s.questionCards() {
		if !c.LineGrounded() && (!c.Resolved || s.ShowResolved) {
			out = append(out, c)
		}
	}
	return out
}

// BlockQuestions is the open file's line-grounded question cards grouped by
// rendered block (Markdown or the HTML preview), like BlockComments, so they
// render under their block and count toward its marker.
func (s PrereviewState) BlockQuestions() map[string][]QuestionCard {
	return groupByBlock(s, s.lineQuestionCards(), func(c QuestionCard) int { return c.ToLine })
}
