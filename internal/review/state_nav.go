package review

import (
	"cmp"
	"fmt"
	"slices"
)

// state_nav.go is the open-items stepper (#193): one ‹ N / M open › bar that walks
// every annotation still waiting on the reviewer, in document order across files —
// unresolved comments, undecided suggestions, unanswered quiz questions, and the
// agent's questions not yet submitted. n/p step the same list.

// Nav kinds: NavPos.Kind, and the kinds of the Annotations filter (annfilter.go).
const (
	navComment    = "comment"
	navSuggestion = "suggestion"
	navQuiz       = "quiz"
	navQuestion   = "question"
)

// navKindRank is each kind's tiebreak rank on a shared line.
var navKindRank = []string{navComment, navSuggestion, navQuiz, navQuestion}

func navKindIndex(kind string) int { return slices.Index(navKindRank, kind) }

// NavPos is a stepper position: the sort key of an item. The cursor stores the last
// target's key rather than its index, so deciding or resolving that item (which
// drops it from the list) leaves "next" continuing from where the reviewer was
// instead of restarting at item 1.
type NavPos struct {
	File int    `json:"file"` // rank in scopedFiles; unknown files sort last
	Body bool   `json:"body"` // false = file head (file/area comments, head questions)
	Line int    `json:"line"` // ToLine (FromLine for a question)
	Kind string `json:"kind"`
	Seq  int    `json:"seq"` // position in its source list: a stable tiebreak
	ID   string `json:"id"`
}

func (a NavPos) compare(b NavPos) int {
	if c := cmp.Compare(a.File, b.File); c != 0 {
		return c
	}
	if a.Body != b.Body {
		if !a.Body {
			return -1
		}
		return 1
	}
	return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(navKindIndex(a.Kind), navKindIndex(b.Kind)),
		cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.ID, b.ID))
}

// sameItem reports whether two positions name the same item. Seq is left out: it
// is a list index, and it shifts when an earlier item is removed.
func (a NavPos) sameItem(b NavPos) bool {
	return a.File == b.File && a.Kind == b.Kind && a.ID == b.ID
}

// NavItem is one stop: the file to open, the side its row is on, and where it sorts.
type NavItem struct {
	File   string
	Side   string
	Pos    NavPos
	Region bool // a live-site annotation (#114): a step shows the live site, not a file
}

// NavItems is the stepper's stops in document order: every open item whose own
// surface's filter shows it.
func (s PrereviewState) NavItems() []NavItem {
	var out []NavItem
	for _, e := range s.annotations() {
		// A request (Run judges, Quiz me) is the agent's work, not a reviewer's note.
		if !e.Settled && !e.Request && s.annFilter(e.Surface).stops(e.Pos.Kind) {
			out = append(out, e.NavItem)
		}
	}
	sortByReview(s, out, func(it *NavItem) *NavItem { return it })
	return out
}

// navStep picks the next (delta>0) or previous item relative to the cursor, wrapping.
// Nil when the list is empty. With no cursor, next starts at the first item and
// previous at the last.
func (s PrereviewState) navStep(delta int) *NavItem {
	items := s.NavItems()
	if len(items) == 0 {
		return nil
	}
	if s.NavAt == nil {
		if delta > 0 {
			return &items[0]
		}
		return &items[len(items)-1]
	}
	if delta > 0 {
		for i := range items {
			if items[i].Pos.compare(*s.NavAt) > 0 {
				return &items[i]
			}
		}
		return &items[0]
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Pos.compare(*s.NavAt) < 0 {
			return &items[i]
		}
	}
	return &items[len(items)-1]
}

// NavLabel is the bar's count: "3 / 12 open" while the cursor is on a live item,
// else "12 open".
func (s PrereviewState) NavLabel() string {
	items := s.NavItems()
	if s.NavAt != nil {
		for i, it := range items {
			if it.Pos.sameItem(*s.NavAt) {
				return fmt.Sprintf("%d / %d open", i+1, len(items))
			}
		}
	}
	return fmt.Sprintf("%d open", len(items))
}

// navSlot: a file view is showing (not a full-page view, not a live site, not a
// walkthrough, whose step bar takes the slot so two bars don't stack on a phone).
func (s PrereviewState) navSlot() bool {
	_, _, _, walking := s.walkPos()
	return !s.fileCovered() && !walking
}

// ShowNav gates the open-items row: the reviewer hasn't collapsed it, and the
// review has open items, or had some this visit (NavSeen), so deciding the last
// one leaves "0 open" rather than pulling the row out from under the reviewer
// (#212's rule). It counts after each file's filter.
func (s PrereviewState) ShowNav() bool {
	return !s.NavDismissed && s.navSlot() && (s.NavSeen || len(s.NavItems()) > 0)
}

// NavCollapsed shows the row as a chip carrying the filter's label: the
// reviewer collapsed it with ×, or the row is gone but the open file still has
// annotations of some kind or state. Either way the filter stays one click away,
// so no filter setting can hide its own control.
func (s PrereviewState) NavCollapsed() bool {
	if !s.navSlot() {
		return false
	}
	if s.NavDismissed {
		return true
	}
	return !s.NavSeen && len(s.NavItems()) == 0 && s.hasAnnotations()
}

// fileCovered: something covers the file view: a full-page view (the views
// closeFullViews closes) or the live site.
func (s PrereviewState) fileCovered() bool {
	return s.ShowAllComments || s.ShowUnderstand || s.ShowLibrary || s.LiveShowing()
}

// noteNav sets NavSeen once the review has open items. Called wherever items
// arrive: Mount, the watcher's reload (LLMStatusChanged), a new comment, a step.
func (s *PrereviewState) noteNav() {
	if !s.NavSeen && len(s.NavItems()) > 0 {
		s.NavSeen = true
	}
}

// fileRank is each file's place in the drawer, for ordering across files: files
// the drawer doesn't list sort after it.
func (s PrereviewState) fileRank() func(string) int {
	rank := map[string]int{}
	for i, f := range s.scopedFiles() {
		rank[f.Path] = i
	}
	return func(path string) int {
		if r, ok := rank[path]; ok {
			return r
		}
		return len(rank)
	}
}

// navAnchorPrefix is each kind's prefix in NavAnchor.
var navAnchorPrefix = map[string]string{navComment: "c:", navSuggestion: "sg:", navQuiz: "qz:", navQuestion: "qs:"}

// NavAnchor names the card the stepper last stepped to ("c:<id>", "sg:<id>",
// "qz:<id>" or "qs:<id>"), or "" before the first step. That card carries an
// anchor keyed by NavSteps: the client scrolls an element into view once, so a
// new key per step is what scrolls again when ‹ › land on the same card.
func (s PrereviewState) NavAnchor() string {
	if s.NavAt == nil || s.NavSteps == 0 {
		return ""
	}
	return navAnchorPrefix[s.NavAt.Kind] + s.NavAt.ID
}
