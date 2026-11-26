package review

import (
	"slices"
	"strconv"
)

// annfilter.go is the Annotations filter: what each surface draws of its
// comments, suggestions, questions and quiz cards, and what the open-items
// stepper stops on there. Every file has its own setting, so do the live site's
// pages and the Annotations page; one nobody has touched shows open items of
// every kind.

// AnnFilter is one surface's setting. The zero value is the default.
type AnnFilter struct {
	// Show is annShowOpen (""), annShowAll (resolved too) or annShowNone.
	Show string `json:"show,omitempty"`
	// Off lists the kinds turned off (navComment, navSuggestion, navQuestion,
	// navQuiz).
	Off []string `json:"off,omitempty"`
}

const (
	annShowOpen = ""
	annShowAll  = "all"
	annShowNone = "none"

	// annListKey is the Annotations page's own setting.
	annListKey = ":annotations"
	// liveKeyPrefix keys a live-site page by its URL; it has no file.
	liveKeyPrefix = "live:"
)

// annKinds is the Kinds list, in the order the filter shows it.
var annKinds = []struct{ Kind, Label string }{
	{navComment, "Comments"}, {navSuggestion, "Suggestions"}, {navQuestion, "Questions"}, {navQuiz, "Quiz"},
}

// kindOn reports whether kind is ticked. Show None is separate: the page hides
// everything with CSS, so the cards keep their places behind their badges.
func (f AnnFilter) kindOn(kind string) bool { return !slices.Contains(f.Off, kind) }

// stops reports whether the stepper stops on an open item of kind here.
func (f AnnFilter) stops(kind string) bool { return f.Show != annShowNone && f.kindOn(kind) }

func (f AnnFilter) showsResolved() bool { return f.Show == annShowAll }

// withKind turns kind on or off.
func (f AnnFilter) withKind(kind string, on bool) AnnFilter {
	off := slices.DeleteFunc(slices.Clone(f.Off), func(k string) bool { return k == kind })
	if !on {
		off = append(off, kind)
	}
	if len(off) == 0 {
		off = nil
	}
	f.Off = off
	return f
}

// flipShow switches Show between show and Open.
func (f AnnFilter) flipShow(show string) AnnFilter {
	if f.Show == show {
		f.Show = annShowOpen
	} else {
		f.Show = show
	}
	return f
}

func (f AnnFilter) flipKind(kind string) AnnFilter { return f.withKind(kind, !f.kindOn(kind)) }

func (f AnnFilter) isDefault() bool { return f.Show == annShowOpen && len(f.Off) == 0 }

func (s PrereviewState) annFilter(key string) AnnFilter { return s.AnnFilters[key] }

// newItemFiles is the files of the items whose id is not in prev: what the
// agent just sent, as opposed to a revision, which keeps its id.
func newItemFiles[T any](prev map[string]bool, items []T, idFile func(T) (id, file string)) []string {
	var files []string
	for _, it := range items {
		if id, file := idFile(it); !prev[id] {
			files = append(files, file)
		}
	}
	return files
}

// revealKind turns kind back on in each file's filter: the agent sent something
// new of that kind there, and a filter must never swallow fresh work (#116).
func (s *PrereviewState) revealKind(kind string, files []string) {
	// files may repeat; withKind is idempotent.
	for _, file := range files {
		if f := s.annFilter(file); !f.kindOn(kind) {
			s.setAnnFilter(file, f.withKind(kind, true))
		}
	}
}

// setAnnFilter stores f for key; the default deletes the key so the persisted
// map holds only surfaces the reviewer changed.
func (s *PrereviewState) setAnnFilter(key string, f AnnFilter) {
	if f.isDefault() {
		delete(s.AnnFilters, key)
		return
	}
	if s.AnnFilters == nil {
		s.AnnFilters = map[string]AnnFilter{}
	}
	s.AnnFilters[key] = f
}

// annSurface is the key of what the reviewer is looking at: the Annotations
// page, a live-site page, or the open file.
func (s PrereviewState) annSurface() string {
	switch {
	case s.ShowAllComments:
		return annListKey
	case s.LiveShowing():
		return liveKeyPrefix + s.CurrentURL
	}
	return s.SelectedFile
}

// commentSurface is the surface a comment is drawn on.
func commentSurface(c Comment) string {
	if c.IsRegionLevel() {
		return liveKeyPrefix + c.URL
	}
	return c.File
}

func (s PrereviewState) openFilter() AnnFilter { return s.annFilter(s.annSurface()) }

// editAnnFilter applies edit to the open surface's filter.
func (s *PrereviewState) editAnnFilter(edit func(AnnFilter) AnnFilter) {
	key := s.annSurface()
	s.setAnnFilter(key, edit(s.annFilter(key)))
}

// ShowResolved, HideMarks and HideSuggestions read the open surface's filter.
// They keep the names of the toggles they replace, so templates that read them
// need no change.
func (s PrereviewState) ShowResolved() bool    { return s.openFilter().showsResolved() }
func (s PrereviewState) HideMarks() bool       { return s.openFilter().Show == annShowNone }
func (s PrereviewState) HideSuggestions() bool { return !s.openFilter().kindOn(navSuggestion) }

// kindShownHere reports whether the open surface draws cards of kind.
func (s PrereviewState) kindShownHere(kind string) bool { return s.openFilter().kindOn(kind) }

// commentHiddenOn is the comment rule for a surface's filter f: its kind is off,
// or it is resolved and either resolved items are hidden or it was hidden on its
// own (#88). An open comment is never hidden by the resolved rule.
func commentHiddenOn(f AnnFilter, c Comment) bool {
	return !f.kindOn(navComment) || (c.Resolved && (!f.showsResolved() || c.Hidden))
}

// AnnShowOption is one Show row of the filter panel.
type AnnShowOption struct {
	Value, Label string
	Count        int // items it shows; 0 for None
	On           bool
}

// AnnKindOption is one Kinds row of the filter panel.
type AnnKindOption struct {
	Kind, Label string
	Count       int // this surface's items of the kind, any state
	On          bool
}

// AnnFilterPanel is everything the filter's trigger and panel render.
type AnnFilterPanel struct {
	Label             string // "Open", "All", "None", "Open · 2 kinds off"
	Shows             []AnnShowOption
	Kinds             []AnnKindOption
	HiddenResolved    int // resolved comments hidden one by one (#88)
	HiddenSuggestions int
}

// AnnFilterView is the open surface's filter panel.
func (s PrereviewState) AnnFilterView() AnnFilterPanel {
	f := s.openFilter()
	counts := s.annCounts()
	label := "Open"
	switch f.Show {
	case annShowAll:
		label = "All"
	case annShowNone:
		label = "None"
	}
	p := AnnFilterPanel{
		Shows: []AnnShowOption{
			{Value: "open", Label: "Open", Count: counts.open, On: f.Show == annShowOpen},
			{Value: annShowAll, Label: "All, with resolved", Count: counts.open + counts.resolved, On: f.Show == annShowAll},
			{Value: annShowNone, Label: "None", On: f.Show == annShowNone},
		},
		HiddenSuggestions: s.HiddenSuggestionCount(),
	}
	off := 0
	for _, k := range annKinds {
		on := f.kindOn(k.Kind)
		if !on {
			off++
		}
		p.Kinds = append(p.Kinds, AnnKindOption{Kind: k.Kind, Label: k.Label, Count: counts.byKind[k.Kind], On: on})
	}
	if f.showsResolved() {
		p.HiddenResolved = counts.hiddenResolved
	}
	if off > 0 && f.Show != annShowNone {
		label += " · " + kindsOffLabel(off)
	}
	p.Label = label
	return p
}

func kindsOffLabel(n int) string {
	if n == 1 {
		return "1 kind off"
	}
	return strconv.Itoa(n) + " kinds off"
}

type annCount struct {
	open, resolved, hiddenResolved int
	byKind                         map[string]int
}

// annCounts counts the open surface's annotations before the filter: open and
// resolved, and per kind in any state.
func (s PrereviewState) annCounts() annCount {
	key := s.annSurface()
	n := annCount{byKind: map[string]int{}}
	add := func(kind string, resolved bool) {
		n.byKind[kind]++
		if resolved {
			n.resolved++
		} else {
			n.open++
		}
	}
	onSurface := func(file string) bool { return key == annListKey || file == key }
	for _, c := range s.scopedComments() {
		if c.Request == "" && (key == annListKey || commentSurface(c) == key) {
			add(navComment, c.Resolved)
			if c.Resolved && c.Hidden {
				n.hiddenResolved++
			}
		}
	}
	decisions := s.DecisionsBySuggestion()
	for _, sg := range s.visibleSuggestions() {
		if onSurface(sg.File) {
			add(navSuggestion, !s.undecidedIn(decisions, sg.ID))
		}
	}
	for _, set := range s.scopedQuestionSets() {
		if onSurface(set.File) {
			add(navQuestion, !s.questionAwaitingAnswer(set))
		}
	}
	for _, it := range s.allQuizItems() {
		if onSurface(it.File) {
			add(navQuiz, it.Answered)
		}
	}
	return n
}

// hasAnnotations reports whether the open surface has any annotation, in any
// state, before the filter. It stops at the first one, cheapest source first.
func (s PrereviewState) hasAnnotations() bool {
	key := s.annSurface()
	for _, c := range s.scopedComments() {
		if c.Request == "" && commentSurface(c) == key {
			return true
		}
	}
	for _, sg := range s.visibleSuggestions() {
		if sg.File == key {
			return true
		}
	}
	for _, set := range s.scopedQuestionSets() {
		if set.File == key {
			return true
		}
	}
	for _, it := range s.allQuizItems() {
		if it.File == key {
			return true
		}
	}
	return false
}
