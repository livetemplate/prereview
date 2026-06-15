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

func TestRenderHTMLPreview_StripsScripts(t *testing.T) {
	src := []byte(`<html><body>
<h1>Title</h1>
<script>alert("xss")</script>
<p>After script</p>
</body></html>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (h1, p) — script must not become a block", len(blocks))
	}
	if strings.Contains(doc, "alert") || strings.Contains(doc, "<script") {
		t.Errorf("script content leaked into doc: %q", doc)
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

func TestRenderHTMLPreview_NoBody(t *testing.T) {
	src := []byte(`<html><head><title>nothing</title></head></html>`)
	doc, blocks := RenderHTMLPreview(src, "")
	if doc != "" || blocks != nil {
		t.Errorf("RenderHTMLPreview (no body) = (%q, %v), want (\"\", nil)", doc, blocks)
	}
}
