package gitdiff

import (
	"strings"
	"testing"
)

func TestRenderHTMLPreview_HappyPath(t *testing.T) {
	src := []byte(`<!doctype html>
<html>
<head>
<title>Smoke</title>
<link rel="stylesheet" href="styles.css">
</head>
<body>
<h1 id="hero">Hello</h1>
<p>Paragraph here.</p>
</body>
</html>
`)
	doc, blocks := RenderHTMLPreview(src, "")
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}

	// The whole document is preserved (head stylesheet stays for the iframe).
	if !strings.Contains(doc, `id="hero"`) {
		t.Errorf("doc missing id=hero: %q", doc)
	}
	if !strings.Contains(doc, `rel="stylesheet"`) {
		t.Errorf("doc missing head stylesheet link: %q", doc)
	}
	if !strings.Contains(doc, `Paragraph here.`) {
		t.Errorf("doc missing paragraph: %q", doc)
	}
	// A <base> is injected for relative-asset resolution; root file → "/".
	if !strings.Contains(doc, `<base href="/">`) {
		t.Errorf("doc missing root <base>: %q", doc)
	}
	// Each top-level body child carries its source line range.
	if !strings.Contains(doc, `data-from="8" data-to="8"`) {
		t.Errorf("h1 block missing data-from/to: %q", doc)
	}
	if !strings.Contains(doc, `data-from="9" data-to="9"`) {
		t.Errorf("p block missing data-from/to: %q", doc)
	}

	// Source lines: <h1> is line 8, <p> is line 9.
	if blocks[0].StartLine != 8 || blocks[0].EndLine != 8 {
		t.Errorf("block 0 lines = (%d, %d), want (8, 8)", blocks[0].StartLine, blocks[0].EndLine)
	}
	if blocks[1].StartLine != 9 || blocks[1].EndLine != 9 {
		t.Errorf("block 1 lines = (%d, %d), want (9, 9)", blocks[1].StartLine, blocks[1].EndLine)
	}
}

func TestRenderHTMLPreview_BaseHrefForSubdir(t *testing.T) {
	src := []byte(`<html><head></head><body><h1>x</h1></body></html>`)
	doc, _ := RenderHTMLPreview(src, "docs/guide/index.html")
	if !strings.Contains(doc, `<base href="/docs/guide/">`) {
		t.Errorf("doc missing subdir <base>: %q", doc)
	}
}

// Scripts now PASS THROUGH — the page's own JS must run (that is the #63 fix:
// JS-generated CSS like the Tailwind Play CDN). The opaque-origin sandbox
// (sandbox="allow-scripts", no allow-same-origin; set in the template) is the
// execution boundary, not content stripping. A body-level <script> still must
// NOT become a commentable block (no visual content).
func TestRenderHTMLPreview_ScriptsPassThrough(t *testing.T) {
	src := []byte(`<html><body>
<h1>Title</h1>
<script>window.x=1</script>
<p>After script</p>
</body></html>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (h1, p) — script must not become a block", len(blocks))
	}
	if !strings.Contains(doc, "window.x=1") {
		t.Errorf("body <script> was stripped — it must pass through to run: %q", doc)
	}
	// The bridge beacon must be injected so the opaque iframe can post its
	// height + block rects out.
	if !strings.Contains(doc, "__lvtPreview") {
		t.Errorf("preview bridge beacon not injected: %q", doc)
	}
}

func TestRenderHTMLPreview_StripsEventHandlers(t *testing.T) {
	src := []byte(`<html><body>
<button onclick="bad()" id="btn">Click</button>
<img onerror="bad()" src="x.png">
</body></html>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if strings.Contains(strings.ToLower(doc), "onclick") {
		t.Errorf("onclick attr leaked: %q", doc)
	}
	if strings.Contains(strings.ToLower(doc), "onerror") {
		t.Errorf("onerror attr leaked: %q", doc)
	}
	// Non-handler attrs must remain.
	if !strings.Contains(doc, `id="btn"`) {
		t.Errorf("legit attr stripped: %q", doc)
	}
	if !strings.Contains(doc, `src="x.png"`) {
		t.Errorf("src attr stripped: %q", doc)
	}
}

func TestRenderHTMLPreview_StripsJavascriptURLs(t *testing.T) {
	src := []byte(`<html><body>
<a href="javascript:bad()">click</a>
<a href="/safe">safe</a>
</body></html>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks", len(blocks))
	}
	if strings.Contains(strings.ToLower(doc), "javascript:") {
		t.Errorf("javascript: URL not stripped: %q", doc)
	}
	if !strings.Contains(doc, `href="/safe"`) {
		t.Errorf("safe href stripped: %q", doc)
	}
}

func TestRenderHTMLPreview_HeadStylesPreserved(t *testing.T) {
	src := []byte(`<html>
<head>
<style>body{background:red}</style>
<link rel="stylesheet" href="styles.css">
<link rel="icon" href="favicon.png">
</head>
<body>
<h1>Title</h1>
</body>
</html>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	// The iframe renders the real document, so the entire head — inline
	// <style>, stylesheet AND non-stylesheet links — passes through.
	if !strings.Contains(doc, "<style") || !strings.Contains(doc, "background:red") {
		t.Errorf("inline <style> not preserved: %q", doc)
	}
	if !strings.Contains(doc, `rel="stylesheet"`) {
		t.Errorf("stylesheet link not preserved: %q", doc)
	}
	if !strings.Contains(doc, `rel="icon"`) {
		t.Errorf("favicon link not preserved: %q", doc)
	}
}

func TestRenderHTMLPreview_MultilineBlock(t *testing.T) {
	src := []byte(`<html><body>
<div>
  <p>nested 1</p>
  <p>nested 2</p>
</div>
</body></html>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (whole div)", len(blocks))
	}
	if blocks[0].StartLine != 2 || blocks[0].EndLine != 5 {
		t.Errorf("multiline block lines = (%d, %d), want (2, 5)",
			blocks[0].StartLine, blocks[0].EndLine)
	}
	if !strings.Contains(doc, "nested 1") || !strings.Contains(doc, "nested 2") {
		t.Errorf("nested content missing: %q", doc)
	}
	// The wrapping <div> (line 2) carries the range, not its children.
	if !strings.Contains(doc, `data-from="2" data-to="5"`) {
		t.Errorf("div block missing range data-from=2 data-to=5: %q", doc)
	}
}

func TestRenderHTMLPreview_VoidElementAtTopLevel(t *testing.T) {
	src := []byte(`<html><body>
<h1>Title</h1>
<hr>
<p>After</p>
</body></html>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (h1, hr, p)", len(blocks))
	}
	if blocks[1].StartLine != 3 || blocks[1].EndLine != 3 {
		t.Errorf("hr block lines = (%d, %d), want (3, 3)", blocks[1].StartLine, blocks[1].EndLine)
	}
	if !strings.Contains(doc, `<hr data-from="3" data-to="3">`) {
		t.Errorf("hr block missing or unstamped: %q", doc)
	}
}

func TestRenderHTMLPreview_EmptyInput(t *testing.T) {
	for _, src := range [][]byte{nil, {}, []byte("   \n   ")} {
		doc, blocks := RenderHTMLPreview(src, "")
		if doc != "" || blocks != nil {
			t.Errorf("RenderHTMLPreview(%q) = (%q, %v), want (\"\", nil)", src, doc, blocks)
		}
	}
}

func TestRenderHTMLPreview_HeadOnlyNoFlow(t *testing.T) {
	// A head-only document has no flow content, so there is nothing
	// commentable — the viewer falls back to the raw/line view.
	src := []byte(`<html><head><title>nothing</title></head></html>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if doc != "" || blocks != nil {
		t.Errorf("RenderHTMLPreview (head-only) = (%q, %v), want (\"\", nil)", doc, blocks)
	}
}

// TestRenderHTMLPreview_ImplicitBody covers issue #79: <head>/<body> are
// optional in HTML5, so a page that omits them must still render a preview.
func TestRenderHTMLPreview_ImplicitBody(t *testing.T) {
	// The exact repro from issue #79: doctype + <html>, a head <style>, and
	// an <h1> flow element — no <head> or <body> tag anywhere.
	src := []byte(`<!doctype html><html>
<style>body{background:rgb(0,128,0)}</style>
<h1>hi</h1>
</html>`)
	doc, blocks := RenderHTMLPreview(src, "index.html")
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (the <h1>): %q", len(blocks), doc)
	}
	// The <h1> is line 3; the head <style> is passthrough, not a block.
	if blocks[0].StartLine != 3 || blocks[0].EndLine != 3 {
		t.Errorf("h1 block lines = (%d, %d), want (3, 3)", blocks[0].StartLine, blocks[0].EndLine)
	}
	if !strings.Contains(doc, `<h1 data-from="3" data-to="3">hi</h1>`) {
		t.Errorf("h1 block missing/unstamped: %q", doc)
	}
	// The head payload (<base>) is still injected, once, and the page's own
	// <style> is preserved for the iframe.
	if !strings.Contains(doc, `<base href="/">`) {
		t.Errorf("doc missing injected <base>: %q", doc)
	}
	if strings.Count(doc, "<base ") != 1 {
		t.Errorf("want exactly one injected <base>, got %d: %q", strings.Count(doc, "<base "), doc)
	}
	if !strings.Contains(doc, `background:rgb(0,128,0)`) {
		t.Errorf("doc missing page <style>: %q", doc)
	}
}

// A document with no <html>/<head>/<body> at all — just head-metadata then
// flow — still opens the body implicitly.
func TestRenderHTMLPreview_ImplicitBodyNoHTMLTag(t *testing.T) {
	src := []byte(`<link rel="stylesheet" href="a.css">
<h1>one</h1>
<p>two</p>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (h1, p): %q", len(blocks), doc)
	}
	if blocks[0].StartLine != 2 || blocks[1].StartLine != 3 {
		t.Errorf("block lines = (%d, %d), want start (2, 3)", blocks[0].StartLine, blocks[1].StartLine)
	}
	// The <link> is head content (passthrough), not a commentable block.
	if !strings.Contains(doc, `rel="stylesheet"`) {
		t.Errorf("doc missing head <link>: %q", doc)
	}
}

// Explicit <head> but omitted <body> — the other half of the optional-tag
// rule — also opens the body implicitly after </head>.
func TestRenderHTMLPreview_ExplicitHeadImplicitBody(t *testing.T) {
	src := []byte(`<html><head><title>t</title></head>
<h1>hi</h1>
</html>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (the <h1>): %q", len(blocks), doc)
	}
	if blocks[0].StartLine != 2 {
		t.Errorf("h1 block start = %d, want 2", blocks[0].StartLine)
	}
}
