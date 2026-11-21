package review

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/livetemplate/prereview/csv"
)

// state_explain.go holds the Understand page's views (#115). Zero-arg methods
// only: livetemplate pre-computes nothing else.

// CurrentExplanation is the walkthrough the page shows: the newest one.
func (s PrereviewState) CurrentExplanation() *Explanation {
	if len(s.Explanations) == 0 {
		return nil
	}
	return &s.Explanations[len(s.Explanations)-1]
}

// HasExplanation reports there is a walkthrough to show.
func (s PrereviewState) HasExplanation() bool { return len(s.Explanations) > 0 }

// ExplainPending: an explain request is still open in the queue and no
// walkthrough has arrived since it was sent, so the button reads "Walkthrough
// requested…" instead of inviting a second tap. Derived from the request row, so
// every tab agrees and a reload keeps it.
func (s PrereviewState) ExplainPending() bool {
	asked := s.openRequest(csv.RequestExplain, "")
	if asked.IsZero() {
		return false
	}
	if e := s.CurrentExplanation(); e != nil {
		if at, err := time.Parse(time.RFC3339, e.CreatedAt); err == nil && !at.Before(asked.Truncate(time.Second)) {
			return false
		}
	}
	return true
}

// CanExplain reports an agent can be asked for a walkthrough.
func (s PrereviewState) CanExplain() bool { return s.AgentMode && len(s.ExplainPrompts) > 0 }

// HasUnderstandContent: the Understand page has something on it, a walkthrough
// or the open file's quiz. Gates the menu entries.
func (s PrereviewState) HasUnderstandContent() bool { return s.HasExplanation() || s.HasQuiz() }

// ShowUnderstandEntry gates the file header's Understand button: there is
// something on the page, or an agent to ask for it.
func (s PrereviewState) ShowUnderstandEntry() bool {
	return s.CanExplain() || len(s.QuizPrompts) > 0 || s.HasUnderstandContent()
}

// UnderstandOnQuiz picks the tab: the one asked for, else the walkthrough, unless
// there is none and the open file has a quiz.
func (s PrereviewState) UnderstandOnQuiz() bool {
	switch s.UnderstandTab {
	case "quiz":
		return true
	case "walkthrough":
		return false
	}
	return !s.HasExplanation() && s.HasQuiz()
}

// WalkStep is one step as the page renders it.
type WalkStep struct {
	Num         int
	ID          string
	Title, Body string
	Anchors     []WalkAnchor
}

// WalkAnchor is a cited place, ready to render as a jump link (or a warning).
type WalkAnchor struct {
	File, Side string
	Line       int // 0 for a whole-file anchor
	Label      string
	Ungrounded bool
}

// WalkSteps is the current walkthrough's steps, numbered, with anchor labels.
// livetemplate computes every zero-arg method on each render, shown or not, so
// it does nothing unless the Walkthrough tab is on screen.
func (s PrereviewState) WalkSteps() []WalkStep {
	e := s.CurrentExplanation()
	if e == nil || !s.ShowUnderstand || s.UnderstandOnQuiz() {
		return nil
	}
	out := make([]WalkStep, len(e.Steps))
	for i, st := range e.Steps {
		ws := WalkStep{Num: i + 1, ID: st.ID, Title: st.Title, Body: st.Body}
		for _, a := range st.Anchors {
			ws.Anchors = append(ws.Anchors, walkAnchor(a))
		}
		out[i] = ws
	}
	return out
}

func walkAnchor(a Anchor) WalkAnchor {
	w := WalkAnchor{File: a.File, Side: a.Side, Label: a.Label(), Ungrounded: a.Ungrounded()}
	if a.Kind == AnchorKindLine {
		w.Line = a.FromLine
	}
	return w
}

// walkSteps is what a walk visits: the walkthrough's steps, in order. It is the
// one list the step bar, the stops, the markers and the drawer order read, so a
// derived step (A4's "Not explained") joins every one of them by joining it.
func (s PrereviewState) walkSteps() []Step {
	e := s.CurrentExplanation()
	if e == nil {
		return nil
	}
	return e.Steps
}

// walkStop is one stop of a walk: a step, and which of its anchors to show (-1
// when none of them resolves, so the stop has nowhere to jump).
type walkStop struct{ step, anchor int }

// walkStopsOf flattens steps into the stops ‹ › visits: every grounded anchor of
// every step, in order; a step with no grounded anchor is still one stop, so its
// explanation (and its ⚠) is read, not skipped.
func walkStopsOf(steps []Step) []walkStop {
	var out []walkStop
	for i, st := range steps {
		n := len(out)
		for j, a := range st.Anchors {
			if !a.Ungrounded() {
				out = append(out, walkStop{i, j})
			}
		}
		if len(out) == n {
			out = append(out, walkStop{i, -1})
		}
	}
	return out
}

// walkPos is the walk in progress: its steps, its stops, and the index of the
// stop being shown. The cursor is stored as the stop itself (WalkStep,
// WalkAnchor), not an index, so stops shifting under it (an anchor that stopped
// resolving after an edit) can't silently move it; a vanished stop falls back to
// its step's first. ok is false when no walk is on for the current walkthrough.
func (s PrereviewState) walkPos() (steps []Step, stops []walkStop, i int, ok bool) {
	e := s.CurrentExplanation()
	if e == nil || s.WalkExplanation == "" || e.ID != s.WalkExplanation {
		return nil, nil, 0, false
	}
	steps = s.walkSteps()
	stops = walkStopsOf(steps)
	i = slices.Index(stops, walkStop{s.WalkStep, s.WalkAnchor})
	if i < 0 {
		i = slices.IndexFunc(stops, func(w walkStop) bool { return w.step == s.WalkStep })
	}
	return steps, stops, i, i >= 0
}

// WalkBarView is the step bar above the file while walking.
type WalkBarView struct {
	Num, Total  int // the step, of how many
	Title, Body string
	Anchor      string // the cited place this stop shows, "" when none resolves
	HasPrev     bool
	HasNext     bool
}

// WalkBar is the step bar, or nil when no walk is on or a page covers the file.
func (s PrereviewState) WalkBar() *WalkBarView {
	steps, stops, i, ok := s.walkPos()
	if !ok || s.fileCovered() {
		return nil
	}
	at, st := stops[i], steps[stops[i].step]
	v := &WalkBarView{Num: at.step + 1, Total: len(steps), Title: st.Title, Body: st.Body,
		HasPrev: i > 0, HasNext: i < len(stops)-1}
	if at.anchor >= 0 {
		v.Anchor = st.Anchors[at.anchor].Label()
	}
	return v
}

// StepMarkLines labels the open file's rows that a step starts at, keyed like
// the row ("L<old>-<new>"): "Step 2: Retry loop", several joined by " · ". The
// marker hangs on the first line of each grounded line anchor.
func (s PrereviewState) StepMarkLines() map[string]string {
	steps := s.walkSteps()
	if len(steps) == 0 || s.CurrentDiff == nil {
		return nil
	}
	byRow := map[string][]int{}
	for i, st := range steps {
		for _, a := range st.Anchors {
			if a.File != s.SelectedFile || a.Kind != AnchorKindLine || a.Ungrounded() {
				continue
			}
			l, ok := findDiffLine(s.CurrentDiff, a.Side, a.FromLine)
			if !ok {
				continue
			}
			k := lineCursorKey(l)
			if !slices.Contains(byRow[k], i) {
				byRow[k] = append(byRow[k], i)
			}
		}
	}
	out := make(map[string]string, len(byRow))
	for k, idx := range byRow {
		labels := make([]string, len(idx))
		for n, i := range idx {
			labels[n] = fmt.Sprintf("Step %d: %s", i+1, steps[i].Title)
		}
		out[k] = strings.Join(labels, " · ")
	}
	return out
}

// walkRanks is each file's place in the walkthrough: the index of the first step
// that cites it (grounded or not, as the agent ordered it).
func (s PrereviewState) walkRanks() map[string]int {
	steps := s.walkSteps()
	if len(steps) == 0 {
		return nil
	}
	out := map[string]int{}
	for i, st := range steps {
		for _, a := range st.Anchors {
			if _, seen := out[a.File]; !seen {
				out[a.File] = i
			}
		}
	}
	return out
}
