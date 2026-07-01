package gitdiff

import (
	"html/template"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ColRange is a half-open [From, To) rune range within a single rendered line,
// in the same raw-line coordinate space the client computes text-comment
// offsets in (guaranteed equal to the line's .content textContent by
// TestHighlightLines_TextContentEqualsRaw). Used to wrap the commented span in
// a <mark> when rendering the line.
type ColRange struct {
	From int
	To   int
}

// markSpanClass is the class on every injected <mark>; styled per scheme in
// prereview.css so a text comment's span is visibly highlighted in the diff.
const markSpanClass = "comment-span"

// MarkRanges wraps each [From, To) rune range of a line's rendered HTML in a
// <mark class="comment-span">, splitting across chroma token spans as needed so
// a range crossing several tokens still highlights contiguously. The input is
// the per-line highlighted fragment (chroma token spans) — or any HTML whose
// textContent equals the raw line; ranges index that textContent by rune.
//
// It fails OPEN: on a parse error or empty/invalid ranges it returns the
// fragment unchanged (an un-highlighted span beats a dropped line).
func MarkRanges(fragment template.HTML, ranges []ColRange) template.HTML {
	rs := normalizeRanges(ranges)
	if len(rs) == 0 {
		return fragment
	}
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(string(fragment)), ctx)
	if err != nil {
		return fragment
	}
	root := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		root.AppendChild(n)
	}

	offset := 0 // running rune offset across all text nodes, in document order
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		c := n.FirstChild
		for c != nil {
			next := c.NextSibling
			switch c.Type {
			case html.TextNode:
				runeLen := len([]rune(c.Data))
				for _, repl := range splitTextNode(c.Data, offset, rs) {
					n.InsertBefore(repl, c)
				}
				n.RemoveChild(c)
				offset += runeLen
			case html.ElementNode:
				walk(c)
			}
			c = next
		}
	}
	walk(root)

	var b strings.Builder
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&b, c); err != nil {
			return fragment
		}
	}
	return template.HTML(b.String())
}

// splitTextNode breaks one text node (whose first rune sits at global offset
// `start`) into an alternating sequence of plain text nodes and
// <mark>-wrapped text nodes, per the marked ranges rs (sorted, non-overlapping).
func splitTextNode(data string, start int, rs []ColRange) []*html.Node {
	runes := []rune(data)
	var out []*html.Node
	i := 0
	for i < len(runes) {
		if r, ok := rangeAt(rs, start+i); ok {
			// Marked run: from i up to the range end or the node end.
			j := i
			for j < len(runes) && start+j < r.To {
				j++
			}
			mark := &html.Node{
				Type:     html.ElementNode,
				Data:     "mark",
				DataAtom: atom.Mark,
				Attr:     []html.Attribute{{Key: "class", Val: markSpanClass}},
			}
			mark.AppendChild(&html.Node{Type: html.TextNode, Data: string(runes[i:j])})
			out = append(out, mark)
			i = j
			continue
		}
		// Plain run: from i up to the next range start or the node end.
		j := i
		for j < len(runes) {
			if _, ok := rangeAt(rs, start+j); ok {
				break
			}
			j++
		}
		out = append(out, &html.Node{Type: html.TextNode, Data: string(runes[i:j])})
		i = j
	}
	return out
}

// rangeAt returns the range containing absolute rune position pos, if any.
func rangeAt(rs []ColRange, pos int) (ColRange, bool) {
	for _, r := range rs {
		if pos >= r.From && pos < r.To {
			return r, true
		}
	}
	return ColRange{}, false
}

// normalizeRanges drops empty/invalid ranges, sorts by start, and merges
// overlapping or touching ones so the mark walk sees clean, disjoint intervals.
func normalizeRanges(ranges []ColRange) []ColRange {
	cleaned := make([]ColRange, 0, len(ranges))
	for _, r := range ranges {
		if r.From < 0 {
			r.From = 0
		}
		if r.To > r.From {
			cleaned = append(cleaned, r)
		}
	}
	if len(cleaned) <= 1 {
		return cleaned
	}
	sort.Slice(cleaned, func(i, j int) bool { return cleaned[i].From < cleaned[j].From })
	merged := cleaned[:1]
	for _, r := range cleaned[1:] {
		last := &merged[len(merged)-1]
		if r.From <= last.To {
			if r.To > last.To {
				last.To = r.To
			}
			continue
		}
		merged = append(merged, r)
	}
	return merged
}
