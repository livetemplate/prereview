package gitdiff

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// GitHub "alerts" (a.k.a. admonitions) are a blockquote whose first line
// is a bare marker — `> [!NOTE]`, `[!TIP]`, `[!IMPORTANT]`, `[!WARNING]`
// or `[!CAUTION]` — rendered as a coloured callout box. goldmark has no
// built-in extension for them, so this is a small local one: zero new
// dependencies, in keeping with the single-binary ethos. It plugs into
// the same mdRenderer singleton, so a transformed alert renders correctly
// even when RenderMarkdownBlocks renders it in isolation (an alert is one
// top-level *ast.Blockquote → one commentable block).

// alertKind is the AST node kind for a transformed alert. Registered once
// at package init via ast.NewNodeKind.
var alertKind = ast.NewNodeKind("Alert")

// alertKinds maps each GitHub alert type — keyed by its lowercase name,
// which is also the CSS-class suffix (md-alert-<name>) and the marker token
// (`[!NOTE]` → "note") — to the title shown in the callout and the inline
// octicon SVG GitHub places beside it. The icon is inline (not <img>) so it
// needs no embedded asset and survives safe-mode: the renderer writes it
// verbatim, and it is a trusted constant, never user content. Order/spelling
// matches GitHub.
var alertKinds = map[string]struct{ title, icon string }{
	"note":      {"Note", `<svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M0 8a8 8 0 1 1 16 0A8 8 0 0 1 0 8Zm8-6.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13ZM6.5 7.75A.75.75 0 0 1 7.25 7h1a.75.75 0 0 1 .75.75v2.75h.25a.75.75 0 0 1 0 1.5h-2a.75.75 0 0 1 0-1.5h.25v-2h-.25a.75.75 0 0 1-.75-.75ZM8 6a1 1 0 1 1 0-2 1 1 0 0 1 0 2Z"/></svg>`},
	"tip":       {"Tip", `<svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M8 1.5c-2.363 0-4 1.69-4 3.75 0 .984.424 1.625.984 2.304l.214.253c.223.264.47.556.673.848.284.411.537.896.621 1.49a.75.75 0 0 1-1.484.211c-.04-.282-.163-.547-.37-.847a8.456 8.456 0 0 0-.542-.68c-.084-.1-.173-.205-.268-.32C3.201 7.75 2.5 6.766 2.5 5.25 2.5 2.31 4.863 0 8 0s5.5 2.31 5.5 5.25c0 1.516-.701 2.5-1.328 3.259-.095.115-.184.22-.268.319-.207.245-.383.453-.541.681-.208.3-.33.565-.37.847a.751.751 0 0 1-1.485-.212c.084-.593.337-1.078.621-1.489.203-.292.45-.584.673-.848.075-.088.147-.173.213-.253.561-.679.985-1.32.985-2.304 0-2.06-1.637-3.75-4-3.75ZM5.75 12h4.5a.75.75 0 0 1 0 1.5h-4.5a.75.75 0 0 1 0-1.5ZM6 15.25a.75.75 0 0 1 .75-.75h2.5a.75.75 0 0 1 0 1.5h-2.5a.75.75 0 0 1-.75-.75Z"/></svg>`},
	"important": {"Important", `<svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M0 1.75C0 .784.784 0 1.75 0h12.5C15.216 0 16 .784 16 1.75v9.5A1.75 1.75 0 0 1 14.25 13H8.06l-2.573 2.573A1.458 1.458 0 0 1 3 14.543V13H1.75A1.75 1.75 0 0 1 0 11.25Zm1.75-.25a.25.25 0 0 0-.25.25v9.5c0 .138.112.25.25.25h2a.75.75 0 0 1 .75.75v2.19l2.72-2.72a.749.749 0 0 1 .53-.22h6.5a.25.25 0 0 0 .25-.25v-9.5a.25.25 0 0 0-.25-.25Zm7 2.25v2.5a.75.75 0 0 1-1.5 0v-2.5a.75.75 0 0 1 1.5 0ZM9 9a1 1 0 1 1-2 0 1 1 0 0 1 2 0Z"/></svg>`},
	"warning":   {"Warning", `<svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M6.457 1.047c.659-1.234 2.427-1.234 3.086 0l6.082 11.378A1.75 1.75 0 0 1 14.082 15H1.918a1.75 1.75 0 0 1-1.543-2.575Zm1.763.707a.25.25 0 0 0-.44 0L1.698 13.132a.25.25 0 0 0 .22.368h12.164a.25.25 0 0 0 .22-.368Zm.53 3.996v2.5a.75.75 0 0 1-1.5 0v-2.5a.75.75 0 0 1 1.5 0ZM9 11a1 1 0 1 1-2 0 1 1 0 0 1 2 0Z"/></svg>`},
	"caution":   {"Caution", `<svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M4.47.22A.749.749 0 0 1 5 0h6c.199 0 .389.079.53.22l4.25 4.25c.141.14.22.331.22.53v6a.749.749 0 0 1-.22.53l-4.25 4.25A.749.749 0 0 1 11 16H5a.749.749 0 0 1-.53-.22L.22 11.53A.749.749 0 0 1 0 11V5c0-.199.079-.389.22-.53Zm.84 1.28L1.5 5.31v5.38l3.81 3.81h5.38l3.81-3.81V5.31L10.69 1.5ZM8 4a.75.75 0 0 1 .75.75v3.5a.75.75 0 0 1-1.5 0v-3.5A.75.75 0 0 1 8 4Zm0 8a1 1 0 1 1 0-2 1 1 0 0 1 0 2Z"/></svg>`},
}

// alertNode is a blockquote re-typed as an alert; AlertType is the
// lowercase callout name (a key into alertKinds). Its children are the
// blockquote's content with the `[!TYPE]` marker stripped.
type alertNode struct {
	ast.BaseBlock
	AlertType string
}

func (n *alertNode) Kind() ast.NodeKind         { return alertKind }
func (n *alertNode) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

// alertTransformer runs after parsing and rewrites every blockquote whose
// first source line is a bare `[!TYPE]` marker into an alertNode.
type alertTransformer struct{}

func (alertTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	// Collect every blockquote (at any nesting depth — GitHub renders alerts
	// inside list items and nested quotes too), then mutate: replacing a node
	// mid-walk is unsafe.
	var quotes []*ast.Blockquote
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if bq, ok := n.(*ast.Blockquote); ok {
				quotes = append(quotes, bq)
			}
		}
		return ast.WalkContinue, nil
	})

	for _, bq := range quotes {
		typ, para, lineStop := alertMarker(bq, src)
		if typ == "" {
			continue
		}
		// Strip the marker: goldmark tokenises `[!NOTE]` into several inline
		// nodes ("[", "!NOTE", "]"), so we remove every inline child that
		// begins on the marker's source line (the marker stands alone there
		// by GitHub's rule). Inlines are in source order, so we stop at the
		// first child past the line. If that empties the paragraph (a
		// marker-only blockquote), drop the paragraph too.
		var drop []ast.Node
		for c := para.FirstChild(); c != nil; c = c.NextSibling() {
			t, ok := c.(*ast.Text)
			if !ok || t.Segment.Start >= lineStop {
				break
			}
			drop = append(drop, c)
		}
		for _, c := range drop {
			para.RemoveChild(para, c)
		}
		if para.ChildCount() == 0 {
			bq.RemoveChild(bq, para)
		}
		alert := &alertNode{AlertType: typ}
		// Move the (now marker-free) blockquote children into the alert.
		for c := bq.FirstChild(); c != nil; c = bq.FirstChild() {
			alert.AppendChild(alert, c)
		}
		if p := bq.Parent(); p != nil {
			p.ReplaceChild(p, bq, alert)
		}
	}
}

// alertMarker reports the lowercase alert type for bq, the leading
// paragraph that carries the marker, and the source offset at which the
// marker's line ends — or ("", nil, 0) for an ordinary blockquote. The
// marker is read from the raw first SOURCE line (para.Lines()), not the
// inline AST: goldmark tokenises `[!NOTE]` into "[", "!NOTE", "]" because
// `[` opens a link, so no single Text node holds the whole token. Reading
// the source line also enforces GitHub's "marker alone on its line" rule —
// any trailing text on the line lands in the matched string and fails it.
func alertMarker(bq *ast.Blockquote, src []byte) (string, *ast.Paragraph, int) {
	para, ok := bq.FirstChild().(*ast.Paragraph)
	if !ok {
		return "", nil, 0
	}
	lines := para.Lines()
	if lines.Len() == 0 {
		return "", nil, 0
	}
	firstLine := lines.At(0)
	label := strings.TrimSpace(string(firstLine.Value(src)))
	if !strings.HasPrefix(label, "[!") || !strings.HasSuffix(label, "]") {
		return "", nil, 0
	}
	key := strings.ToLower(strings.TrimSpace(label[2 : len(label)-1]))
	if _, ok := alertKinds[key]; !ok {
		return "", nil, 0
	}
	return key, para, firstLine.Stop
}

// alertRenderer renders an alertNode as <div class="md-alert md-alert-TYPE">
// with a title row (octicon + label); its children render between via the
// default block renderers.
type alertRenderer struct{}

func (alertRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(alertKind, renderAlert)
}

func renderAlert(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*alertNode)
	if entering {
		k := alertKinds[n.AlertType]
		_, _ = w.WriteString(`<div class="md-alert md-alert-` + n.AlertType + `"><p class="md-alert-title">`)
		_, _ = w.WriteString(k.icon)
		_, _ = w.WriteString(k.title)
		_, _ = w.WriteString(`</p>`)
	} else {
		_, _ = w.WriteString("</div>")
	}
	return ast.WalkContinue, nil
}

// alertExtender wires the transformer and renderer into a goldmark.Markdown.
type alertExtender struct{}

func (alertExtender) Extend(m goldmark.Markdown) {
	// Priority is not load-bearing: alerts only rewrite top-level
	// blockquotes, which no other extension touches, so this transformer can
	// run in any order relative to GFM/footnote/emoji. 100 is an ordinary
	// default (goldmark applies lower priorities first).
	m.Parser().AddOptions(parser.WithASTTransformers(
		util.Prioritized(alertTransformer{}, 100),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(alertRenderer{}, 100),
	))
}
