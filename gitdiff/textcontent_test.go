package gitdiff

import (
	"html/template"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// textContent extracts the rendered text of an HTML fragment exactly as a
// browser's Node.textContent would: the concatenation of every descendant text
// node, with HTML entities decoded. For the inline-span markup chroma emits into
// `.content`, this equals what document.querySelector('.content').textContent
// returns in the browser.
func textContent(t *testing.T, frag template.HTML) string {
	t.Helper()
	nodes, err := html.ParseFragment(strings.NewReader(string(frag)), &html.Node{
		Type:     html.ElementNode,
		Data:     "div",
		DataAtom: atom.Div,
	})
	if err != nil {
		t.Fatalf("parse fragment %q: %v", frag, err)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return b.String()
}

// TestHighlightLines_TextContentEqualsRaw is the load-bearing invariant for
// character-range ("text"-kind) comments: the client computes selection offsets
// from the rendered `.content` DOM text, but the server stores and re-highlights
// them in *raw line* coordinates. Those two coordinate spaces MUST coincide —
// i.e. textContent(HighlightLines(...)[i]) must equal the raw input line
// byte-for-byte (rune-for-rune). If a chroma upgrade, an entity-escaping change,
// or the long-line fallback ever breaks this, every text-comment offset silently
// shifts. This test is the guard.
func TestHighlightLines_TextContentEqualsRaw(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		line     string
	}{
		{"plain go", "main.go", "func Foo(a int) int { return a + 1 }"},
		{"leading tab indent", "main.go", "\tif x > 0 {"},
		{"html metachars", "main.go", "if a < b && c > d || e <= f {"},
		{"ampersand and quotes", "main.go", `s := "a & b" + ` + "`c < d`"},
		{"unicode", "main.go", "name := \"café\" // ☕ → done"},
		{"trailing whitespace", "main.go", "x := 1   "},
		{"go template tmpl lexer", "page.tmpl", `<div class="{{if .X}}on{{end}}">{{.Name}}</div>`},
		{"only whitespace", "main.go", "    \t  "},
		{"single rune", "main.go", "}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := HighlightLines(tc.filename, []string{tc.line})
			if len(out) != 1 {
				t.Fatalf("HighlightLines returned %d lines, want 1", len(out))
			}
			got := textContent(t, out[0])
			if got != tc.line {
				t.Errorf("textContent mismatch:\n raw=%q\n got=%q", tc.line, got)
			}
		})
	}
}

// TestHighlightLines_TextContentEqualsRaw_LongLineFallback covers the
// maxHighlightLineChars path, where the line skips chroma and is rendered as a
// single EscapeString'd string. textContent must still decode back to the raw
// line so offsets stay valid on minified / one-liner content too.
func TestHighlightLines_TextContentEqualsRaw_LongLineFallback(t *testing.T) {
	long := strings.Repeat("aB<&>\t z", maxHighlightLineChars/8+50) // > maxHighlightLineChars
	if len(long) <= maxHighlightLineChars {
		t.Fatalf("test setup: long line is only %d chars, need > %d", len(long), maxHighlightLineChars)
	}
	out := HighlightLines("main.go", []string{long})
	if got := textContent(t, out[0]); got != long {
		t.Errorf("long-line textContent mismatch:\n raw len=%d\n got len=%d", len(long), len(got))
	}
}

// TestHighlightLines_TextContentEqualsRaw_MultiLine verifies the per-line split
// preserves each line's content independently (the offset model anchors FromCol
// to FromLine and ToCol to ToLine, so each line must round-trip on its own).
func TestHighlightLines_TextContentEqualsRaw_MultiLine(t *testing.T) {
	lines := []string{
		"func Example() {",
		"\ts := \"a < b\"",
		"",
		"\treturn // ☕",
		"}",
	}
	out := HighlightLines("main.go", lines)
	if len(out) != len(lines) {
		t.Fatalf("got %d output lines, want %d", len(out), len(lines))
	}
	for i, raw := range lines {
		if got := textContent(t, out[i]); got != raw {
			t.Errorf("line %d textContent mismatch:\n raw=%q\n got=%q", i, raw, got)
		}
	}
}
