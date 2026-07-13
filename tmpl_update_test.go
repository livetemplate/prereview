package main

import (
	"io"
	"strings"
	"testing"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/review"
)

// #167: a comment card must receive its LIVE update — the agent's thread reply
// and the "worked on" badge have to reach an already-rendered card, not just a
// fresh page load. The reviewer never reloads: `prereview reply` fans out over
// the WebSocket, and the card is patched in place.
//
// This exercises the same two livetemplate calls the server makes — Execute (the
// initial tree) then ExecuteUpdates (the fragment payload for the next state) —
// against the REAL templates, so it catches a card whose update is silently
// dropped without booting a browser. The e2e (e2e/e2e_filecomment_thread_test.go)
// covers the same ground through the real WebSocket; this one localizes it.
func TestTemplateUpdate_CommentThreadReachesCard(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string // "file" (commentCardSimple) or "line" (commentCardFull)
	}{
		{"file comment", "file"},
		{"line comment", "line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths, cleanup, err := stageTemplates(templatesFS)
			if err != nil {
				t.Fatalf("stage templates: %v", err)
			}
			defer cleanup()
			tmpl, err := livetemplate.New("prereview", livetemplate.WithParseFiles(paths...))
			if err != nil {
				t.Fatalf("livetemplate.New: %v", err)
			}

			before := stateWithComment(tc.kind)
			if err := tmpl.Execute(io.Discard, before); err != nil {
				t.Fatalf("initial Execute: %v", err)
			}

			// The agent replies and marks the comment worked-on — exactly what
			// `prereview reply` + `prereview done` produce in the live session.
			const note = "Renamed the greeting and updated its callers."
			after := stateWithComment(tc.kind)
			after.Comments[0].Processed = true
			after.ThreadEntries = []review.ThreadEntry{{
				TargetID: after.Comments[0].ID,
				Author:   "agent",
				Body:     note,
				At:       1700000000000000000,
			}}

			var buf strings.Builder
			if err := tmpl.ExecuteUpdates(&buf, after); err != nil {
				t.Fatalf("ExecuteUpdates: %v", err)
			}
			if !strings.Contains(buf.String(), note) {
				t.Errorf("the agent's reply never reaches the %s card: it is absent from the update payload, "+
					"so an open page keeps showing a card with no thread until a full reload.\npayload: %s",
					tc.kind, buf.String())
			}
		})
	}
}

// stateWithComment builds a rendered-file state holding one unresolved comment of
// the given kind — a file comment (rendered by commentCardSimple) or a line comment
// (commentCardFull) — on the selected file.
func stateWithComment(kind string) review.PrereviewState {
	c := review.Comment{
		ID:   "c1",
		File: "app.go",
		Body: "rename this greeting",
		Kind: kind,
	}
	if kind == "line" {
		c.FromLine, c.ToLine, c.Side = 1, 1, "new"
	}
	return review.PrereviewState{
		SelectedFile: "app.go",
		Comments:     []review.Comment{c},
		AgentMode:    true,
		CurrentDiff: &gitdiff.FileDiff{
			Path:   "app.go",
			Lines: []gitdiff.DiffLine{
				{Kind: "context", OldNum: 1, NewNum: 1, Content: "package main"},
			},
		},
	}
}
