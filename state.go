package main

import (
	"fmt"
	"time"

	"github.com/livetemplate/prereview/gitdiff"
)

// PrereviewState is the per-session state cloned by livetemplate. Fields
// tagged `lvt:"persist"` survive WebSocket reconnects (browser refresh, etc.)
// so the user doesn't lose their selected file or comment draft if the page
// reloads.
type PrereviewState struct {
	// Session identity — set once by main.go, never mutated.
	RepoPath  string `json:"repo_path"  lvt:"persist"`
	Base      string `json:"base"       lvt:"persist"`
	StartedAt string `json:"started_at" lvt:"persist"`
	CSVPath   string `json:"csv_path"   lvt:"persist"`

	// File navigation.
	Files        []gitdiff.FileEntry `json:"files"`
	SelectedFile string              `json:"selected_file" lvt:"persist"`
	CurrentDiff  *gitdiff.FileDiff   `json:"current_diff"`

	// Two-click selection (anchor → end). 0 = nothing selected; first
	// click sets both to the same line; second click moves end; third
	// click reseats anchor.
	SelectionAnchor int    `json:"selection_anchor" lvt:"persist"`
	SelectionEnd    int    `json:"selection_end"    lvt:"persist"`
	SelectionSide   string `json:"selection_side"   lvt:"persist"` // "new"|"old"|"both"

	// Comment composer.
	DraftBody string `json:"draft_body" lvt:"persist"`

	// Comments accumulated during this session.
	Comments []Comment `json:"comments"`

	// UI status.
	LastSaved   string `json:"last_saved"`
	DoneWritten bool   `json:"done_written" lvt:"persist"`
}

// Comment is one row in the CSV output (and one entry in state).
type Comment struct {
	ID       string    `json:"id"`
	File     string    `json:"file"`
	FromLine int       `json:"from_line"`
	ToLine   int       `json:"to_line"`
	Side     string    `json:"side"`
	Body     string    `json:"body"`
	Created  time.Time `json:"created"`
}

// LineSpan returns "L42" for single-line and "L42-L48" for ranges.
// Method on Comment so the template can call {{.LineSpan}} on each entry.
func (c Comment) LineSpan() string {
	if c.FromLine == c.ToLine {
		return fmt.Sprintf("L%d", c.FromLine)
	}
	return fmt.Sprintf("L%d-L%d", c.FromLine, c.ToLine)
}

// SelectionEmpty reports whether nothing is currently selected.
func (s PrereviewState) SelectionEmpty() bool { return s.SelectionAnchor == 0 }

// SelectedLines returns a set of line numbers currently selected. Zero-arg
// so the livetemplate framework eagerly pre-computes it once per render
// (the framework only pre-computes zero-arg methods, so a SelectionContains(n)
// helper would not be callable from the template). The template membership-tests
// with `{{index $.SelectedLines $n}}` — index returns the zero value (false)
// for missing keys, which is exactly what we want for unselected lines.
func (s PrereviewState) SelectedLines() map[int]bool {
	if s.SelectionAnchor == 0 {
		return nil
	}
	lo, hi := s.SelectionAnchor, s.SelectionEnd
	if lo > hi {
		lo, hi = hi, lo
	}
	out := make(map[int]bool, hi-lo+1)
	for n := lo; n <= hi; n++ {
		out[n] = true
	}
	return out
}

// SelectionLabel returns the human-readable form of the current selection
// (e.g. "L42" or "L42-L48"). Empty string when nothing is selected.
func (s PrereviewState) SelectionLabel() string {
	if s.SelectionAnchor == 0 {
		return ""
	}
	lo, hi := s.SelectionAnchor, s.SelectionEnd
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo == hi {
		return fmt.Sprintf("L%d", lo)
	}
	return fmt.Sprintf("L%d-L%d", lo, hi)
}
