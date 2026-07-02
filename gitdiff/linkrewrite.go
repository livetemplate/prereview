package gitdiff

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// rewriteTagAttrs returns a copy of attrs with relative URLs rewritten for the
// repo-aware tags, when currentPath is non-empty:
//   - <a href>   → ResolveRelativeLink (in-SPA deep-link hash).
//   - <img src>  → resolveImageSrc (server-absolute static path).
//
// Other tags and other attributes pass through unchanged. The returned slice is
// independent of the input — caller can pass it straight to serializeTag.
func rewriteTagAttrs(tagName string, attrs []rawAttr, currentPath string) []rawAttr {
	if currentPath == "" {
		return attrs
	}
	switch strings.ToLower(tagName) {
	case "a":
		return rewriteAnchorAttrs(attrs, currentPath)
	case "img":
		return rewriteImageAttrs(attrs, currentPath)
	default:
		return attrs
	}
}

// rewriteAnchorAttrs rewrites an <a>'s href to an in-SPA deep-link hash.
func rewriteAnchorAttrs(attrs []rawAttr, currentPath string) []rawAttr {
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

// rewriteImageAttrs rewrites an <img>'s src server-absolute (resolveImageSrc),
// dropping a src that escapes the repo root so it can't request an outside
// file. Other attributes pass through.
func rewriteImageAttrs(attrs []rawAttr, currentPath string) []rawAttr {
	out := make([]rawAttr, 0, len(attrs))
	for _, a := range attrs {
		if strings.ToLower(a.key) == "src" {
			newVal, keep := resolveImageSrc(currentPath, a.val)
			if !keep {
				continue // src escapes the repo root — drop it
			}
			out = append(out, rawAttr{key: a.key, val: newVal})
			continue
		}
		out = append(out, a)
	}
	return out
}

// resolveImageSrc turns a repo-relative image src into a server-absolute path
// (`/dir/file.png`) so it loads from any directory — the static fallback
// (server.go) then serves it. This fixes both the re-admitted raw-HTML <img>
// (htmlimage.go) and plain Markdown-syntax `![](x.png)` images, which otherwise
// resolve browser-relative to the SPA root `/` and 404 from a subdirectory
// README. Mirrors previewBaseHref (htmlpreview.go), which gives the HTML
// preview the same any-directory resolution via <base>.
//
// External srcs — a URL scheme, protocol-relative `//host`, already server-
// absolute `/path`, or a `?query` we can't represent — pass through unchanged,
// so a Markdown-syntax remote image keeps working. keep=false signals a
// relative src that escapes the repo root via `../`; the caller drops it.
func resolveImageSrc(currentPath, src string) (val string, keep bool) {
	t := strings.TrimSpace(src)
	if t == "" || isExternalTarget(t) {
		return src, true
	}
	// Strip a fragment (rare on an image) before resolving the path part.
	path := t
	if h := strings.Index(path, "#"); h >= 0 {
		path = path[:h]
	}
	if path == "" {
		return src, true // fragment-only src — nothing to resolve
	}
	resolved := resolveRelativePath(currentPath, path)
	if resolved == "" {
		return "", false // escapes the repo root
	}
	return "/" + encodePath(resolved), true
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

// rewriteRelativeURLs walks a fragment of rendered HTML and rewrites the
// repo-aware relative URLs — `<a href>` (deep-link hash) and `<img src>`
// (server-absolute static path) — via rewriteTagAttrs. Used by the markdown
// renderer to post-process goldmark's output (goldmark's renderer is a
// package-level global, so per-call rewriting is cheaper here than installing a
// per-call NodeRendererFunc).
//
// Tolerant: malformed HTML round-trips through the tokenizer; an
// unparseable fragment returns unchanged (the markdown renderer
// always produces well-formed HTML in practice, so this is a
// defensive fallback for the renderProseLine escaped-text path).
func rewriteRelativeURLs(htmlFragment, currentPath string) string {
	if currentPath == "" ||
		(!strings.Contains(htmlFragment, "<a ") && !strings.Contains(htmlFragment, "<img")) {
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
			out.Write(serializeTag(tagName, rewriteTagAttrs(tagName, attrs, currentPath), selfClose))
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

// OpenExternalLinksInNewTab adds target="_blank" rel="noopener noreferrer" to
// every <a> whose href carries a URL scheme (http/https/mailto/…), so an
// external link opens in a new tab instead of navigating the current page
// away. Anchors that already declare a target, and in-page anchors (`#id`),
// are left untouched. It reuses the same tokenizer round-trip as
// rewriteRelativeURLs, so a fragment with no external anchors — or a malformed
// one — returns unchanged.
//
// Used by the standalone Usage page (RenderMarkdownDoc): that page is served
// on its own route, so a same-tab click on a doc link would drop the reader
// out of the app. The review-view renderer deliberately does NOT call this —
// its links deep-link within the SPA.
func OpenExternalLinksInNewTab(htmlFragment string) string {
	if !strings.Contains(htmlFragment, "<a ") {
		return htmlFragment
	}
	z := html.NewTokenizer(strings.NewReader(htmlFragment))
	var out bytes.Buffer
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt == html.StartTagToken {
			nameBytes, hasAttr := z.TagName()
			if string(nameBytes) == "a" && hasAttr {
				out.Write(serializeTag("a", externalAnchorAttrs(readAttrs(z)), false))
				continue
			}
		}
		out.Write(z.Raw())
	}
	if !looksWellFormed(out.String(), htmlFragment) {
		return htmlFragment
	}
	return out.String()
}

// externalAnchorAttrs returns attrs with target + rel added when the href is
// external and no target is already set; otherwise it returns attrs unchanged.
func externalAnchorAttrs(attrs []rawAttr) []rawAttr {
	var href string
	for _, a := range attrs {
		switch strings.ToLower(a.key) {
		case "target":
			return attrs // author-set target wins
		case "href":
			href = a.val
		}
	}
	if !hasURLScheme(href) {
		return attrs
	}
	return append(attrs,
		rawAttr{key: "target", val: "_blank"},
		rawAttr{key: "rel", val: "noopener noreferrer"},
	)
}
