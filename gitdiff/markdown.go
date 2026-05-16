package gitdiff

import (
	"bytes"
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
// Most top-level Markdown nodes (heading, paragraph, code fence,
// blockquote, …) are one block. Two container types are descended ONE
// level so review comments can target a single line of structured
// content: a list yields one block per list item, and a table yields
// one block for the header row plus one per body row. A multi-source-
// line prose paragraph stays a single block — reflowed prose has no
// per-source-line visual anchor.
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
	emit := func(node ast.Node, htmlStr string) {
		if htmlStr == "" {
			return
		}
		start, stop := segmentSpan(node)
		out = append(out, MarkdownBlock{
			HTML:      template.HTML(htmlStr), //nolint:gosec // goldmark safe-mode output; raw HTML not passed through
			StartLine: lineAt(start),
			EndLine:   lineAt(max(stop-1, start)),
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
