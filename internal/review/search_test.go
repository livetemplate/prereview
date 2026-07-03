package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

// searchFixture writes files into a no-git temp repo and returns a controller +
// state whose Files list them all as changed ("A"), so computeSearch can scan
// real on-disk content via loadDiffCached (no-git → reads the file).
func searchFixture(t *testing.T, files [][2]string) (*PrereviewController, PrereviewState) {
	t.Helper()
	dir := t.TempDir()
	var entries []gitdiff.FileEntry
	for _, f := range files {
		name, content := f[0], f[1]
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, gitdiff.FileEntry{Path: name, Status: "A"})
	}
	return &PrereviewController{RepoPath: dir, NoGit: true}, PrereviewState{Files: entries}
}

func TestComputeSearch_ContentAndFilename(t *testing.T) {
	c, st := searchFixture(t, [][2]string{
		{"alpha.go", "package alpha\n\nfunc Widget() {}\n"},
		{"beta.go", "package beta\n// nothing to see\n"},
	})

	// Content match: "widget" (case-insensitive) → alpha.go line 3.
	st.SearchQuery = "widget"
	hits := c.computeSearch(st)
	var lineHit bool
	for _, h := range hits {
		if h.Kind == "line" && h.File == "alpha.go" && h.NewNum == 3 {
			lineHit = true
			if !strings.Contains(string(h.Line), `<mark class="search-match">`) {
				t.Errorf("content hit should be highlighted: %q", h.Line)
			}
		}
	}
	if !lineHit {
		t.Fatalf("want content hit alpha.go:3 for 'widget', got %+v", hits)
	}

	// Filename match: "beta" → a Kind:"file" hit for beta.go.
	st.SearchQuery = "beta"
	hits = c.computeSearch(st)
	var fileHit bool
	for _, h := range hits {
		if h.Kind == "file" && h.File == "beta.go" {
			fileHit = true
		}
	}
	if !fileHit {
		t.Fatalf("want filename hit beta.go for 'beta', got %+v", hits)
	}
}

func TestComputeSearch_MinLenAndScope(t *testing.T) {
	c, st := searchFixture(t, [][2]string{
		{"changed.go", "func Alpha() {}\n"},
		{"stable.go", "func Alpha() {}\n"},
	})
	st.Files[1].Status = "" // stable.go is unchanged

	// Below min length → no scan.
	st.SearchQuery = "a"
	if hits := c.computeSearch(st); hits != nil {
		t.Errorf("1-char query should return nil, got %+v", hits)
	}

	// Changed scope (default): only changed.go.
	st.SearchQuery = "alpha"
	for _, h := range c.computeSearch(st) {
		if h.File == "stable.go" {
			t.Errorf("changed scope must not include unchanged stable.go")
		}
	}

	// All scope: stable.go now included.
	st.SearchScopeAll = true
	var sawStable bool
	for _, h := range c.computeSearch(st) {
		if h.File == "stable.go" {
			sawStable = true
		}
	}
	if !sawStable {
		t.Error("all-files scope should include unchanged stable.go")
	}
}

func TestComputeSearch_CapsResults(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxSearchHits+50; i++ {
		b.WriteString("needle here\n")
	}
	c, st := searchFixture(t, [][2]string{{"big.go", b.String()}})
	st.SearchQuery = "needle"
	if n := len(c.computeSearch(st)); n != maxSearchHits {
		t.Errorf("results should cap at %d, got %d", maxSearchHits, n)
	}
}

func TestHighlightMatch(t *testing.T) {
	// Case-insensitive wrap + HTML escaping of the surrounding text.
	got := string(highlightMatch(`if x<y && Foo() {`, "foo"))
	if !strings.Contains(got, `<mark class="search-match">Foo</mark>`) {
		t.Errorf("should wrap the original-case match: %q", got)
	}
	if !strings.Contains(got, "x&lt;y") || strings.Contains(got, "x<y") {
		t.Errorf("surrounding text must be HTML-escaped: %q", got)
	}
	// No match → plain escaped line, no <mark>.
	plain := string(highlightMatch("a & b", "zzz"))
	if strings.Contains(plain, "<mark") || !strings.Contains(plain, "a &amp; b") {
		t.Errorf("no-match should be plain escaped: %q", plain)
	}
}

// TestRevealingForcesRawView pins the Markdown/HTML jump fix: a search-jump into
// a Markdown file (RevealFile set) must fall to the raw line view so the target
// line row exists — ShowRenderedMarkdown false, VisibleLines shows the file.
func TestRevealingForcesRawView(t *testing.T) {
	md := &gitdiff.FileDiff{
		Path:           "readme.md",
		MarkdownBlocks: []gitdiff.MarkdownBlock{{}},
		Lines: []gitdiff.DiffLine{
			{NewNum: 1, Content: "# Title"},
			{NewNum: 0, OldNum: 4, Content: "deleted line"},
			{NewNum: 2, Content: "body"},
		},
	}
	st := PrereviewState{CurrentDiff: md}
	if !st.ShowRenderedMarkdown() {
		t.Fatal("baseline: a Markdown file should render as blocks")
	}
	st.RevealFile = "readme.md"
	if !st.Revealing() {
		t.Fatal("Revealing() should be true when RevealFile == current path")
	}
	if st.ShowRenderedMarkdown() {
		t.Error("revealing must force the raw line view (ShowRenderedMarkdown false)")
	}
	// VisibleLines shows every working-tree line (NewNum>0), del line excluded.
	if vl := st.VisibleLines(); len(vl) != 2 {
		t.Errorf("revealing should show 2 working-tree lines, got %d", len(vl))
	}
	// A different RevealFile does not reveal the current file.
	st.RevealFile = "other.md"
	if st.Revealing() || !st.ShowRenderedMarkdown() {
		t.Error("RevealFile for a different path must not affect the current file")
	}
}
