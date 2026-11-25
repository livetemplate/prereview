package gitdiff

import (
	"bytes"
	htmlpkg "html"
	"strconv"
	"strings"
	"sync/atomic"

	"golang.org/x/net/html"
)

// HTMLBlock is one independently-commentable region of an HTML file: a
// top-level <body> child tagged with the 1-based, inclusive SOURCE line
// range it came from. The line range is what makes HTML commenting
// line-accurate — a comment placed on a block anchors to these real
// source lines so it round-trips with the raw view and the CSV (same
// contract as MarkdownBlock).
//
// The block carries no HTML of its own: the whole document is rendered
// once into the preview iframe's srcdoc (see RenderHTMLPreview), and the
// block's element there carries matching data-from/data-to attributes so
// a click in the iframe resolves to this range.
type HTMLBlock struct {
	StartLine int
	EndLine   int
}

// voidElements is the HTML5 void-element set: tags the parser reports as
// StartTagToken with no matching EndTagToken. Tracking these prevents
// depth-counter drift.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// openElement is one element open inside <body> while RenderHTMLPreview
// waits for its end line.
type openElement struct {
	start    int
	annotate bool // gets data-from/data-to
	inner    bool // its descendants may be annotated
	patch    int  // index into the patches its range fills, or -1
}

// rangePatch is a start tag's range attributes, spliced into the output at
// byte offset at (just before the tag's '>') once the element has closed.
type rangePatch struct {
	at   int
	text string
}

// rangeAttrs are an element's data-from/data-to. A top-level block always gets
// both; a one-line nested element (most of them) gets data-from alone — the
// bridge defaults data-to to data-from — to keep the srcdoc lean.
func rangeAttrs(from, to int, top bool) []rawAttr {
	if !top && from == to {
		return []rawAttr{{"data-from", strconv.Itoa(from)}}
	}
	return []rawAttr{{"data-from", strconv.Itoa(from)}, {"data-to", strconv.Itoa(to)}}
}

// splicePatches inserts every patch's text at its offset in one pass. Patches
// are recorded in document order (start tags), so their offsets ascend.
func splicePatches(out []byte, patches []rangePatch) string {
	n := len(out)
	for _, p := range patches {
		n += len(p.text)
	}
	var b strings.Builder
	b.Grow(n)
	prev := 0
	for _, p := range patches {
		b.Write(out[prev:p.at])
		b.WriteString(p.text)
		prev = p.at
	}
	b.Write(out[prev:])
	return b.String()
}

// nonBlockElements render nothing themselves, so they get no line range.
var nonBlockElements = map[string]bool{"script": true, "style": true, "template": true, "noscript": true}

// opaqueElements' descendants aren't layout blocks (or aren't markup at all):
// nothing inside them is annotated. An <svg> itself is a block; its shapes are not.
var opaqueElements = map[string]bool{"script": true, "style": true, "template": true, "noscript": true, "svg": true, "math": true}

// headMetaElements is the HTML5 metadata-content set: elements that belong
// in <head>. In a document written without explicit <head>/<body> tags
// (both are optional in HTML5), these still count as head content and pass
// through untouched; the FIRST element NOT in this set implicitly opens the
// body — matching the parser's own "the body element start tag can be
// omitted" rule. Tracking them is what lets a body-less page still render a
// preview instead of falling through to the raw view (issue #79).
var headMetaElements = map[string]bool{
	"base": true, "basefont": true, "bgsound": true, "link": true,
	"meta": true, "noscript": true, "script": true, "style": true,
	"template": true, "title": true,
}

// rawAttr is one HTML attribute pulled from the tokenizer, kept in the
// parsed (entity-decoded) form. Re-emitting goes through
// htmlpkg.EscapeString so any & " < > in the value round-trip safely.
type rawAttr struct {
	key, val string
}

// RenderHTMLPreview transforms an HTML document into the form shown in the
// preview iframe's srcdoc and returns it alongside the per-block source
// line ranges used for commenting.
//
// The document is passed through almost verbatim so the iframe renders a
// real page (real <body>, <head> styles, var()/@media/vw/sticky all
// resolve against the iframe viewport). These transforms apply:
//
//   - A <base href="/<dir>/"> is injected (the file's directory, URL-
//     encoded) so relative href/src resolve to server-absolute paths the
//     static fallback serves — srcdoc otherwise inherits the parent's base
//     and would fetch a subdirectory file's assets from the repo root.
//   - Every element inside <body> gets data-from/data-to attributes carrying
//     its own source line range (not inside script/style/template/noscript or
//     svg/math internals), so a drawn region resolves to just the elements it
//     covers. The returned blocks are the top-level <body> children only (they
//     group the comment list under the preview).
//   - The preview bridge beacon (previewBridgeJS) is injected so the iframe can
//     post its height + block rects out (the iframe is opaque-origin, so the
//     parent can't read its contentDocument).
//   - Viewport-height units in <style> text and style attributes are pinned
//     to a fixed basis (rewriteViewportUnits), or a page with a 100vh hero
//     would grow with the auto-heighted iframe without end.
//   - on* event-handler attributes and javascript: URLs are still stripped
//     (serializeTag). <script> elements are NOT stripped — the page's own
//     scripts must run (that is the whole point: JS-generated CSS like the
//     Tailwind Play CDN). The execution boundary is the opaque-origin sandbox
//     (sandbox="allow-scripts", NO allow-same-origin), which lets scripts run
//     but blocks all access to the parent app — never the two together.
//
// <head> and <body> are both optional in HTML5, so the tokenizer also
// synthesizes an implicit body: outside any explicit structural element,
// head-metadata tags (style/link/script/…) pass through as head content
// and the first flow element opens the body (see headMetaElements). A
// body-less page therefore renders a preview like any other.
//
// currentPath is the file's repo-relative path (drives the <base> dir).
// Empty input, or input with no flow content at all (e.g. head-only),
// yields an empty document and nil blocks (no commentable surface).
//
// The document is returned as a plain string: the template places it in
// the iframe's `srcdoc="…"` attribute, where html/template's contextual
// autoescaper applies the correct escaping. The browser reconstitutes the
// document from the attribute; the opaque-origin sandbox (allow-scripts, no
// allow-same-origin) is the security boundary — scripts run but can't reach
// the parent app.
func RenderHTMLPreview(src []byte, currentPath string) (string, []HTMLBlock) {
	if len(bytes.TrimSpace(src)) == 0 {
		return "", nil
	}

	// Injected into the head: a <base> so relative assets resolve to the
	// file's server directory, a cursor:pointer rule on the block elements so
	// they read as tappable AND so iOS Safari treats a tap on them as a click
	// (it only fires click on "clickable" elements), and the preview bridge
	// beacon (previewBridgeJS) — the iframe is an opaque-origin sandbox that
	// runs the page's own scripts, so it posts its height + block rects out to
	// the parent rather than the parent reading its contentDocument.
	headInject := `<base href="` + htmlpkg.EscapeString(previewBaseHref(currentPath)) + `">` +
		`<style>body>[data-from]{cursor:pointer}</style>` + previewBridgeJS

	z := html.NewTokenizer(bytes.NewReader(src))
	line := 1

	var (
		out      bytes.Buffer
		blocks   []HTMLBlock
		inHead   bool
		inBody   bool
		baseDone bool
		// stack holds the elements open INSIDE <body>; the bottom frame is a
		// top-level <body> child (those are also the returned blocks). Every
		// element's range attributes are only known when it closes, so instead
		// of buffering content per element (which copies the same bytes once per
		// nesting level — quadratic on deep markup) everything streams into out
		// once, each annotated start tag records where its '>' landed, and the
		// ranges are spliced in at the end. Linear in the input at any depth.
		stack   []openElement
		patches []rangePatch
		// inStyle is set inside a <style> element, whose text is CSS.
		inStyle bool
	)

	// sink is where passthrough bytes go: always the single output buffer.
	sink := func() *bytes.Buffer { return &out }

	// stamp decides, for an element opening at the current depth, whether it
	// gets data-from/data-to and whether its descendants may. A top-level child
	// is a block unless it's a <script> (no visual content; it still runs); a
	// nested element is annotated unless it's non-rendering or inside an element
	// whose children aren't layout blocks (svg internals, script/style text…).
	stamp := func(name string) (annotate, inner bool) {
		parentInner := len(stack) == 0 || stack[len(stack)-1].inner
		if len(stack) == 0 {
			annotate = name != "script"
		} else {
			annotate = parentInner && !nonBlockElements[name]
		}
		return annotate, parentInner && !opaqueElements[name]
	}

	// emitVoid writes an element with no content (void or self-closing): its
	// range is known now, so its attributes are written directly.
	emitVoid := func(name string, attrs []rawAttr, from, to int, annotate, selfClosing bool) {
		top := len(stack) == 0
		if annotate {
			attrs = append(append(make([]rawAttr, 0, len(attrs)+2), attrs...), rangeAttrs(from, to, top)...)
		}
		out.Write(serializeTag(name, attrs, selfClosing))
		if annotate && top {
			blocks = append(blocks, HTMLBlock{StartLine: from, EndLine: to})
		}
	}

	// open writes an element's start tag now and, when annotated, records where
	// its closing '>' is so closeTop can splice the range in before it.
	open := func(name string, attrs []rawAttr, from int, annotate, inner bool) {
		tag := serializeTag(name, attrs, false)
		out.Write(tag[:len(tag)-1])
		f := openElement{start: from, annotate: annotate, inner: inner, patch: -1}
		if annotate {
			patches = append(patches, rangePatch{at: out.Len()})
			f.patch = len(patches) - 1
		}
		out.WriteByte('>')
		stack = append(stack, f)
	}

	// closeTop pops the innermost open element, ending at endLine.
	closeTop := func(endLine int, end []byte) {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out.Write(end)
		if f.patch < 0 {
			return
		}
		top := len(stack) == 0
		var b strings.Builder
		for _, a := range rangeAttrs(f.start, endLine, top) {
			b.WriteString(" " + a.key + `="` + a.val + `"`)
		}
		patches[f.patch].text = b.String()
		if top {
			blocks = append(blocks, HTMLBlock{StartLine: f.start, EndLine: endLine})
		}
	}

	// openImplicitStructure handles a start/self-closing tag seen while
	// outside <head>/<body> (and past <html>): a document with the optional
	// <head>/<body> tags omitted. It injects the head payload once, then
	// reports whether name is head-metadata (isHeadMeta → caller writes it
	// as head-content passthrough and continues) or a flow element that
	// implicitly opens the body (isHeadMeta false → inBody is now set, so
	// the caller treats the tag as the first top-level body child).
	openImplicitStructure := func(name string) (isHeadMeta bool) {
		if !baseDone {
			out.WriteString(headInject)
			baseDone = true
		}
		if headMetaElements[name] {
			return true
		}
		inBody = true
		return false
	}

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			// Malformed input — close whatever is still open at EOF.
			for len(stack) > 0 {
				closeTop(line, nil)
			}
			break
		}
		raw := z.Raw()
		startLine := line
		line += bytes.Count(raw, []byte("\n"))

		switch tt {
		case html.DoctypeToken, html.CommentToken:
			sink().Write(raw)

		case html.TextToken:
			if inStyle {
				sink().WriteString(rewriteViewportUnits(string(raw)))
			} else {
				sink().Write(raw)
			}

		case html.StartTagToken, html.SelfClosingTagToken:
			selfClosing := tt == html.SelfClosingTagToken
			nameBytes, hasAttr := z.TagName()
			name := string(nameBytes)
			var attrs []rawAttr
			if hasAttr {
				attrs = rewriteStyleAttrs(prefixRootURLs(readAttrs(z)))
			}
			if name == "style" && !selfClosing {
				inStyle = true
			}

			if !selfClosing && !inHead && !inBody && name == "head" {
				out.Write(serializeTag(name, attrs, false))
				out.WriteString(headInject)
				baseDone = true
				inHead = true
				continue
			}
			if !selfClosing && !inBody && name == "body" {
				if !baseDone {
					out.WriteString(headInject)
					baseDone = true
				}
				out.Write(serializeTag(name, attrs, false))
				inBody = true
				continue
			}
			// No explicit <head>/<body> yet: a head-metadata tag is head
			// content (passthrough); the first flow tag opens the body and
			// falls through to the body logic below.
			if !inHead && !inBody && name != "html" {
				if openImplicitStructure(name) {
					out.Write(serializeTag(name, attrs, selfClosing))
					continue
				}
			}
			if !inBody {
				sink().Write(serializeTag(name, attrs, selfClosing))
				continue
			}
			annotate, inner := stamp(name)
			if selfClosing || voidElements[name] {
				emitVoid(name, attrs, startLine, line, annotate, selfClosing)
				continue
			}
			open(name, attrs, startLine, annotate, inner)

		case html.EndTagToken:
			nameBytes, _ := z.TagName()
			name := string(nameBytes)
			if name == "style" {
				inStyle = false
			}
			if inBody && name == "body" {
				for len(stack) > 0 { // unclosed elements end with the body
					closeTop(line, nil)
				}
				inBody = false
				out.Write(raw)
				continue
			}
			if inHead && name == "head" {
				inHead = false
				out.Write(raw)
				continue
			}
			if inBody && len(stack) > 0 {
				closeTop(line, raw)
				continue
			}
			out.Write(raw)
		}
	}

	if len(blocks) == 0 {
		return "", nil
	}
	return splicePatches(out.Bytes(), patches), blocks
}

// previewBaseHref builds the <base> href for a file at currentPath: the
// server-absolute, URL-encoded directory with a trailing slash, so the
// iframe's relative asset URLs resolve there. Root-level files get "/".
func previewBaseHref(currentPath string) string {
	dir := ""
	if i := strings.LastIndex(currentPath, "/"); i >= 0 {
		dir = currentPath[:i]
	}
	href := previewFilesBase() + "/"
	if dir != "" {
		href += encodePath(dir) + "/"
	}
	return href
}

var filesBase atomic.Pointer[string]

// SetPreviewFilesBase sets the path prefix (such as "/_files/<key>") that the
// HTML preview loads repo files under. The preview iframe is an opaque origin,
// so the browser sends no cookie with its requests, and the prefix carries a
// read-only key instead. Call it once at startup, before any diff loads. Unset
// (as in tests), files load from the server root.
func SetPreviewFilesBase(prefix string) { filesBase.Store(&prefix) }

func previewFilesBase() string {
	if p := filesBase.Load(); p != nil {
		return *p
	}
	return ""
}

// prefixRootURLs moves root-absolute URL attributes ("/img/a.png", and each
// srcset candidate) under the preview files base, as <base> does for relative
// ones. Protocol-relative
// "//host" URLs are another site's and stay as they are. A url() inside a
// stylesheet is not rewritten.
func prefixRootURLs(attrs []rawAttr) []rawAttr {
	base := previewFilesBase()
	if base == "" {
		return attrs
	}
	root := func(u string) bool { return strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") }
	for i, a := range attrs {
		switch {
		case strings.EqualFold(a.key, "srcset"):
			cands := strings.Split(a.val, ",")
			for j, c := range cands {
				if t := strings.TrimLeft(c, " \t\n"); root(t) {
					cands[j] = c[:len(c)-len(t)] + base + t
				}
			}
			attrs[i].val = strings.Join(cands, ",")
		case (isURLAttr(a.key) || strings.EqualFold(a.key, "poster")) && root(a.val):
			attrs[i].val = base + a.val
		}
	}
	return attrs
}

// readAttrs drains the tokenizer's attribute cursor for the current
// start/self-closing tag. Returning a slice lets caller code inspect
// attrs AND re-serialize from the same data, which z.Token() + z.TagAttr()
// can't both do (they consume the same cursor).
func readAttrs(z *html.Tokenizer) []rawAttr {
	var out []rawAttr
	for {
		k, v, more := z.TagAttr()
		out = append(out, rawAttr{string(k), string(v)})
		if !more {
			break
		}
	}
	return out
}

// serializeTag rebuilds `<name attr="val" …>` (or `…/>` if selfClosing),
// dropping on* event handlers, empty attr names, and javascript: URLs.
func serializeTag(name string, attrs []rawAttr, selfClosing bool) []byte {
	var buf bytes.Buffer
	buf.WriteByte('<')
	buf.WriteString(name)
	for _, a := range attrs {
		if a.key == "" {
			continue
		}
		keyLower := strings.ToLower(a.key)
		if strings.HasPrefix(keyLower, "on") {
			continue
		}
		if isURLAttr(a.key) && hasJavascriptScheme(a.val) {
			continue
		}
		buf.WriteByte(' ')
		buf.WriteString(a.key)
		buf.WriteString(`="`)
		buf.WriteString(htmlpkg.EscapeString(a.val))
		buf.WriteByte('"')
	}
	if selfClosing {
		buf.WriteByte('/')
	}
	buf.WriteByte('>')
	return buf.Bytes()
}

// isURLAttr identifies attributes whose value is a URL — the targets for
// javascript: scheme stripping.
func isURLAttr(name string) bool {
	switch strings.ToLower(name) {
	case "href", "src", "action", "formaction", "xlink:href", "ping":
		return true
	}
	return false
}

// hasJavascriptScheme reports whether val starts with the javascript:
// scheme after leading-whitespace trim. Conservative match: case-
// insensitive, ASCII whitespace only (matches the HTML5 parser's view).
func hasJavascriptScheme(val string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(val)), "javascript:")
}
