package gitdiff

import (
	"fmt"
	"hash/fnv"
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

// MermaidKeys renders the diagrams of one view. Each gets a data-key that is
// unique on the page: morphdom indexes keyed elements across the whole patch
// root, so two diagrams sharing a key leave a stale copy behind when one of
// them is edited. The key hashes the view's scope and the definition, plus
// how many identical definitions came before it in this view. So it changes
// when the definition does, which forces a re-insert past lvt-ignore, and it
// stays put when unrelated lines move.
type MermaidKeys struct {
	scope string
	seen  map[string]int
}

// NewMermaidKeys starts the diagrams of one view, such as one file's Markdown
// or the walkthrough's Diagrams tab. scope must differ between views that can
// be on screen together.
func NewMermaidKeys(scope string) *MermaidKeys {
	return &MermaidKeys{scope: scope, seen: map[string]int{}}
}

// HTML wraps a diagram definition in the container the client renderer
// turns into SVG. Three load-bearing pieces:
//
//   - lvt-ignore: livetemplate's morphdom skips this element and its whole
//     subtree, so a later server DOM patch (adding a comment elsewhere, a
//     scroll directive, …) never clobbers the SVG mermaid injects here.
//   - lvt-x:mermaid: arms the client handler in handlers/src/mermaid.ts,
//     which reads the definition from the <pre>'s textContent. The registry
//     ignores an empty value, so it carries a constant.
//   - html.EscapeString: untrusted repo content can't inject markup (matching
//     the pipeline's safe default). The browser decodes the entities back
//     when it computes textContent, so the mermaid parser still sees the
//     original definition (e.g. `A--&gt;B` → `A-->B`).
//
// The trailing newline goldmark leaves on the last fence line is trimmed so
// the definition mermaid parses has no spurious blank final line.
func (k *MermaidKeys) HTML(raw []byte) string {
	def := strings.TrimRight(string(raw), "\n")
	if def == "" {
		return ""
	}
	n := k.seen[def]
	k.seen[def] = n + 1
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%d\x00%s", k.scope, n, def)
	return fmt.Sprintf(`<div class="md-mermaid" data-key="mmd-%x" lvt-x:mermaid="svg" lvt-ignore><pre class="mermaid">`, h.Sum64()) +
		html.EscapeString(def) + `</pre></div>`
}
