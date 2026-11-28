package gitdiff

import (
	"strings"
	"testing"
)

// TestFrontMatterRendersAsRows: #294. A page's YAML front matter is not
// parsed as Markdown (goldmark made the opening `---` a rule and the keys a
// setext <h2>). It renders as one key/value row per key, on that key's lines,
// and the body keeps its source lines.
func TestFrontMatterRendersAsRows(t *testing.T) {
	src := "---\ntitle: \"Getting started\"\ndescription: Install and run.\ntags:\n  - go\n  - web\nlayout: landing\n---\n\n# Hello\n\nBody text.\n"
	blocks := RenderMarkdownBlocks([]byte(src), "")
	type want struct {
		start, end int
		has        []string
	}
	wants := []want{
		{1, 1, []string{"Front matter"}},
		{2, 2, []string{">title<", ">Getting started<"}},
		{3, 3, []string{">description<", "Install and run."}},
		{4, 6, []string{">tags<", "- go\n- web"}},
		{7, 7, []string{">layout<", ">landing<"}},
		{10, 10, []string{"<h1", "Hello"}},
		{12, 12, []string{"Body text."}},
	}
	if len(blocks) != len(wants) {
		var got []string
		for _, b := range blocks {
			got = append(got, string(b.HTML))
		}
		t.Fatalf("got %d blocks, want %d:\n%s", len(blocks), len(wants), strings.Join(got, "\n"))
	}
	for i, w := range wants {
		b := blocks[i]
		if b.StartLine != w.start || b.EndLine != w.end {
			t.Errorf("block %d spans L%d-L%d, want L%d-L%d", i, b.StartLine, b.EndLine, w.start, w.end)
		}
		for _, s := range w.has {
			if !strings.Contains(string(b.HTML), s) {
				t.Errorf("block %d = %s, want it to contain %q", i, b.HTML, s)
			}
		}
	}
	for _, b := range blocks {
		if strings.Contains(string(b.HTML), "<h2") || strings.Contains(string(b.HTML), "<hr") {
			t.Errorf("front matter leaked into Markdown: %s", b.HTML)
		}
	}
	if hs := ExtractHeadings([]byte(src)); len(hs) != 1 || hs[0].Text != "Hello" || hs[0].Line != 10 {
		t.Errorf("headings = %+v, want only Hello on L10", hs)
	}
}

// TestFrontMatterNeedsBothDelimiters: front matter opens on line 1, closes,
// and holds only YAML keys; anything else is plain Markdown, such as prose
// between two rules.
func TestFrontMatterNeedsBothDelimiters(t *testing.T) {
	for _, src := range []string{
		"---\n\nJust a rule above.\n",
		"Intro\n\n---\ntitle: x\n---\n",
		"---\nA paragraph between two rules.\n---\n",
		"---\n---\n",
	} {
		if _, ok := parseFrontMatter([]byte(src)); ok {
			t.Errorf("%q: parsed as front matter", src)
		}
	}
	if out := string(RenderMarkdownDoc([]byte("---\ntitle: x\n---\n\nBody\n"))); strings.Contains(out, "title") || !strings.Contains(out, "Body") {
		t.Errorf("RenderMarkdownDoc = %q, want the body without the front matter", out)
	}
}
