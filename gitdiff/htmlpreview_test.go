package gitdiff

import (
	"strings"
	"testing"
)

func TestRenderHTMLBlocks_HappyPath(t *testing.T) {
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
	blocks := RenderHTMLBlocks(src)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}

	if !strings.Contains(string(blocks[0].HTML), `id="hero"`) {
		t.Errorf("block 0 missing id=hero: %q", blocks[0].HTML)
	}
	if !strings.Contains(string(blocks[0].HTML), `rel="stylesheet"`) {
		t.Errorf("block 0 missing preamble link: %q", blocks[0].HTML)
	}
	if !strings.Contains(string(blocks[1].HTML), `Paragraph here.`) {
		t.Errorf("block 1 missing paragraph: %q", blocks[1].HTML)
	}

	// Source lines: <h1> is line 8, <p> is line 9.
	if blocks[0].StartLine != 8 || blocks[0].EndLine != 8 {
		t.Errorf("block 0 lines = (%d, %d), want (8, 8)", blocks[0].StartLine, blocks[0].EndLine)
	}
	if blocks[1].StartLine != 9 || blocks[1].EndLine != 9 {
		t.Errorf("block 1 lines = (%d, %d), want (9, 9)", blocks[1].StartLine, blocks[1].EndLine)
	}
}

func TestRenderHTMLBlocks_StripsScripts(t *testing.T) {
	src := []byte(`<html><body>
<h1>Title</h1>
<script>alert("xss")</script>
<p>After script</p>
</body></html>`)
	blocks := RenderHTMLBlocks(src)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (h1, p) — script must be excluded entirely", len(blocks))
	}
	for _, b := range blocks {
		if strings.Contains(string(b.HTML), "alert") || strings.Contains(string(b.HTML), "<script") {
			t.Errorf("script content leaked into block HTML: %q", b.HTML)
		}
	}
}

func TestRenderHTMLBlocks_StripsEventHandlers(t *testing.T) {
	src := []byte(`<html><body>
<button onclick="bad()" id="btn">Click</button>
<img onerror="bad()" src="x.png">
</body></html>`)
	blocks := RenderHTMLBlocks(src)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	for _, b := range blocks {
		s := string(b.HTML)
		if strings.Contains(strings.ToLower(s), "onclick") {
			t.Errorf("onclick attr leaked: %q", s)
		}
		if strings.Contains(strings.ToLower(s), "onerror") {
			t.Errorf("onerror attr leaked: %q", s)
		}
	}
	// Non-handler attrs must remain.
	if !strings.Contains(string(blocks[0].HTML), `id="btn"`) {
		t.Errorf("legit attr stripped: %q", blocks[0].HTML)
	}
	if !strings.Contains(string(blocks[1].HTML), `src="x.png"`) {
		t.Errorf("src attr stripped: %q", blocks[1].HTML)
	}
}

func TestRenderHTMLBlocks_StripsJavascriptURLs(t *testing.T) {
	src := []byte(`<html><body>
<a href="javascript:bad()">click</a>
<a href="/safe">safe</a>
</body></html>`)
	blocks := RenderHTMLBlocks(src)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks", len(blocks))
	}
	if strings.Contains(strings.ToLower(string(blocks[0].HTML)), "javascript:") {
		t.Errorf("javascript: URL not stripped: %q", blocks[0].HTML)
	}
	if !strings.Contains(string(blocks[1].HTML), `href="/safe"`) {
		t.Errorf("safe href stripped: %q", blocks[1].HTML)
	}
}

func TestRenderHTMLBlocks_PreambleEmbedded(t *testing.T) {
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
	blocks := RenderHTMLBlocks(src)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	html := string(blocks[0].HTML)
	if !strings.Contains(html, "<style") || !strings.Contains(html, "background:red") {
		t.Errorf("inline <style> not embedded in block preamble: %q", html)
	}
	if !strings.Contains(html, `rel="stylesheet"`) {
		t.Errorf("stylesheet link not embedded: %q", html)
	}
	// Non-stylesheet links must NOT pollute the preamble.
	if strings.Contains(html, `rel="icon"`) {
		t.Errorf("non-stylesheet link leaked into preamble: %q", html)
	}
}

func TestRenderHTMLBlocks_MultilineBlock(t *testing.T) {
	src := []byte(`<html><body>
<div>
  <p>nested 1</p>
  <p>nested 2</p>
</div>
</body></html>`)
	blocks := RenderHTMLBlocks(src)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (whole div)", len(blocks))
	}
	if blocks[0].StartLine != 2 || blocks[0].EndLine != 5 {
		t.Errorf("multiline block lines = (%d, %d), want (2, 5)",
			blocks[0].StartLine, blocks[0].EndLine)
	}
	html := string(blocks[0].HTML)
	if !strings.Contains(html, "nested 1") || !strings.Contains(html, "nested 2") {
		t.Errorf("nested content missing: %q", html)
	}
}

func TestRenderHTMLBlocks_VoidElementAtTopLevel(t *testing.T) {
	src := []byte(`<html><body>
<h1>Title</h1>
<hr>
<p>After</p>
</body></html>`)
	blocks := RenderHTMLBlocks(src)
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (h1, hr, p)", len(blocks))
	}
	if !strings.Contains(string(blocks[1].HTML), "<hr") {
		t.Errorf("hr block missing: %q", blocks[1].HTML)
	}
}

func TestRenderHTMLBlocks_EmptyInput(t *testing.T) {
	for _, src := range [][]byte{nil, {}, []byte("   \n   ")} {
		if got := RenderHTMLBlocks(src); got != nil {
			t.Errorf("RenderHTMLBlocks(%q) = %v, want nil", src, got)
		}
	}
}

func TestRenderHTMLBlocks_NoBody(t *testing.T) {
	src := []byte(`<html><head><title>nothing</title></head></html>`)
	if got := RenderHTMLBlocks(src); got != nil {
		t.Errorf("RenderHTMLBlocks (no body) = %v, want nil", got)
	}
}
