package gitdiff

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// MarkdownBlock is one top-level rendered block (heading, paragraph,
// list, code fence, …) plus the 1-based, inclusive SOURCE line range
// it came from. The line range is what makes rendered-Markdown
// commenting line-accurate: a comment placed on a block anchors to
// these real source lines, so it round-trips with the raw line view
// and the CSV — nothing is renumbered.
type MarkdownBlock struct {
	HTML      template.HTML
	StartLine int
	EndLine   int
}

// mdRenderer uses goldmark's safe defaults: raw HTML in the source is
// NOT passed through (no html.WithUnsafe), so untrusted repo content
// can't inject <script>/onerror/etc. No separate sanitizer needed —
// same choice the sibling modules (tinkerdown, devbox-dash) make.
var mdRenderer = goldmark.New()

// RenderMarkdownBlocks parses src and returns each top-level block as
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

	doc := mdRenderer.Parser().Parse(text.NewReader(src))
	var out []MarkdownBlock
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		start, stop := segmentSpan(n)
		var buf bytes.Buffer
		if err := mdRenderer.Renderer().Render(&buf, src, n); err != nil {
			continue
		}
		h := bytes.TrimSpace(buf.Bytes())
		if len(h) == 0 {
			continue
		}
		out = append(out, MarkdownBlock{
			HTML:      template.HTML(h), //nolint:gosec // goldmark safe-mode output; raw HTML not passed through
			StartLine: lineAt(start),
			EndLine:   lineAt(max(stop-1, start)),
		})
	}
	return out
}

// segmentSpan returns the [minStart, maxStop) source byte range
// covered by node and all its descendants. Container blocks (List,
// Blockquote) carry no line segments of their own, so we union every
// descendant that does.
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
