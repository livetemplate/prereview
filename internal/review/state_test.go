package review

import (
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

func TestIsHTML(t *testing.T) {
	cases := []struct {
		name string
		diff *gitdiff.FileDiff
		want bool
	}{
		{"no diff", nil, false},
		{".html", &gitdiff.FileDiff{Path: "index.html"}, true},
		{".HTM uppercase", &gitdiff.FileDiff{Path: "INDEX.HTM"}, true},
		{".md", &gitdiff.FileDiff{Path: "README.md"}, false},
		{".go", &gitdiff.FileDiff{Path: "main.go"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := PrereviewState{CurrentDiff: c.diff}
			if got := s.IsHTML(); got != c.want {
				t.Errorf("IsHTML() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestShowRenderedHTML(t *testing.T) {
	blocks := []gitdiff.HTMLBlock{{StartLine: 1, EndLine: 1}}
	cases := []struct {
		name    string
		diff    *gitdiff.FileDiff
		rawHTML bool
		want    bool
	}{
		{"html + blocks + not raw → preview", &gitdiff.FileDiff{Path: "index.html", HTMLBlocks: blocks}, false, true},
		{"html + blocks + raw → no preview", &gitdiff.FileDiff{Path: "index.html", HTMLBlocks: blocks}, true, false},
		{"html + no blocks → no preview (falls back to line view)", &gitdiff.FileDiff{Path: "index.html"}, false, false},
		{"markdown → no html preview", &gitdiff.FileDiff{Path: "README.md", HTMLBlocks: blocks}, false, false},
		{"nil diff → no preview", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := PrereviewState{CurrentDiff: c.diff, RawHTML: c.rawHTML}
			if got := s.ShowRenderedHTML(); got != c.want {
				t.Errorf("ShowRenderedHTML() = %v, want %v", got, c.want)
			}
		})
	}
}
