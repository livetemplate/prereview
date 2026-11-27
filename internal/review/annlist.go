package review

import (
	"slices"
	"strings"
)

// annlist.go is the one walk over every annotation of the review: comments,
// suggestions, questions and quiz questions, in any state, in review order. The
// stepper (NavItems), the filter's counts (annCounts) and the Annotations page
// (AnnotationList) all read it, so they always agree on what exists.

// annEntry is one annotation. Exactly one of the source pointers is set.
type annEntry struct {
	NavItem
	Surface  string // the filter key it is drawn under (commentSurface, or the file)
	Settled  bool   // resolved, decided, answered or submitted: no longer open
	Hidden   bool   // a resolved comment hidden on its own (#88)
	Request  bool   // a comment that asks the agent for work (Run judges, Quiz me)
	comment  *Comment
	sugg     *Suggestion
	question *QuestionSet
	quiz     *QuizItem
}

// annotations is every in-scope annotation, unsorted (sortByReview orders them).
func (s PrereviewState) annotations() []annEntry {
	var out []annEntry
	comments := s.scopedComments()
	for i := range comments {
		c := &comments[i]
		body := !c.IsFileLevel() && !c.IsAreaLevel()
		out = append(out, annEntry{
			NavItem: NavItem{File: c.File, Side: c.Side, Pos: NavPos{Body: body, Line: c.ToLine, Kind: navComment, Seq: i, ID: c.ID}, Region: c.IsRegionLevel()},
			Surface: commentSurface(*c), Settled: c.Resolved, Hidden: c.Resolved && c.Hidden, Request: c.Request != "", comment: c,
		})
	}
	decisions := s.DecisionsBySuggestion()
	suggs := s.visibleSuggestions()
	for i := range suggs {
		sg := &suggs[i]
		out = append(out, annEntry{
			NavItem: NavItem{File: sg.File, Side: sg.Side, Pos: NavPos{Body: true, Line: sg.ToLine, Kind: navSuggestion, Seq: i, ID: sg.ID}},
			Surface: sg.File, Settled: !s.undecidedIn(decisions, sg.ID), sugg: sg,
		})
	}
	quiz := s.allQuizItems()
	for i := range quiz {
		it := &quiz[i]
		out = append(out, annEntry{
			NavItem: NavItem{File: it.File, Side: it.Side, Pos: NavPos{Body: it.LineGrounded(), Line: it.FromLine, Kind: navQuiz, Seq: it.Num, ID: it.ID}},
			Surface: it.File, Settled: it.Answered, quiz: it,
		})
	}
	sets := s.scopedQuestionSets()
	for i := range sets {
		set := &sets[i]
		out = append(out, annEntry{
			NavItem: NavItem{File: set.File, Side: set.Side, Pos: NavPos{Body: set.LineGrounded(), Line: set.FromLine, Kind: navQuestion, Seq: i, ID: set.ID}},
			Surface: set.File, Settled: !s.questionAwaitingAnswer(*set), question: set,
		})
	}
	return out
}

// sortByReview orders items as the stepper walks them: file by file in drawer
// order, the file head first, then by line. Callers sort only what they keep,
// since annotations runs several times a render.
func sortByReview[T any](s PrereviewState, items []T, item func(*T) *NavItem) {
	rankOf := s.fileRank()
	for i := range items {
		it := item(&items[i])
		it.Pos.File = rankOf(it.File)
	}
	slices.SortFunc(items, func(a, b T) int { return item(&a).Pos.compare(item(&b).Pos) })
}

// AnnListRow is one row of the Annotations page. A comment keeps its full card
// (Comment set); the other kinds are a row that jumps to the card on its file.
type AnnListRow struct {
	Key     string // the row's data-key: one value, so rows never share a key
	Kind    string
	ID      string
	Comment *Comment
	Label   string // "Suggestion", "Question", "Quiz"
	File    string
	Span    string // "L12", "L3-L5", "file"
	Text    string
	State   string // "" while open; "decided", "answered" or "sent" once settled
}

// annRowPrefix is each kind's data-key prefix, so a comment and a suggestion
// with the same id never share a key. A comment keeps its bare id, the key its
// card has always had.
var annRowPrefix = map[string]string{navComment: "", navSuggestion: "s:", navQuestion: "q:", navQuiz: "z:"}

// AnnotationList is the Annotations page: every annotation its own filter
// shows, in review order. This and the page's counts below are nil or zero
// unless the page is open, since every zero-arg method runs on each render.
func (s PrereviewState) AnnotationList() []AnnListRow {
	if !s.ShowAllComments {
		return nil
	}
	kept := s.listedEntries(true)
	if len(kept) == 0 {
		return nil
	}
	sortByReview(s, kept, func(e *annEntry) *NavItem { return &e.NavItem })
	out := make([]AnnListRow, 0, len(kept))
	for _, e := range kept {
		out = append(out, annRow(e))
	}
	return out
}

// listedEntries is what the page lists under its filter; chip applies the
// Live site chip too.
func (s PrereviewState) listedEntries(chip bool) []annEntry {
	list := s.annFilter(annListKey)
	if list.Show == annShowNone {
		return nil
	}
	liveOnly := chip && s.CommentListFilter == CommentFilterAnnotations
	var out []annEntry
	for _, e := range s.annotations() {
		if list.kindOn(e.Pos.Kind) && (!liveOnly || e.Region) &&
			(!e.Settled || (list.showsResolved() && !e.Hidden)) {
			out = append(out, e)
		}
	}
	return out
}

// resolvableIDs is what Resolve all resolves: the open comments the page lists,
// so it never reaches one the filter hides.
func (s PrereviewState) resolvableIDs() []string {
	var ids []string
	for _, e := range s.listedEntries(true) {
		if e.comment != nil && !e.Settled {
			ids = append(ids, e.Pos.ID)
		}
	}
	return ids
}

// UnresolvedListed is the count on Resolve all.
func (s PrereviewState) UnresolvedListed() int {
	if !s.ShowAllComments {
		return 0
	}
	return len(s.resolvableIDs())
}

// AnnotationsListed is the count on the Live site chip: the live site's boxes
// the page lists. Zero hides the chip.
func (s PrereviewState) AnnotationsListed() int {
	n := 0
	if s.ShowAllComments {
		for _, e := range s.listedEntries(false) {
			if e.Region {
				n++
			}
		}
	}
	return n
}

// HasAnyAnnotation tells the page's empty state whether the filter is what
// hides everything.
func (s PrereviewState) HasAnyAnnotation() bool {
	return s.ShowAllComments && len(s.annotations()) > 0
}

func annRow(e annEntry) AnnListRow {
	r := AnnListRow{Key: annRowPrefix[e.Pos.Kind] + e.Pos.ID, Kind: e.Pos.Kind, ID: e.Pos.ID, File: e.File}
	switch {
	case e.comment != nil:
		r.Comment = e.comment
	case e.sugg != nil:
		r.Label, r.Span, r.Text = "Suggestion", e.sugg.LineSpan(), firstLine(suggestionQueueBody(*e.sugg))
		if e.Settled {
			r.State = "decided"
		}
	case e.question != nil:
		r.Label, r.Span = "Question", e.question.AnchorLabel()
		if len(e.question.Steps) > 0 {
			r.Text = firstLine(e.question.Steps[0].Prompt)
		}
		if e.Settled {
			r.State = "sent"
		}
	case e.quiz != nil:
		r.Label, r.Span, r.Text = "Quiz", "file", firstLine(e.quiz.Prompt)
		if e.quiz.LineGrounded() {
			r.Span = e.quiz.AnchorLabel()
		}
		if e.Settled {
			r.State = "answered"
		}
	}
	return r
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
