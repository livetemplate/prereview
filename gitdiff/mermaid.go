package gitdiff

import (
	"html"
	"strings"

	"github.com/yuin/goldmark/ast"
)

// Mermaid diagrams are authored as a ```mermaid fenced code block. Unlike
// every other Markdown construct prereview renders, a mermaid diagram can't
// be turned into final HTML in Go: it needs the mermaid.js engine to lay the
// graph out into SVG in the browser. So this is the single client-rendered
// element in an otherwise fully server-rendered pipeline (chroma, emoji,
// alerts all finish in Go).
//
// The contract that keeps it commentable: the fence stays ONE MarkdownBlock
// anchored to its source-line span (RenderMarkdownBlocks routes the fence
// here instead of to chroma), so a review comment anchors to the real source
// lines whether the block paints as diagram text or rendered SVG.

// isMermaidFence reports whether a fenced code block's info string names the
// mermaid language. The info string's first word is the language (GitHub and
// goldmark both treat ```mermaid and e.g. ```mermaid foo identically — extra
// words are ignored), and the match is case-insensitive to mirror how GitHub
// recognises the fence.
func isMermaidFence(fcb *ast.FencedCodeBlock, src []byte) bool {
	lang := strings.TrimSpace(string(fcb.Language(src)))
	if i := strings.IndexAny(lang, " \t"); i >= 0 {
		lang = lang[:i]
	}
	return strings.EqualFold(lang, "mermaid")
}

// fencedCodeRaw concatenates a fenced code block's raw source lines (the
// diagram definition), exactly as authored — goldmark's per-fence Lines()
// segments exclude the ``` delimiters and any info string.
func fencedCodeRaw(fcb *ast.FencedCodeBlock, src []byte) []byte {
	ls := fcb.Lines()
	if ls == nil {
		return nil
	}
	var buf []byte
	for i := 0; i < ls.Len(); i++ {
		seg := ls.At(i)
		buf = append(buf, src[seg.Start:seg.Stop]...)
	}
	return buf
}

// renderMermaidBlock wraps a diagram definition in the container the client
// renderer turns into SVG. Three load-bearing pieces:
//
//   - lvt-ignore: livetemplate's morphdom skips this element and its whole
//     subtree, so a later server DOM patch (adding a comment elsewhere, a
//     scroll directive, …) never clobbers the SVG mermaid injects here.
//   - class="mermaid" on the <pre>: the selector mermaid.run / our init
//     script scan for; the diagram definition is its textContent.
//   - html.EscapeString: untrusted repo content can't inject markup (matching
//     the pipeline's safe default). The browser decodes the entities back
//     when it computes textContent, so the mermaid parser still sees the
//     original definition (e.g. `A--&gt;B` → `A-->B`).
//
// The trailing newline goldmark leaves on the last fence line is trimmed so
// the definition mermaid parses has no spurious blank final line.
func renderMermaidBlock(raw []byte) string {
	def := strings.TrimRight(string(raw), "\n")
	if def == "" {
		return ""
	}
	return `<div class="md-mermaid" lvt-ignore><pre class="mermaid">` +
		html.EscapeString(def) + `</pre></div>`
}
