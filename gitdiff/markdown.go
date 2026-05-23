package gitdiff

import (
	"bytes"
	"html"
	"html/template"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// MarkdownBlock is one independently-commentable rendered unit plus the
// 1-based, inclusive SOURCE line range it came from. The line range is
// what makes rendered-Markdown commenting line-accurate: a comment
// placed on a unit anchors to these real source lines, so it
// round-trips with the raw line view and the CSV — nothing is
// renumbered.
//
// Single-line nodes (heading, code fence, blockquote, …) are one
// block. Containers are descended ONE level so review comments can
// target a single line: a list yields one block per list item, a table
// one block for the header row plus one per body row. A paragraph that
// spans multiple SOURCE lines is split into one block per source line
// ONLY when it is authored one-sentence-per-line (every line except
// possibly the last ends a sentence) — CommonMark soft-wraps such a
// paragraph into one <p>, and splitting restores per-line commenting.
// A hard-wrapped paragraph (lines break mid-sentence) instead renders
// as a single reflowed CommonMark paragraph, so a sentence is never
// broken across visual lines; it stays commentable at paragraph
// granularity (its wrap points are arbitrary, so per-line comments
// there would be meaningless). An inline span that crosses a soft
// line-break degrades to literal text on that one line; it never
// produces invalid HTML.
type MarkdownBlock struct {
	HTML      template.HTML
	StartLine int
	EndLine   int
}

// mdRenderer enables the GFM extension (tables, strikethrough,
// autolinks, task-lists) so repo Markdown renders the way its authors
// wrote it for GitHub. GFM does NOT enable html.WithUnsafe, so the safe
// default still holds: raw HTML in the source is not passed through, so
// untrusted repo content can't inject <script>/onerror/etc. No separate
// sanitizer needed — same choice the sibling modules (tinkerdown,
// devbox-dash) make.
var mdRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// RenderMarkdownBlocks parses src and returns each commentable unit as
// safe HTML tagged with its source line span. Empty input → nil.
func RenderMarkdownBlocks(src []byte) []MarkdownBlock {
	if len(bytes.TrimSpace(src)) == 0 {
		return nil
	}
	lineAt := func(off int) int {
		if off < 0 {
			off = 0
		}
		if off > len(src) {
			off = len(src)
		}
		return bytes.Count(src[:off], []byte{'\n'}) + 1
	}

	renderNode := func(n ast.Node) string {
		var buf bytes.Buffer
		if err := mdRenderer.Renderer().Render(&buf, src, n); err != nil {
			return ""
		}
		return string(bytes.TrimSpace(buf.Bytes()))
	}

	doc := mdRenderer.Parser().Parse(text.NewReader(src))
	var out []MarkdownBlock
	// cursor tracks "one line past the last emitted block" so nodes whose
	// AST entry carries no source segments (goldmark's ThematicBreak being
	// the load-bearing example — it leaves an empty Lines() set) still get
	// a unique source line. Without this, segmentSpan returns (0, 0) and
	// lineAt(0) collapses every such node to line 1, so a 1200-line plan
	// with 20+ `---` separators ends up with 20+ blocks all reporting
	// [1, 1]. That violates the "every line belongs to one block" invariant
	// the template relies on and causes the composer + L1-anchored comments
	// to render once per collapsed block.
	cursor := 1
	emit := func(node ast.Node, htmlStr string) {
		if htmlStr == "" {
			return
		}
		start, stop := segmentSpan(node)
		var startLine, endLine int
		if start == 0 && stop == 0 {
			// No source segments — fall back to the cursor. The line will
			// be one past the previous block (usually the blank line that
			// precedes the node); comments on visual-only blocks like
			// thematic breaks are vanishingly rare, so the slight
			// imprecision is acceptable in exchange for a unique range.
			startLine, endLine = cursor, cursor
		} else {
			startLine = lineAt(start)
			endLine = lineAt(max(stop-1, start))
		}
		cursor = endLine + 1
		out = append(out, MarkdownBlock{
			HTML:      template.HTML(htmlStr), //nolint:gosec // goldmark safe-mode output; raw HTML not passed through
			StartLine: startLine,
			EndLine:   endLine,
		})
	}

	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		switch n.Kind() {
		case ast.KindList:
			lst := n.(*ast.List)
			ordinal := lst.Start
			if ordinal == 0 {
				ordinal = 1
			}
			for it := n.FirstChild(); it != nil; it = it.NextSibling() {
				if it.Kind() != ast.KindListItem {
					continue
				}
				emit(it, wrapListItem(lst, renderNode(it), ordinal))
				ordinal++
			}
		case east.KindTable:
			if rows, ok := tableRowBlocks(n, renderNode); ok {
				for _, rb := range rows {
					emit(rb.node, rb.html)
				}
			} else {
				// Row extraction failed → fall back to one whole-table
				// block (the pre-per-line behaviour) rather than ever
				// injecting unbalanced table HTML into the page.
				emit(n, renderNode(n))
			}
		case ast.KindParagraph:
			ls := n.Lines()
			if ls == nil || ls.Len() <= 1 {
				emit(n, renderNode(n)) // single source line — one block
				break
			}
			if !oneSentencePerLine(ls, src) {
				// Hard-wrapped prose: lines break mid-sentence, so
				// per-line splitting would visually break a sentence.
				// Render as one reflowed CommonMark paragraph instead;
				// still commentable at paragraph granularity.
				emit(n, renderNode(n))
				break
			}
			for i := 0; i < ls.Len(); i++ {
				seg := ls.At(i)
				if h := renderProseLine(src[seg.Start:seg.Stop]); h != "" {
					ln := lineAt(seg.Start)
					out = append(out, MarkdownBlock{
						HTML:      template.HTML(h), //nolint:gosec // single <p> of goldmark safe-mode output, else HTML-escaped text
						StartLine: ln,
						EndLine:   ln,
					})
					// Mirror emit()'s cursor update so a thematic-break
					// (or other empty-segmentSpan node) following a
					// split paragraph anchors to the correct line.
					cursor = ln + 1
				}
			}
		default:
			emit(n, renderNode(n))
		}
	}
	return out
}

// wrapListItem renders a single list item back inside its parent list
// tag so the bullet/number and CSS still apply standalone. Ordered
// lists keep their numbering via <ol start="N">.
func wrapListItem(lst *ast.List, itemHTML string, ordinal int) string {
	if itemHTML == "" {
		return ""
	}
	if lst.IsOrdered() {
		return `<ol start="` + strconv.Itoa(ordinal) + `">` + itemHTML + `</ol>`
	}
	return `<ul>` + itemHTML + `</ul>`
}

// oneSentencePerLine reports whether every line of a multi-line
// paragraph except the last ends a sentence. True ⇒ the author wrote
// one sentence per line (CommonMark soft-wrapped them into a single
// paragraph) so per-source-line splitting is safe and restores per-line
// commenting; false ⇒ the paragraph is hard-wrapped (a line breaks
// mid-sentence) and must render as one reflowed paragraph so a sentence
// is never split across visual lines. The last line is ignored: a
// paragraph can end anywhere, so its punctuation carries no signal.
func oneSentencePerLine(ls *text.Segments, src []byte) bool {
	for i := 0; i < ls.Len()-1; i++ {
		seg := ls.At(i)
		if !endsSentence(src[seg.Start:seg.Stop]) {
			return false
		}
	}
	return true
}

// endsSentence reports whether line, after trimming trailing whitespace
// and trailing inline-close markers (code backticks, emphasis `*_`,
// quotes and brackets `"')]}`), ends in sentence-terminal punctuation
// (. ! ?). `:` `;` `,` and em-dash are deliberately NOT terminal — a
// line ending in one of those is a hard-wrap continuation, not a
// sentence boundary.
func endsSentence(line []byte) bool {
	s := bytes.TrimRight(line, " \t\r\n")
	s = bytes.TrimRight(s, "`*_\"')]}")
	s = bytes.TrimRight(s, " \t")
	if len(s) == 0 {
		return false
	}
	switch s[len(s)-1] {
	case '.', '!', '?':
		return true
	default:
		return false
	}
}

// renderProseLine renders ONE source line of a paragraph as its own
// <p> block. Leading indentation is trimmed so a continuation line
// can't misfire as an indented code block. goldmark still applies
// block rules to a lone line, so a line that begins like a list/quote
// (`2.`, `3)`, `>`) would render as <ol>/<blockquote>; we accept the
// render only when it is a single <p>…</p>, otherwise we emit the
// HTML-escaped raw text in a <p> (correct + line-anchored; inline
// markup is lost only for that rare pathological line). Empty → "".
func renderProseLine(raw []byte) string {
	trimmed := bytes.TrimLeft(raw, " \t")
	if len(bytes.TrimSpace(trimmed)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := mdRenderer.Convert(trimmed, &buf); err == nil {
		h := string(bytes.TrimSpace(buf.Bytes()))
		if strings.HasPrefix(h, "<p>") && strings.HasSuffix(h, "</p>") {
			return h
		}
	}
	return "<p>" + html.EscapeString(string(bytes.TrimSpace(raw))) + "</p>"
}

type rowBlock struct {
	node ast.Node
	html string
}

// tableRowBlocks turns a GFM table into one block per row (header row +
// each body row). goldmark only emits <table>/<tbody> at the Table-node
// level, so a child row rendered standalone leaks an unbalanced
// <tbody>/</tbody>; we therefore extract just the single deterministic
// <tr>…</tr> element and re-wrap it in a clean minimal table. ok=false
// signals the caller to fall back to one whole-table block.
func tableRowBlocks(table ast.Node, renderNode func(ast.Node) string) ([]rowBlock, bool) {
	var rbs []rowBlock
	for r := table.FirstChild(); r != nil; r = r.NextSibling() {
		tr := extractTR(renderNode(r))
		if tr == "" {
			return nil, false
		}
		var wrapped string
		switch r.Kind() {
		case east.KindTableHeader:
			wrapped = `<table class="md-solo-table"><thead>` + tr + `</thead></table>`
		case east.KindTableRow:
			wrapped = `<table class="md-solo-table"><tbody>` + tr + `</tbody></table>`
		default:
			return nil, false
		}
		rbs = append(rbs, rowBlock{node: r, html: wrapped})
	}
	if len(rbs) == 0 {
		return nil, false
	}
	return rbs, true
}

// extractTR slices the single <tr>…</tr> element out of goldmark's
// per-row HTML, discarding any unbalanced <thead>/<tbody> wrappers
// goldmark leaks when a row is rendered outside its table. goldmark
// escapes cell text (`<` → `&lt;`), so the only literal <tr>/</tr> are
// the structural row tags — making the first-<tr / last-</tr> slice
// deterministic for the pinned goldmark version. "" → caller falls
// back to a whole-table block.
func extractTR(h string) string {
	i := strings.Index(h, "<tr")
	if i < 0 {
		return ""
	}
	const close = "</tr>"
	j := strings.LastIndex(h, close)
	if j < i {
		return ""
	}
	return h[i : j+len(close)]
}

// segmentSpan returns the [minStart, maxStop) source byte range
// covered by node and all its descendants. Container blocks (List,
// Blockquote, Table) carry no line segments of their own, so we union
// every descendant that does (a table row's lines live on its cells).
func segmentSpan(node ast.Node) (int, int) {
	minStart, maxStop := -1, -1
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		// Lines() panics on inline nodes (BaseInline) in goldmark — it
		// only exists for block nodes. Source segments live on blocks
		// anyway (Paragraph/TextBlock/Heading/CodeBlock/…).
		if n.Type() != ast.TypeBlock {
			return ast.WalkContinue, nil
		}
		if ls := n.Lines(); ls != nil {
			for i := 0; i < ls.Len(); i++ {
				s := ls.At(i)
				if minStart < 0 || s.Start < minStart {
					minStart = s.Start
				}
				if s.Stop > maxStop {
					maxStop = s.Stop
				}
			}
		}
		return ast.WalkContinue, nil
	})
	if minStart < 0 {
		return 0, 0
	}
	return minStart, maxStop
}
