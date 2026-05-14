package main

import (
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
