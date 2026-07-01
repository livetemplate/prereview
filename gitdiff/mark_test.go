package gitdiff

import (
	"html/template"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// markedText returns the concatenation of text inside every <mark> element of a
// fragment — i.e. exactly the substring MarkRanges highlighted.
func markedText(t *testing.T, frag template.HTML) string {
	t.Helper()
	nodes, err := html.ParseFragment(strings.NewReader(string(frag)), &html.Node{
		Type: html.ElementNode, Data: "div", DataAtom: atom.Div,
	})
	if err != nil {
		t.Fatalf("parse %q: %v", frag, err)
	}
	var b strings.Builder
	var walk func(n *html.Node, inMark bool)
	walk = func(n *html.Node, inMark bool) {
		if n.Type == html.ElementNode && n.Data == "mark" {
			inMark = true
		}
		if inMark && n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inMark)
		}
	}
	for _, n := range nodes {
		walk(n, false)
	}
	return b.String()
}

// The mark highlight must never change what the line reads as — only wrap part
// of it — so textContent(after) must still equal textContent(before). This is
// the same invariant offsets depend on.
func assertTextContentUnchanged(t *testing.T, before, after template.HTML) {
	t.Helper()
	if b, a := textContent(t, before), textContent(t, after); a != b {
		t.Errorf("MarkRanges changed textContent:\n before=%q\n after =%q", b, a)
	}
}

func TestMarkRanges_Highlighted(t *testing.T) {
	// A realistic chroma line for `func Greet(name string) string {`.
	line := HighlightLines("main.go", []string{"func Greet(name string) string {"})[0]

	t.Run("single token word", func(t *testing.T) {
		// "func " = 5 runes → "Greet" = [5,10).
		got := MarkRanges(line, []ColRange{{From: 5, To: 10}})
		if mt := markedText(t, got); mt != "Greet" {
			t.Errorf("marked text = %q, want %q", mt, "Greet")
		}
		if !strings.Contains(string(got), `<mark class="comment-span">`) {
			t.Errorf("no comment-span mark emitted: %s", got)
		}
		assertTextContentUnchanged(t, line, got)
	})

	t.Run("cross-token range", func(t *testing.T) {
		// [5,15) = "Greet(name" spans several chroma tokens (nf, p, nx).
		got := MarkRanges(line, []ColRange{{From: 5, To: 15}})
		if mt := markedText(t, got); mt != "Greet(name" {
			t.Errorf("marked text = %q, want %q", mt, "Greet(name")
		}
		assertTextContentUnchanged(t, line, got)
	})

	t.Run("range at line start", func(t *testing.T) {
		got := MarkRanges(line, []ColRange{{From: 0, To: 4}})
		if mt := markedText(t, got); mt != "func" {
			t.Errorf("marked text = %q, want %q", mt, "func")
		}
		assertTextContentUnchanged(t, line, got)
	})

	t.Run("multiple disjoint ranges", func(t *testing.T) {
		got := MarkRanges(line, []ColRange{{From: 0, To: 4}, {From: 5, To: 10}})
		if mt := markedText(t, got); mt != "funcGreet" {
			t.Errorf("marked text = %q, want %q", mt, "funcGreet")
		}
		assertTextContentUnchanged(t, line, got)
	})
}

func TestMarkRanges_Unicode(t *testing.T) {
	// Emoji before the target must count as one rune, so [4,6) = "cd".
	line := HighlightLines("main.go", []string{"☕ abcd"})[0]
	got := MarkRanges(line, []ColRange{{From: 4, To: 6}})
	if mt := markedText(t, got); mt != "cd" {
		t.Errorf("marked text = %q, want %q (rune offsets, not UTF-16)", mt, "cd")
	}
	assertTextContentUnchanged(t, line, got)
}

func TestMarkRanges_PlainFallback(t *testing.T) {
	// Long-line fallback path renders as a single escaped text node (no spans);
	// marking must still split it correctly and keep the metacharacters intact.
	raw := "if a < b && c > d {"
	frag := template.HTML(template.HTMLEscapeString(raw)) // "if a &lt; b &amp;&amp; c &gt; d {"
	got := MarkRanges(frag, []ColRange{{From: 3, To: 8}}) // "a < b"
	if mt := markedText(t, got); mt != "a < b" {
		t.Errorf("marked text = %q, want %q", mt, "a < b")
	}
	if tc := textContent(t, got); tc != raw {
		t.Errorf("textContent = %q, want %q", tc, raw)
	}
}

func TestMarkRanges_NoOpCases(t *testing.T) {
	line := HighlightLines("main.go", []string{"return x"})[0]
	for _, tc := range []struct {
		name   string
		ranges []ColRange
	}{
		{"nil", nil},
		{"empty slice", []ColRange{}},
		{"zero-width", []ColRange{{From: 3, To: 3}}},
		{"inverted", []ColRange{{From: 8, To: 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarkRanges(line, tc.ranges); got != line {
				t.Errorf("expected unchanged fragment, got %q", got)
			}
		})
	}
}

func TestNormalizeRanges_MergesAndSorts(t *testing.T) {
	in := []ColRange{{5, 10}, {0, 4}, {8, 12}, {3, 3}, {-2, 2}}
	got := normalizeRanges(in)
	// -2→0 clamps to {0,2}; {0,4} overlaps → {0,4}; {5,10}+{8,12} → {5,12};
	// {3,3} dropped. Result: {0,4},{5,12}.
	want := []ColRange{{0, 4}, {5, 12}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range %d = %v, want %v", i, got[i], want[i])
		}
	}
}
