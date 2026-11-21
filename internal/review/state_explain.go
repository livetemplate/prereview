package review

import (
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
