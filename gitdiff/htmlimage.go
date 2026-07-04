package gitdiff

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
	"golang.org/x/net/html"
)

// goldmark runs in safe mode (no html.WithUnsafe, see markdown.go), so raw
// HTML in repo Markdown is dropped — replaced by `<!-- raw HTML omitted -->`.
// That takes the GitHub-README hero pattern with it:
//
//	<p align="center"><img src="docs/hero.gif" alt="…" width="820"></p>
//
// which renders blank even though the file is served by the static fallback
// (server.go). This extension re-admits a local `<img>`, optionally wrapped in a
// single `<p>`/`<div>` for centering, and EXTRACTS it out of a block that also
// carries inert sibling markup — a caption. goldmark groups two adjacent `<p>`
// blocks with no blank line between them into ONE ast.HTMLBlock, so the common
// "centered image + caption" hero (`<p><img></p><p>…caption…</p>`) arrives here
// as a single block; the caption must NOT veto the image (issue #104). So the
// image and its wrapper are emitted while inert siblings (a caption `<p>`, and
// `<sub>`/`<em>`/… formatting, and text) are DROPPED; a non-inert tag
// (`<script>`, `<a>`, `<iframe>`, …) still vetoes the whole block. It does so by
// OVERRIDING goldmark's default NodeRenderer for
// the raw-HTML node kinds rather than enabling WithUnsafe (which is all-or-
// nothing) or mutating the AST (a transformer would need custom block AND
// inline node types and would have to re-attach source line ranges so
// per-block commenting stays anchored). Overriding the renderer leaves the
// original HTMLBlock/RawHTML nodes — and thus segmentSpan's line math
// (markdown.go) — untouched.
//
// Two render paths are covered, matching how goldmark parses raw image HTML:
//   - ast.HTMLBlock — a `<p>`/`<div>`-wrapped hero OR a bare `<img>` on its own
//     line (both parse to a single block).
//   - ast.RawHTML — an inline `<img>` sitting mid-paragraph.
//
// An inline `<img>` keeps its paragraph from classifying one-sentence-per-line,
// so it renders via renderNode (which runs this override). The one degenerate
// case is an `<img>`-only LAST line of an otherwise sentence-per-line paragraph:
// it reaches renderProseLine's escaped-text fallback (markdown.go) and renders
// the tag escaped rather than as an image — rare enough to leave as-is.
//
// The src is left RELATIVE here; resolveImageSrc in the post-render pass
// (linkrewrite.go) turns it server-absolute against the file's directory, the
// same pass that resolves Markdown-syntax image srcs — so this renderer needs
// no currentPath and stays a package-global singleton like mdRenderer itself.
//
// SECURITY: this output lands in template.HTML (markdown.go) and so bypasses
// the contextual autoescaper. sanitizeImageHTML therefore ALLOWLISTS — a fixed
// set of tags and attributes — rather than denylisting. Only the `<img>` and its
// wrapper (with allowlisted attrs) are ever EMITTED; every other tag/text is
// dropped, so admitting inert caption siblings opens no new sink — the caption's
// markup and attributes never reach the output. A tag outside the allowlist
// (scripts, `<a>`, `<iframe>`, styles-as-a-tag, …), a non-local/data/javascript
// img src, a second image, an event handler or style on the img, or a stray
// close tag still makes the WHOLE block fall back to the omitted-comment, i.e.
// it is dropped exactly as before this extension existed.

// rawHTMLOmitted mirrors goldmark's safe-mode placeholder for dropped raw HTML
// (html.Renderer, pinned v1.8.2). Reproduced so a NON-image raw-HTML node — now
// that this renderer owns the kind — emits exactly what it did before: emit()
// in markdown.go skips an empty string, so writing the comment keeps the block
// list and line cursor identical for non-image blocks.
const rawHTMLOmitted = "<!-- raw HTML omitted -->"

// allowedImgAttr is the img attribute allowlist (besides src, handled
// separately for scheme validation). Everything else — on* handlers, style,
// srcset, class, id, … — is dropped.
var allowedImgAttr = map[string]bool{
	"alt": true, "width": true, "height": true, "title": true,
}

// allowedWrapper is the set of tags an `<img>` may be wrapped in. Only their
// `align` attribute survives (the centering the hero pattern relies on); a
// wrapper's other attributes — notably style= (a CSS url()/expression vector) —
// are dropped.
var allowedWrapper = map[string]bool{"p": true, "div": true}

// allowedAlign restricts the wrapper's align value to the meaningful set, so a
// junk value can't ride through even after escaping.
var allowedAlign = map[string]bool{
	"left": true, "right": true, "center": true, "justify": true,
}

// captionInline is the set of inert text-formatting tags allowed to appear as
// caption siblings of the image within one merged raw-HTML block (see the
// package doc — goldmark groups adjacent `<p>` blocks with no blank line into a
// single block, so an image's caption lands alongside it). These tags — and any
// text — are DROPPED, never emitted; only the `<img>` and its wrapper reach the
// output, so admitting them adds no sink. A tag outside this set (and outside
// img/wrapper) still vetoes the whole block.
var captionInline = map[string]bool{
	"sub": true, "sup": true, "em": true, "strong": true, "b": true,
	"i": true, "br": true, "code": true, "kbd": true, "small": true,
	"s": true, "u": true, "mark": true,
}

// wrapFrame is one open `<p>`/`<div>` on sanitizeImageHTML's nesting stack, held
// so the image's DIRECT wrapper (the innermost open frame when the `<img>` is
// seen) can be re-emitted with its centering align.
type wrapFrame struct {
	name  string
	align string
}

// imageRenderer overrides the raw-HTML node renderers to pass through a safe,
// local <img>. Registered at priority 100 (lower wins in goldmark; the default
// html renderer is 1000), see imageExtender.
type imageRenderer struct{}

func (imageRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHTMLBlock, renderImageHTMLBlock)
	reg.Register(ast.KindRawHTML, renderImageRawHTML)
}

func renderImageHTMLBlock(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	if img, ok := sanitizeImageHTML(htmlBlockRaw(n.(*ast.HTMLBlock), source)); ok {
		_, _ = w.WriteString(img)
	} else {
		_, _ = w.WriteString(rawHTMLOmitted + "\n")
	}
	return ast.WalkSkipChildren, nil
}

func renderImageRawHTML(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	if img, ok := sanitizeImageHTML(rawHTMLRaw(n.(*ast.RawHTML), source)); ok {
		_, _ = w.WriteString(img)
	} else {
		_, _ = w.WriteString(rawHTMLOmitted)
	}
	return ast.WalkSkipChildren, nil
}

// htmlBlockRaw reconstructs an HTMLBlock's source text, including the closure
// line goldmark stores separately for closed block types.
func htmlBlockRaw(b *ast.HTMLBlock, source []byte) string {
	var sb strings.Builder
	l := b.Lines()
	for i := 0; i < l.Len(); i++ {
		seg := l.At(i)
		sb.Write(seg.Value(source))
	}
	if b.HasClosure() {
		sb.Write(b.ClosureLine.Value(source))
	}
	return sb.String()
}

// rawHTMLRaw reconstructs an inline RawHTML node's source text.
func rawHTMLRaw(r *ast.RawHTML, source []byte) string {
	var sb strings.Builder
	for i := 0; i < r.Segments.Len(); i++ {
		seg := r.Segments.At(i)
		sb.Write(seg.Value(source))
	}
	return sb.String()
}

// sanitizeImageHTML extracts the single admissible local `<img>` — optionally
// wrapped in a single `<p>`/`<div>` for centering — from a raw-HTML block, and
// returns ok=false (caller drops the block) if there is no such image or the
// block carries a NON-inert construct. Inert caption siblings — text and
// captionInline tags (`<sub>`/`<em>`/…) and their own `<p>`/`<div>` wrappers —
// are DROPPED, not vetoed, so a caption alongside the image no longer blanks it
// (issue #104). A tag outside {img, wrapper, captionInline} (`<script>`, `<a>`,
// `<iframe>`, …), a comment, a second image, or a stray/mismatched wrapper close
// rejects the whole block. Only the image + wrapper are emitted.
func sanitizeImageHTML(raw string) (string, bool) {
	z := html.NewTokenizer(strings.NewReader(raw))

	var (
		stack     []wrapFrame // open <p>/<div>, LIFO — to find the img's direct wrapper
		imgTag    string
		haveImg   bool
		wrapper   string // the img's direct wrapper tag ("" = none)
		wrapAlign string
	)

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break // EOF (io.EOF) or a parse error — either way, stop.
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			nameBytes, hasAttr := z.TagName()
			name := strings.ToLower(string(nameBytes))
			var attrs []rawAttr
			if hasAttr {
				attrs = readAttrs(z)
			}
			switch {
			case name == "img":
				if haveImg {
					return "", false // at most one image
				}
				built, ok := buildSafeImg(attrs)
				if !ok {
					return "", false
				}
				imgTag, haveImg = built, true
				// The img's direct wrapper is the innermost open <p>/<div>.
				if len(stack) > 0 {
					top := stack[len(stack)-1]
					wrapper, wrapAlign = top.name, top.align
				}
			case allowedWrapper[name]:
				// A <p>/<div>: the img's wrapper OR a caption container. Only a
				// non-self-closing one opens a frame to match a later close.
				if tt != html.SelfClosingTagToken {
					stack = append(stack, wrapFrame{name, wrapperAlignValue(attrs)})
				}
			case captionInline[name]:
				// inert caption formatting — dropped, never emitted
			default:
				return "", false // <script>/<a>/<iframe>/… veto the whole block
			}
		case html.EndTagToken:
			nameBytes, _ := z.TagName()
			name := strings.ToLower(string(nameBytes))
			switch {
			case allowedWrapper[name]:
				if len(stack) == 0 || stack[len(stack)-1].name != name {
					return "", false // stray/mismatched wrapper close
				}
				stack = stack[:len(stack)-1]
			case captionInline[name]:
				// inert caption close — dropped
			default:
				return "", false // stray close of a non-allowlisted tag
			}
		case html.TextToken:
			// caption text or whitespace — dropped
		default: // CommentToken, DoctypeToken
			return "", false
		}
	}

	if !haveImg || len(stack) != 0 {
		return "", false // no image, or an unclosed wrapper
	}
	if wrapper == "" {
		return imgTag, true
	}
	var wrapAttrs []rawAttr
	if wrapAlign != "" {
		wrapAttrs = []rawAttr{{"align", wrapAlign}}
	}
	return string(serializeTag(wrapper, wrapAttrs, false)) + imgTag + "</" + wrapper + ">", true
}

// buildSafeImg rebuilds an `<img>` from the allowlist: a local relative src
// (no scheme, not protocol-relative/absolute, not data:/javascript:) plus
// alt/width/height/title. The src is kept relative — resolveImageSrc resolves
// it server-absolute post-render. ok=false drops the whole block. serializeTag
// (htmlpreview.go) re-escapes every value and drops on*/javascript: — redundant
// here since keys are allowlisted and src is validated local, but it keeps the
// emit path identical to the rest of the package.
func buildSafeImg(attrs []rawAttr) (string, bool) {
	var (
		src     string
		haveSrc bool
		rest    []rawAttr
	)
	for _, a := range attrs {
		switch key := strings.ToLower(a.key); {
		case key == "src":
			src, haveSrc = a.val, true
		case allowedImgAttr[key]:
			rest = append(rest, rawAttr{key, a.val})
		}
	}
	if !haveSrc || strings.TrimSpace(src) == "" || !isLocalImageSrc(src) {
		return "", false
	}
	// src leads the tag, then the allowlisted alt/width/height/title.
	return string(serializeTag("img", append([]rawAttr{{"src", src}}, rest...), false)), true
}

// wrapperAlignValue returns the wrapper's align value if it is in the
// meaningful set, else "" (the wrapper is still emitted, just uncentered).
func wrapperAlignValue(attrs []rawAttr) string {
	for _, a := range attrs {
		if strings.ToLower(a.key) != "align" {
			continue
		}
		if v := strings.ToLower(strings.TrimSpace(a.val)); allowedAlign[v] {
			return v
		}
	}
	return ""
}

// isLocalImageSrc reports whether src is a repo-relative path (the only kind a
// raw-HTML <img> may carry) — i.e. not isExternalTarget (no URL scheme, not
// protocol-relative `//host`, not server-absolute `/path`, no query). This is
// the local-only posture for the newly re-admitted raw-HTML surface —
// remote/data/javascript srcs are rejected. (Markdown-syntax `![](https://…)`
// images keep working; they flow through goldmark's own image node, not this
// sanitizer.) Path containment (`../` escaping the root) is enforced later by
// resolveImageSrc, which has the file's path; the static fallback's traversal
// guard (server.go) backstops it.
func isLocalImageSrc(src string) bool {
	t := strings.TrimSpace(src)
	return t != "" && !isExternalTarget(t)
}

// imageExtender wires the override into a goldmark.Markdown. Renderer-only —
// no parser/transformer options, since the AST is left as-is.
type imageExtender struct{}

func (imageExtender) Extend(m goldmark.Markdown) {
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(imageRenderer{}, 100),
	))
}
