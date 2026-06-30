package gitdiff

import (
	"bytes"
	htmlpkg "html"
	"strconv"
	"strings"

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
//   - Each top-level <body> child element gets data-from/data-to
//     attributes carrying its source line range; the client maps a click
//     inside the iframe to the matching range via closest('[data-from]').
//   - The preview bridge beacon (previewBridgeJS) is injected so the iframe can
//     post its height + block rects out (the iframe is opaque-origin, so the
//     parent can't read its contentDocument).
//   - on* event-handler attributes and javascript: URLs are still stripped
//     (serializeTag). <script> elements are NOT stripped — the page's own
//     scripts must run (that is the whole point: JS-generated CSS like the
//     Tailwind Play CDN). The execution boundary is the opaque-origin sandbox
//     (sandbox="allow-scripts", NO allow-same-origin), which lets scripts run
//     but blocks all access to the parent app — never the two together.
//
// currentPath is the file's repo-relative path (drives the <base> dir).
// Empty input, or input without a <body>, yields an empty document and
// nil blocks (no commentable surface).
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
		`<style>[data-from]{cursor:pointer}</style>` + previewBridgeJS

	z := html.NewTokenizer(bytes.NewReader(src))
	line := 1

	var (
		out      bytes.Buffer
		blocks   []HTMLBlock
		inHead   bool
		inBody   bool
		baseDone bool
		// depthInBody counts the open-tag stack INSIDE <body>. 0 means
		// we're between top-level children; >=1 means we're inside the
		// current top-level child (which is buffered in childBuf until it
		// closes, so its start tag can be emitted with the now-known end
		// line as data-to).
		depthInBody    int
		childBuf       bytes.Buffer
		childName      string
		childAttrs     []rawAttr
		childStartLine int
	)

	// sink is where passthrough bytes go: into the current top-level child
	// buffer while we're inside one, else straight to the output.
	sink := func() *bytes.Buffer {
		if depthInBody >= 1 {
			return &childBuf
		}
		return &out
	}

	// emitChild flushes the buffered top-level child: its start tag (with
	// the data-from/data-to range appended) followed by its inner content
	// and close tag (already in childBuf).
	emitChild := func(endLine int, selfClosing bool) {
		attrs := appendDataRange(childAttrs, childStartLine, endLine)
		out.Write(serializeTag(childName, attrs, selfClosing))
		out.Write(childBuf.Bytes())
		blocks = append(blocks, HTMLBlock{StartLine: childStartLine, EndLine: endLine})
		childBuf.Reset()
	}

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			// Malformed input — flush any unclosed child at EOF.
			if depthInBody >= 1 {
				emitChild(line, false)
				depthInBody = 0
			}
			break
		}
		raw := z.Raw()
		startLine := line
		line += bytes.Count(raw, []byte("\n"))

		switch tt {
		case html.DoctypeToken, html.CommentToken, html.TextToken:
			sink().Write(raw)

		case html.StartTagToken:
			nameBytes, hasAttr := z.TagName()
			name := string(nameBytes)
			var attrs []rawAttr
			if hasAttr {
				attrs = readAttrs(z)
			}

			if !inHead && !inBody && name == "head" {
				out.Write(serializeTag(name, attrs, false))
				out.WriteString(headInject)
				baseDone = true
				inHead = true
				continue
			}
			if !inBody && name == "body" {
				if !baseDone {
					out.WriteString(headInject)
					baseDone = true
				}
				out.Write(serializeTag(name, attrs, false))
				inBody = true
				depthInBody = 0
				continue
			}
			if inBody && depthInBody == 0 {
				// A <script> as a direct <body> child passes through to run, but
				// is NOT a commentable block (no visual content). Leaving
				// depthInBody at 0 lets its rawtext + </script> flow to `out` as
				// passthrough, and the next real child starts fresh.
				if name == "script" {
					out.Write(serializeTag(name, attrs, false))
					continue
				}
				// A new top-level <body> child opens: start buffering it so
				// emitChild can stamp data-to once we know the end line.
				childName = name
				childAttrs = attrs
				childStartLine = startLine
				depthInBody = 1
				if voidElements[name] {
					emitChild(line, false)
					depthInBody = 0
				}
				continue
			}
			// Nested inside a child, or inside <head>, or outside <body>.
			sink().Write(serializeTag(name, attrs, false))
			if inBody && depthInBody >= 1 && !voidElements[name] {
				depthInBody++
			}

		case html.SelfClosingTagToken:
			nameBytes, hasAttr := z.TagName()
			name := string(nameBytes)
			var attrs []rawAttr
			if hasAttr {
				attrs = readAttrs(z)
			}
			if inBody && depthInBody == 0 {
				// Self-closing <script/> as a body child: pass through, no block
				// (mirrors the start-tag case above).
				if name == "script" {
					out.Write(serializeTag(name, attrs, true))
					continue
				}
				childName = name
				childAttrs = attrs
				childStartLine = startLine
				depthInBody = 1
				emitChild(line, true)
				depthInBody = 0
				continue
			}
			sink().Write(serializeTag(name, attrs, true))

		case html.EndTagToken:
			nameBytes, _ := z.TagName()
			name := string(nameBytes)
			if inBody && name == "body" {
				inBody = false
				out.Write(raw)
				continue
			}
			if inHead && name == "head" {
				inHead = false
				out.Write(raw)
				continue
			}
			if inBody && depthInBody >= 1 {
				childBuf.Write(raw)
				depthInBody--
				if depthInBody == 0 {
					emitChild(line, false)
				}
				continue
			}
			out.Write(raw)
		}
	}

	if len(blocks) == 0 {
		return "", nil
	}
	return out.String(), blocks
}

// previewBaseHref builds the <base> href for a file at currentPath: the
// server-absolute, URL-encoded directory with a trailing slash, so the
// iframe's relative asset URLs resolve there. Root-level files get "/".
func previewBaseHref(currentPath string) string {
	dir := ""
	if i := strings.LastIndex(currentPath, "/"); i >= 0 {
		dir = currentPath[:i]
	}
	if dir == "" {
		return "/"
	}
	return "/" + encodePath(dir) + "/"
}

// appendDataRange returns attrs plus data-from/data-to carrying the
// block's source line range. The input slice is not mutated.
func appendDataRange(attrs []rawAttr, from, to int) []rawAttr {
	out := make([]rawAttr, 0, len(attrs)+2)
	out = append(out, attrs...)
	out = append(out,
		rawAttr{"data-from", strconv.Itoa(from)},
		rawAttr{"data-to", strconv.Itoa(to)},
	)
	return out
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
