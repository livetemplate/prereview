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

// Anchor-href rewriting in the HTML preview was retired with the move to
// the sandboxed iframe (in-iframe links are inert; <base> handles relative
// resolution). The rewriteAnchorHrefs tests above still cover the markdown
// renderer, which is the only remaining consumer.
