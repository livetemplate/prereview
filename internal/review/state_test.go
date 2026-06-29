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

// TestScopeViewedCounts pins the "X/Y viewed" denominator to the review scope
// (changed files), not the full tracked-file list — so 1 changed file out of
// 144 reads "0/1", not "0/144" (reported in P7 signoff). When the scope is all
// (clean tree or ShowAllFiles) it counts every file.
func TestScopeViewedCounts(t *testing.T) {
	files := []gitdiff.FileEntry{
		{Path: "changed.go", Status: "M"},
		{Path: "added.go", Status: "A"},
		{Path: "unchanged1.go", Status: ""},
		{Path: "unchanged2.go", Status: ""},
	}
	// Default scope = changed only (2 files). One changed + one unchanged viewed:
	// the unchanged viewed file must NOT count toward the changed-scope progress.
	s := PrereviewState{
		Files:       files,
		ViewedFiles: map[string]bool{"changed.go": true, "unchanged1.go": true},
	}
	if got := s.ScopeFileCount(); got != 2 {
		t.Errorf("ScopeFileCount (changed scope) = %d, want 2", got)
	}
	if got := s.ScopeViewedCount(); got != 1 {
		t.Errorf("ScopeViewedCount (changed scope) = %d, want 1 (only changed.go counts)", got)
	}

	// ShowAllFiles → scope is every file (4); both viewed files count.
	s.ShowAllFiles = true
	if got := s.ScopeFileCount(); got != 4 {
		t.Errorf("ScopeFileCount (all scope) = %d, want 4", got)
	}
	if got := s.ScopeViewedCount(); got != 2 {
		t.Errorf("ScopeViewedCount (all scope) = %d, want 2", got)
	}

	// Clean tree (no changed files) falls back to all-files scope.
	clean := PrereviewState{
		Files:       []gitdiff.FileEntry{{Path: "a.go", Status: ""}, {Path: "b.go", Status: ""}},
		ViewedFiles: map[string]bool{"a.go": true},
	}
	if got := clean.ScopeFileCount(); got != 2 {
		t.Errorf("ScopeFileCount (clean tree) = %d, want 2 (fallback to all)", got)
	}
	if got := clean.ScopeViewedCount(); got != 1 {
		t.Errorf("ScopeViewedCount (clean tree) = %d, want 1", got)
	}
}
