package gitdiff

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// rewriteAnchorAttrs returns a copy of attrs with the `href` rewritten
// via ResolveRelativeLink when tagName is "a" and currentPath is non-
// empty. Other tag names and other attribute names pass through
// unchanged. The returned slice is independent of the input — caller
// can pass it straight to serializeTag.
func rewriteAnchorAttrs(tagName string, attrs []rawAttr, currentPath string) []rawAttr {
	if currentPath == "" || strings.ToLower(tagName) != "a" {
		return attrs
	}
	// Footnote links (`#fn:1` / `#fnref:1`, emitted by extension.Footnote)
	// are intra-document anchors to ids that live in the SAME rendered page,
	// not repo-file links. Routing them through ResolveRelativeLink would
	// turn `#fn:1` into a cross-file deep-link hash and break footnote
	// navigation, so leave a footnote anchor's href untouched. goldmark tags
	// these with class="footnote-ref"/"footnote-backref".
	if isFootnoteAnchor(attrs) {
		return attrs
	}
	out := make([]rawAttr, len(attrs))
	for i, a := range attrs {
		if strings.ToLower(a.key) == "href" {
			newVal, _ := ResolveRelativeLink(currentPath, a.val)
			out[i] = rawAttr{key: a.key, val: newVal}
			continue
		}
		out[i] = a
	}
	return out
}

// isFootnoteAnchor reports whether attrs carry goldmark's footnote class
// ("footnote-ref" on a `[^1]` reference, "footnote-backref" on the ↩ link
// back from a definition). Matched as a whitespace-delimited token so a
// substring like "my-footnote-ref-x" doesn't falsely match.
func isFootnoteAnchor(attrs []rawAttr) bool {
	for _, a := range attrs {
		if strings.ToLower(a.key) != "class" {
			continue
		}
		for _, cls := range strings.Fields(a.val) {
			if cls == "footnote-ref" || cls == "footnote-backref" {
				return true
			}
		}
	}
	return false
}

// rewriteAnchorHrefs walks a fragment of rendered HTML and rewrites
// every `<a href="...">` value through ResolveRelativeLink. Used by
// the markdown renderer to post-process goldmark's output (goldmark's
// renderer is a package-level global, so per-call rewriting is
// cheaper here than installing a per-call NodeRendererFunc).
//
// Tolerant: malformed HTML round-trips through the tokenizer; an
// unparseable fragment returns unchanged (the markdown renderer
// always produces well-formed HTML in practice, so this is a
// defensive fallback for the renderProseLine escaped-text path).
func rewriteAnchorHrefs(htmlFragment, currentPath string) string {
	if currentPath == "" || !strings.Contains(htmlFragment, "<a ") {
		return htmlFragment
	}
	z := html.NewTokenizer(strings.NewReader(htmlFragment))
	var out bytes.Buffer
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			nameBytes, hasAttr := z.TagName()
			tagName := string(nameBytes)
			var attrs []rawAttr
			if hasAttr {
				attrs = readAttrs(z)
			}
			selfClose := tt == html.SelfClosingTagToken
			out.Write(serializeTag(tagName, rewriteAnchorAttrs(tagName, attrs, currentPath), selfClose))
		default:
			out.Write(z.Raw())
		}
	}
	// If tokenization stopped before EOF (a truly malformed fragment),
	// the partial output would lose bytes — fall back to the input.
	if !looksWellFormed(out.String(), htmlFragment) {
		return htmlFragment
	}
	return out.String()
}

// looksWellFormed is a cheap fidelity check on the tokenizer round-
// trip. The tokenizer re-emits everything byte-for-byte except where
// we deliberately rewrite, so a length drop is the only signal of a
// silent truncation. (Plain anchor-href rewrites can grow OR shrink
// the byte count — we compare the OTHER tags as a sanity check by
// counting the number of `<` tag starts.)
func looksWellFormed(out, in string) bool {
	return strings.Count(out, "<") == strings.Count(in, "<")
}
