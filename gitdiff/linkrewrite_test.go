package gitdiff

import (
	"strings"
	"testing"
)

func TestRewriteAnchorHrefs_MarkdownLinkBecomesHash(t *testing.T) {
	in := `<p>See <a href="OTHER.md">other</a> for details.</p>`
	out := rewriteAnchorHrefs(in, "README.md")
	if !strings.Contains(out, `href="#OTHER.md"`) {
		t.Errorf("got %q, want href rewritten to #OTHER.md", out)
	}
}

func TestRewriteAnchorHrefs_IntraDocAnchor(t *testing.T) {
	in := `<p>Jump to <a href="#architecture">arch</a>.</p>`
	out := rewriteAnchorHrefs(in, "README.md")
	if !strings.Contains(out, `href="#README.md:h-architecture"`) {
		t.Errorf("got %q, want intra-doc anchor rewrite", out)
	}
}

func TestRewriteAnchorHrefs_ExternalUnchanged(t *testing.T) {
	in := `<p><a href="https://example.com/x">ext</a></p>`
	out := rewriteAnchorHrefs(in, "README.md")
	if !strings.Contains(out, `href="https://example.com/x"`) {
		t.Errorf("got %q, external URL should pass through", out)
	}
}

func TestRewriteAnchorHrefs_EmptyPath_NoRewrite(t *testing.T) {
	in := `<p><a href="OTHER.md">x</a></p>`
	out := rewriteAnchorHrefs(in, "")
	if out != in {
		t.Errorf("got %q, want unchanged when currentPath is empty", out)
	}
}

func TestRewriteAnchorHrefs_NoAnchorTags_FastPath(t *testing.T) {
	in := `<p>no links here</p>`
	out := rewriteAnchorHrefs(in, "README.md")
	if out != in {
		t.Errorf("got %q, want unchanged", out)
	}
}

func TestRewriteAnchorAttrs_PreservesOtherAttrs(t *testing.T) {
	attrs := []rawAttr{
		{key: "href", val: "OTHER.md"},
		{key: "class", val: "btn"},
		{key: "title", val: "click me"},
	}
	out := rewriteAnchorAttrs("a", attrs, "README.md")
	if out[0].val != "#OTHER.md" {
		t.Errorf("href = %q, want #OTHER.md", out[0].val)
	}
	if out[1].val != "btn" || out[2].val != "click me" {
		t.Errorf("non-href attrs mutated: %+v", out)
	}
}

func TestRewriteAnchorAttrs_NotAnchor_NoOp(t *testing.T) {
	attrs := []rawAttr{{key: "href", val: "OTHER.md"}}
	out := rewriteAnchorAttrs("link", attrs, "README.md")
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

func TestRenderHTMLBlocks_LinkRewritingFlow(t *testing.T) {
	src := []byte(`<html><body>
<a href="other.html">other</a>
<a href="#section">section</a>
<a href="https://ext.com">ext</a>
</body></html>`)
	blocks := RenderHTMLBlocks(src, "docs/index.html")
	if len(blocks) == 0 {
		t.Fatal("got no blocks")
	}
	joined := ""
	for _, b := range blocks {
		joined += string(b.HTML)
	}
	if !strings.Contains(joined, `href="#docs/other.html"`) {
		t.Errorf("html link not rewritten: %q", joined)
	}
	if !strings.Contains(joined, `href="#docs/index.html:h-section"`) {
		t.Errorf("html intra-doc anchor not rewritten: %q", joined)
	}
	if !strings.Contains(joined, `href="https://ext.com"`) {
		t.Errorf("external href changed: %q", joined)
	}
}

func TestExtractHTMLAnchorIDs(t *testing.T) {
	src := []byte(`<html><body>
<h1 id="hero">Hello</h1>
<section id="features"><p>stuff</p></section>
<div><span id="cta">Click</span></div>
</body></html>`)
	blocks := RenderHTMLBlocks(src, "doc.html")
	ids := ExtractHTMLAnchorIDs(blocks)
	if ids == nil {
		t.Fatal("expected non-nil map")
	}
	// hero is block 0, features is block 1 (with cta nested below)
	if ids["hero"] != 0 {
		t.Errorf("hero index = %d, want 0", ids["hero"])
	}
	if ids["features"] != 1 {
		t.Errorf("features index = %d, want 1", ids["features"])
	}
	// cta is inside block 2 (the <div>), not features
	if _, ok := ids["cta"]; !ok {
		t.Error("cta id not extracted")
	}
}

func TestExtractHTMLAnchorIDs_DuplicateIDsKeepFirst(t *testing.T) {
	src := []byte(`<html><body>
<h1 id="dup">first</h1>
<h2 id="dup">second</h2>
</body></html>`)
	blocks := RenderHTMLBlocks(src, "doc.html")
	ids := ExtractHTMLAnchorIDs(blocks)
	if ids["dup"] != 0 {
		t.Errorf("duplicate id should map to FIRST occurrence (block 0), got %d", ids["dup"])
	}
}

func TestExtractHTMLAnchorIDs_NoIDs_ReturnsNil(t *testing.T) {
	src := []byte(`<html><body><p>no ids here</p></body></html>`)
	blocks := RenderHTMLBlocks(src, "doc.html")
	if got := ExtractHTMLAnchorIDs(blocks); got != nil {
		t.Errorf("expected nil for no-id input, got %+v", got)
	}
}
