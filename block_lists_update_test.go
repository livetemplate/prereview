package main

import (
	"fmt"
	"html/template"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/review"
)

// TestBlockListsUpdateOneBlock: a new comment or suggestion on one rendered
// Markdown block updates that block only. Each block used to range over the
// whole file's list and filter it with {{if}}, so one new item changed the list
// in every block (15.8–21.9 KB per new item on a 107-block guide). The HTML
// preview has no per-block lists: its comments open in one card (PreviewCard).
func TestBlockListsUpdateOneBlock(t *testing.T) {
	const blocks = 20
	markdown := repoBase()
	markdown.SelectedFile = "README.md"
	markdown.CurrentDiff = &gitdiff.FileDiff{Path: "README.md"}
	for i := 1; i <= blocks; i++ {
		line := gitdiff.DiffLine{Kind: "add", NewNum: i, Content: fmt.Sprintf("line %d", i)}
		markdown.CurrentDiff.Lines = append(markdown.CurrentDiff.Lines, line)
		markdown.CurrentDiff.MarkdownBlocks = append(markdown.CurrentDiff.MarkdownBlocks,
			gitdiff.MarkdownBlock{HTML: template.HTML(fmt.Sprintf("<p>block %d</p>", i)), StartLine: i, EndLine: i})
	}

	for _, change := range []struct {
		name string
		add  func(s *review.PrereviewState)
	}{
		{"comment", func(s *review.PrereviewState) {
			s.Comments = []review.Comment{{ID: "c1", File: "README.md", Body: "tighten", Kind: "line", FromLine: 10, ToLine: 10, Side: "new"}}
		}},
		{"suggestion", func(s *review.PrereviewState) {
			s.Suggestions = []review.Suggestion{{ID: "s1", File: "README.md", FromLine: 10, ToLine: 10, Side: "new", OriginalText: "line 10", ProposedText: "line ten"}}
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			after := markdown
			change.add(&after)
			payload := renderUpdate(t, markdown, after)
			if n := strings.Count(payload, `["u","MB-`); n != 1 {
				t.Errorf("updated %d blocks, want 1 (MB-10-10): %d bytes\n%.600s", n, len(payload), payload)
			}
			if !strings.Contains(payload, `["u","MB-10-10"`) {
				t.Errorf("block MB-10-10 is not the one updated:\n%.600s", payload)
			}
		})
	}
}
