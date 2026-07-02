package gitdiff

import (
	"strings"
	"testing"
)

func TestRewriteRelativeURLs_MarkdownLinkBecomesHash(t *testing.T) {
	in := `<p>See <a href="OTHER.md">other</a> for details.</p>`
	out := rewriteRelativeURLs(in, "README.md")
	if !strings.Contains(out, `href="#OTHER.md"`) {
		t.Errorf("got %q, want href rewritten to #OTHER.md", out)
	}
}

func TestRewriteRelativeURLs_IntraDocAnchor(t *testing.T) {
	in := `<p>Jump to <a href="#architecture">arch</a>.</p>`
	out := rewriteRelativeURLs(in, "README.md")
	if !strings.Contains(out, `href="#README.md:h-architecture"`) {
		t.Errorf("got %q, want intra-doc anchor rewrite", out)
	}
}

func TestRewriteRelativeURLs_ExternalUnchanged(t *testing.T) {
	in := `<p><a href="https://example.com/x">ext</a></p>`
	out := rewriteRelativeURLs(in, "README.md")
	if !strings.Contains(out, `href="https://example.com/x"`) {
		t.Errorf("got %q, external URL should pass through", out)
	}
}

func TestRewriteRelativeURLs_EmptyPath_NoRewrite(t *testing.T) {
	in := `<p><a href="OTHER.md">x</a></p>`
	out := rewriteRelativeURLs(in, "")
	if out != in {
		t.Errorf("got %q, want unchanged when currentPath is empty", out)
	}
}

func TestRewriteRelativeURLs_NoAnchorTags_FastPath(t *testing.T) {
	in := `<p>no links here</p>`
	out := rewriteRelativeURLs(in, "README.md")
	if out != in {
		t.Errorf("got %q, want unchanged", out)
	}
}

func TestRewriteTagAttrs_PreservesOtherAttrs(t *testing.T) {
	attrs := []rawAttr{
		{key: "href", val: "OTHER.md"},
		{key: "class", val: "btn"},
		{key: "title", val: "click me"},
	}
	out := rewriteTagAttrs("a", attrs, "README.md")
	if out[0].val != "#OTHER.md" {
		t.Errorf("href = %q, want #OTHER.md", out[0].val)
	}
	if out[1].val != "btn" || out[2].val != "click me" {
		t.Errorf("non-href attrs mutated: %+v", out)
	}
}

func TestRewriteTagAttrs_NotAnchor_NoOp(t *testing.T) {
	attrs := []rawAttr{{key: "href", val: "OTHER.md"}}
	out := rewriteTagAttrs("link", attrs, "README.md")
	if out[0].val != "OTHER.md" {
		t.Errorf("non-<a> href was rewritten: %+v", out)
	}
}

func TestRenderMarkdownBlocks_LinkRewritingFlow(t *testing.T) {
	src := "Click [other](OTHER.md) or jump to [hero](#hero).\n"
	blocks := RenderMarkdownBlocks([]byte(src), "docs/README.md")
	joined := ""
	for _, b := range blocks {
		joined += string(b.HTML)
	}
	if !strings.Contains(joined, `href="#docs/OTHER.md"`) {
		t.Errorf("markdown link not rewritten: %q", joined)
	}
	if !strings.Contains(joined, `href="#docs/README.md:h-hero"`) {
		t.Errorf("intra-doc anchor not rewritten: %q", joined)
	}
}

// Anchor-href rewriting in the HTML preview was retired with the move to
// the sandboxed iframe (in-iframe links are inert; <base> handles relative
// resolution). The rewriteRelativeURLs tests above still cover the markdown
// renderer, which is the only remaining consumer.

// resolveImageSrc turns a relative <img> src server-absolute so it loads from
// any directory — the fix for #49's subdir-README case, applied to both
// raw-HTML and Markdown-syntax images.
func TestResolveImageSrc(t *testing.T) {
	for _, tc := range []struct {
		name        string
		currentPath string
		src         string
		wantVal     string
		wantKeep    bool
	}{
		{"root sibling", "README.md", "hero.gif", "/hero.gif", true},
		{"subdir sibling", "docs/README.md", "hero.gif", "/docs/hero.gif", true},
		{"subdir nested", "docs/README.md", "img/x.png", "/docs/img/x.png", true},
		{"dot-slash", "docs/README.md", "./x.png", "/docs/x.png", true},
		{"up one valid", "docs/guide/README.md", "../x.png", "/docs/x.png", true},
		{"space encoded", "README.md", "my image.png", "/my%20image.png", true},
		{"http passthrough", "docs/README.md", "https://ex.com/x.png", "https://ex.com/x.png", true},
		{"protocol-relative passthrough", "docs/README.md", "//ex.com/x.png", "//ex.com/x.png", true},
		{"server-absolute passthrough", "docs/README.md", "/already/abs.png", "/already/abs.png", true},
		{"data passthrough", "README.md", "data:image/gif;base64,AAAA", "data:image/gif;base64,AAAA", true},
		{"query passthrough", "docs/README.md", "x.png?v=2", "x.png?v=2", true},
		{"escape root dropped", "README.md", "../../etc/passwd", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			val, keep := resolveImageSrc(tc.currentPath, tc.src)
			if keep != tc.wantKeep || (keep && val != tc.wantVal) {
				t.Errorf("resolveImageSrc(%q, %q) = (%q, %v), want (%q, %v)",
					tc.currentPath, tc.src, val, keep, tc.wantVal, tc.wantKeep)
			}
		})
	}
}

// An <img src> in a rendered fragment is resolved server-absolute; an escaping
// src has its attribute dropped entirely (no traversal request reaches the
// static fallback).
func TestRewriteRelativeURLs_ImageSrc(t *testing.T) {
	got := rewriteRelativeURLs(`<p><img src="hero.gif" alt="x"></p>`, "docs/README.md")
	if !strings.Contains(got, `src="/docs/hero.gif"`) {
		t.Errorf("img src not resolved server-absolute: %q", got)
	}
	if !strings.Contains(got, `alt="x"`) {
		t.Errorf("alt attr lost: %q", got)
	}

	escaped := rewriteRelativeURLs(`<img src="../../secret.png">`, "README.md")
	if strings.Contains(escaped, "secret.png") {
		t.Errorf("escaping img src not dropped: %q", escaped)
	}

	// Fast path: a fragment with neither <a nor <img is returned verbatim.
	plain := `<p>nothing to rewrite</p>`
	if rewriteRelativeURLs(plain, "README.md") != plain {
		t.Errorf("fast-path fragment was modified")
	}
}

func TestOpenExternalLinksInNewTab(t *testing.T) {
	cases := []struct {
		name, in string
		want     string // substring that must be present
	}{
		{"external gets target+rel",
			`<p><a href="https://github.com/x/y">docs</a></p>`,
			`<a href="https://github.com/x/y" target="_blank" rel="noopener noreferrer">`},
		{"mailto is external too",
			`<a href="mailto:a@b.com">mail</a>`,
			`target="_blank"`},
		{"in-page anchor untouched",
			`<a href="#section">jump</a>`,
			`<a href="#section">`},
		{"author target preserved",
			`<a href="https://x.com" target="_self">x</a>`,
			`target="_self"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := OpenExternalLinksInNewTab(c.in)
			if !strings.Contains(got, c.want) {
				t.Errorf("in=%q got=%q, want substring %q", c.in, got, c.want)
			}
		})
	}

	// In-page anchor must NOT gain a target.
	if strings.Contains(OpenExternalLinksInNewTab(`<a href="#x">j</a>`), "target=") {
		t.Error("in-page anchor should not open in a new tab")
	}
	// Author-set target must not be doubled.
	if got := OpenExternalLinksInNewTab(`<a href="https://x.com" target="_self">x</a>`); strings.Contains(got, "_blank") {
		t.Errorf("author target overridden: %q", got)
	}
	// No anchors → fast-path unchanged.
	if in := `<p>plain</p>`; OpenExternalLinksInNewTab(in) != in {
		t.Error("fragment with no anchors should be unchanged")
	}
}
