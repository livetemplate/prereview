package gitdiff

import (
	"bytes"
	htmlpkg "html"
	"html/template"
	"strings"

	"golang.org/x/net/html"
)

// HTMLBlock is one independently-commentable rendered unit of an HTML
// file plus the 1-based, inclusive SOURCE line range it came from. The
// line range is what makes HTML commenting line-accurate: a comment
// placed on a block anchors to these real source lines so it round-trips
// with the raw view and the CSV — same contract as MarkdownBlock.
//
// HTML carries the block element wrapped in the document's <head>
// stylesheets (each block's shadow root needs its own copy for visual
// fidelity inside the isolation boundary). The client hydrates these
// into Declarative Shadow DOM shadow roots, so the user's CSS applies
// only inside the block — no leakage to the SPA chrome.
type HTMLBlock struct {
	HTML      template.HTML
	StartLine int
	EndLine   int
}

// voidElements is the HTML5 void-element set: tags that the parser
// reports as StartTagToken but have no matching EndTagToken. Tracking
// these prevents depth-counter drift.
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

// RenderHTMLBlocks parses src and returns each top-level <body> child as
// a block tagged with its source line span. Each block's HTML embeds
// the head's stylesheets (link/style) so the per-block shadow root has
// access to the user's CSS. Stripped of <script> elements, on* event-
// handler attributes, and javascript: URLs — the inline DOM renders in
// the SPA origin (within a shadow root) which would let those mutate
// livetemplate state or fire on element load.
//
// currentPath drives the relative-link rewriter: `<a href="other.html">`
// and `<a href="#section">` inside the preview are rewritten to deep-
// link hashes so a click stays in the SPA; external `href`s pass
// through. Empty currentPath disables rewriting.
//
// Empty input → nil. Input without a <body> → nil (no commentable
// surface).
func RenderHTMLBlocks(src []byte, currentPath string) []HTMLBlock {
	if len(bytes.TrimSpace(src)) == 0 {
		return nil
	}

	z := html.NewTokenizer(bytes.NewReader(src))
	line := 1

	var (
		preamble       bytes.Buffer
		inHead         bool
		inBody         bool
		inStyle        bool // captures text inside head <style>
		blockBuf       bytes.Buffer
		blockStartLine int
		// depthInBody counts the open-tag stack INSIDE <body>. 0 means
		// we're at the body element itself; 1 means we just opened a
		// top-level body child (new block); >1 means we're nested
		// inside a block.
		depthInBody int
		blocks      []HTMLBlock
	)

	emitBlock := func(endLine int) {
		trimmed := bytes.TrimSpace(blockBuf.Bytes())
		if len(trimmed) == 0 {
			blockBuf.Reset()
			return
		}
		// Compose final block HTML: preamble (head styles) + element.
		// Browser caches subsequent link fetches, so duplicating the
		// preamble per block has near-zero network cost on warm cache.
		var combined bytes.Buffer
		combined.Write(preamble.Bytes())
		combined.Write(trimmed)
		blocks = append(blocks, HTMLBlock{
			HTML:      template.HTML(combined.String()), //nolint:gosec // sanitized: scripts/on*/javascript: stripped, preamble is head's <style>/<link>
			StartLine: blockStartLine,
			EndLine:   endLine,
		})
		blockBuf.Reset()
	}

	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			// Flush any open block at EOF (malformed HTML — missing
			// close tag).
			if blockBuf.Len() > 0 {
				emitBlock(line)
			}
			return blocks
		}
		raw := z.Raw()
		startLine := line
		line += bytes.Count(raw, []byte("\n"))

		switch tt {
		case html.DoctypeToken, html.CommentToken:
			// Skip — never part of a commentable block.

		case html.TextToken:
			if inStyle {
				preamble.Write(raw)
			} else if inBody && depthInBody >= 1 {
				blockBuf.Write(raw)
			}

		case html.StartTagToken:
			nameBytes, hasAttr := z.TagName()
			tagName := string(nameBytes)
			var attrs []rawAttr
			if hasAttr {
				attrs = readAttrs(z)
			}

			// Drop <script> entirely. Its content arrives as one
			// rawtext TextToken which we'd otherwise include in the
			// current block.
			if tagName == "script" {
				skipUntilCloseTag(z, "script", &line)
				continue
			}

			if !inBody && !inHead && tagName == "head" {
				inHead = true
				continue
			}
			if !inBody && tagName == "body" {
				inBody = true
				continue
			}

			if inHead {
				if tagName == "link" {
					if isStylesheetLink(attrs) {
						preamble.Write(serializeTag(tagName, attrs, false))
					}
					continue
				}
				if tagName == "style" {
					preamble.Write(serializeTag(tagName, attrs, false))
					inStyle = true
					continue
				}
				if !voidElements[tagName] {
					skipUntilCloseTag(z, tagName, &line)
				}
				continue
			}

			if inBody {
				if depthInBody == 0 {
					blockStartLine = startLine
					blockBuf.Reset()
				}
				blockBuf.Write(serializeTag(tagName, rewriteAnchorAttrs(tagName, attrs, currentPath), false))
				if !voidElements[tagName] {
					depthInBody++
				} else if depthInBody == 0 {
					emitBlock(line)
				}
			}

		case html.SelfClosingTagToken:
			nameBytes, hasAttr := z.TagName()
			tagName := string(nameBytes)
			var attrs []rawAttr
			if hasAttr {
				attrs = readAttrs(z)
			}
			if inHead {
				if tagName == "link" && isStylesheetLink(attrs) {
					preamble.Write(serializeTag(tagName, attrs, true))
				}
				continue
			}
			if inBody {
				if depthInBody == 0 {
					blockStartLine = startLine
					blockBuf.Reset()
					blockBuf.Write(serializeTag(tagName, rewriteAnchorAttrs(tagName, attrs, currentPath), true))
					emitBlock(line)
				} else {
					blockBuf.Write(serializeTag(tagName, rewriteAnchorAttrs(tagName, attrs, currentPath), true))
				}
			}

		case html.EndTagToken:
			nameBytes, _ := z.TagName()
			tagName := string(nameBytes)

			if inHead && tagName == "style" {
				preamble.Write(raw)
				inStyle = false
				continue
			}
			if inHead && tagName == "head" {
				inHead = false
				continue
			}
			if inBody && tagName == "body" {
				if blockBuf.Len() > 0 {
					emitBlock(line)
				}
				inBody = false
				continue
			}

			if inBody && depthInBody >= 1 {
				blockBuf.Write(raw)
				depthInBody--
				if depthInBody == 0 {
					emitBlock(line)
				}
			}
		}
	}
}

// readAttrs drains the tokenizer's attribute cursor for the current
// start/self-closing tag. Returning a slice lets caller code inspect
// attrs (e.g. is-stylesheet) AND re-serialize from the same data, which
// z.Token() + z.TagAttr() can't both do (they consume the same cursor).
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

// isStylesheetLink reports whether the link element is a stylesheet —
// the only <link> relation that adds rendering content to the preview.
func isStylesheetLink(attrs []rawAttr) bool {
	for _, a := range attrs {
		if strings.ToLower(a.key) == "rel" &&
			strings.Contains(strings.ToLower(a.val), "stylesheet") {
			return true
		}
	}
	return false
}

// isURLAttr identifies attributes whose value is a URL — the targets
// for javascript: scheme stripping.
func isURLAttr(name string) bool {
	switch strings.ToLower(name) {
	case "href", "src", "action", "formaction", "xlink:href", "ping":
		return true
	}
	return false
}

// hasJavascriptScheme reports whether val starts with the javascript:
// scheme after leading-whitespace trim. Conservative match: case-
// insensitive, ignores Unicode-whitespace flavours (HTML5 leading
// whitespace covers ASCII only, so this matches the parser's view).
func hasJavascriptScheme(val string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(val)), "javascript:")
}

// skipUntilCloseTag consumes tokenizer events until the matching close
// tag for tagName, advancing line. Drops <script> content wholesale and
// skips non-styling head elements (title, meta, ...) without treating
// their bytes as part of a body block.
func skipUntilCloseTag(z *html.Tokenizer, tagName string, line *int) {
	depth := 1
	for depth > 0 {
		tt := z.Next()
		if tt == html.ErrorToken {
			return
		}
		raw := z.Raw()
		*line += bytes.Count(raw, []byte("\n"))
		switch tt {
		case html.StartTagToken:
			name, _ := z.TagName()
			if string(name) == tagName {
				depth++
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if string(name) == tagName {
				depth--
			}
		}
	}
}
