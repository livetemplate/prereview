package review

import (
	"cmp"
	"fmt"
	"slices"
)

// state_nav.go is the open-items stepper (#193): one ‹ N / M open › bar that walks
// every annotation still waiting on the reviewer, in document order across files —
// unresolved comments, undecided suggestions, unanswered quiz questions. n/p step
// the same list.

// Nav kinds: the filter values (NavKind) and NavPos.Kind.
const (
	navComment    = "comment"
	navSuggestion = "suggestion"
	navQuiz       = "quiz"
)

// navKindTable is the filter chip's menu, in order. An item kind's index here is
// also its tiebreak rank on a shared line (comment, suggestion, question).
var navKindTable = []struct{ Kind, Label string }{
	{"", "All"}, {navComment, "Comments"}, {navSuggestion, "Suggestions"}, {navQuiz, "Quiz"},
}

func navKindIndex(kind string) int {
	return slices.IndexFunc(navKindTable, func(k struct{ Kind, Label string }) bool { return k.Kind == kind })
}

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

// navItems is every open item in the review, sorted, before the kind filter.
func (s PrereviewState) navItems() []NavItem {
	var out []NavItem
	for i, c := range s.scopedComments() {
		// A request (Run judges, Quiz me) is the agent's work, not a reviewer's note.
		if !c.Resolved && c.Request == "" {
			body := !c.IsFileLevel() && !c.IsAreaLevel()
			out = append(out, NavItem{File: c.File, Side: c.Side, Pos: NavPos{Body: body, Line: c.ToLine, Kind: navComment, Seq: i, ID: c.ID}, Region: c.IsRegionLevel()})
		}
	}
	if !s.HideSuggestions {
		decisions := s.DecisionsBySuggestion()
		for i, sg := range s.visibleSuggestions() {
			if s.undecidedIn(decisions, sg.ID) {
				out = append(out, NavItem{File: sg.File, Side: sg.Side, Pos: NavPos{Body: true, Line: sg.ToLine, Kind: navSuggestion, Seq: i, ID: sg.ID}})
			}
		}
	}
	for _, it := range s.allQuizItems() {
		if !it.Answered {
			out = append(out, NavItem{File: it.File, Side: it.Side, Pos: NavPos{Body: it.LineGrounded(), Line: it.FromLine, Kind: navQuiz, Seq: it.Num, ID: it.ID}})
		}
	}
	rankOf := s.fileRank()
	for i := range out {
		out[i].Pos.File = rankOf(out[i].File)
	}
	slices.SortFunc(out, func(a, b NavItem) int { return a.Pos.compare(b.Pos) })
	return out
}

// NavItems is the stepper's list under the current kind filter.
func (s PrereviewState) NavItems() []NavItem {
	all := s.navItems()
	if s.NavKind == "" {
		return all
	}
	return slices.DeleteFunc(all, func(it NavItem) bool { return it.Pos.Kind != s.NavKind })
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

// NavKindOption is one row of the filter chip's menu.
type NavKindOption struct {
	Kind, Label string
	On          bool
}

// NavKinds is the filter chip's menu, the active row marked.
func (s PrereviewState) NavKinds() []NavKindOption {
	out := make([]NavKindOption, len(navKindTable))
	for i, k := range navKindTable {
		out[i] = NavKindOption{Kind: k.Kind, Label: k.Label, On: k.Kind == s.NavKind}
	}
	return out
}

// NavKindLabel names the active filter on its chip.
func (s PrereviewState) NavKindLabel() string {
	if i := navKindIndex(s.NavKind); i >= 0 {
		return navKindTable[i].Label
	}
	return navKindTable[0].Label
}

// ShowNav gates the bar: the reviewer hasn't put it away, a file view is showing
// (not a full-page view, not a live site), and the review has open items, or had
// some this visit (NavSeen), so deciding the last one leaves the bar at "0 open"
// rather than pulling it out from under the reviewer (#212's rule). It counts
// before the filter, so a filter that matches nothing still leaves the chip.
func (s PrereviewState) ShowNav() bool {
	// Hidden while walking a walkthrough (#115): its step bar takes the slot, and
	// two bars would stack on a phone.
	_, _, _, walking := s.walkPos()
	if s.NavDismissed || s.fileCovered() || walking {
		return false
	}
	return s.NavSeen || len(s.navItems()) > 0
}

// fileCovered: something covers the file view: a full-page view (the views
// closeFullViews closes) or the live site.
func (s PrereviewState) fileCovered() bool {
	return s.ShowAllComments || s.ShowUnderstand || s.ShowLibrary || s.LiveShowing()
}

// noteNav sets NavSeen once the review has open items. Called wherever items
// arrive: Mount, the watcher's reload (LLMStatusChanged), a new comment, a step.
func (s *PrereviewState) noteNav() {
	if !s.NavSeen && len(s.navItems()) > 0 {
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
