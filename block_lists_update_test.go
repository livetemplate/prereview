package main

import (
	"fmt"
	"html/template"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/review"
)

// TestBlockListsUpdateOneBlock: a new comment or suggestion on one rendered block
// updates that block only. Each block used to range over the whole file's list
// and filter it with {{if}}, so one new item changed the list in every block
// (15.8–21.9 KB per new item on a 107-block guide).
func TestBlockListsUpdateOneBlock(t *testing.T) {
	const blocks = 20
	markdown := repoBase()
	markdown.SelectedFile = "README.md"
	markdown.CurrentDiff = &gitdiff.FileDiff{Path: "README.md"}
	html := repoBase()
	html.SelectedFile = "index.html"
	html.CurrentDiff = &gitdiff.FileDiff{Path: "index.html", HTMLDoc: "<body></body>"}
	for i := 1; i <= blocks; i++ {
		line := gitdiff.DiffLine{Kind: "add", NewNum: i, Content: fmt.Sprintf("line %d", i)}
		markdown.CurrentDiff.Lines = append(markdown.CurrentDiff.Lines, line)
		markdown.CurrentDiff.MarkdownBlocks = append(markdown.CurrentDiff.MarkdownBlocks,
			gitdiff.MarkdownBlock{HTML: template.HTML(fmt.Sprintf("<p>block %d</p>", i)), StartLine: i, EndLine: i})
		html.CurrentDiff.Lines = append(html.CurrentDiff.Lines, line)
		html.CurrentDiff.HTMLBlocks = append(html.CurrentDiff.HTMLBlocks, gitdiff.HTMLBlock{StartLine: i, EndLine: i})
	}

	for _, view := range []struct {
		name, keyPrefix string
		state           review.PrereviewState
	}{
		{"markdown", "MB-", markdown},
		{"html", "HB-", html},
	} {
		file := view.state.SelectedFile
		for _, change := range []struct {
			name string
			add  func(s *review.PrereviewState)
		}{
			{"comment", func(s *review.PrereviewState) {
				s.Comments = []review.Comment{{ID: "c1", File: file, Body: "tighten", Kind: "line", FromLine: 10, ToLine: 10, Side: "new"}}
			}},
			{"suggestion", func(s *review.PrereviewState) {
				s.Suggestions = []review.Suggestion{{ID: "s1", File: file, FromLine: 10, ToLine: 10, Side: "new", OriginalText: "line 10", ProposedText: "line ten"}}
			}},
		} {
			t.Run(view.name+"/"+change.name, func(t *testing.T) {
				after := view.state
				change.add(&after)
				payload := renderUpdate(t, view.state, after)
				if n := strings.Count(payload, `["u","`+view.keyPrefix); n != 1 {
					t.Errorf("updated %d blocks, want 1 (%s10-10): %d bytes\n%.600s", n, view.keyPrefix, len(payload), payload)
				}
				if !strings.Contains(payload, `["u","`+view.keyPrefix+`10-10"`) {
					t.Errorf("block %s10-10 is not the one updated:\n%.600s", view.keyPrefix, payload)
				}
			})
		}
	}
}
