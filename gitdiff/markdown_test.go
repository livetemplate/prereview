package gitdiff

import (
	"strings"
	"testing"
)

func TestRenderMarkdownBlocks_LineRangesAndHTML(t *testing.T) {
	// 1: # Title
	// 2: (blank)
	// 3: first paragraph line
	// 4: continues here
	// 5: (blank)
	// 6: - item one
	// 7: - item two
	src := "# Title\n\nfirst paragraph line\ncontinues here\n\n- item one\n- item two\n"
	blocks := RenderMarkdownBlocks([]byte(src))
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (heading, paragraph, list)", len(blocks))
	}

	h, p, l := blocks[0], blocks[1], blocks[2]
	if !strings.Contains(string(h.HTML), "<h1") || !strings.Contains(string(h.HTML), "Title") {
		t.Errorf("heading HTML = %q, want an <h1>Title", h.HTML)
	}
	if h.StartLine != 1 || h.EndLine != 1 {
		t.Errorf("heading lines = %d-%d, want 1-1", h.StartLine, h.EndLine)
	}
	if !strings.Contains(string(p.HTML), "<p>") {
		t.Errorf("paragraph HTML = %q, want a <p>", p.HTML)
	}
	if p.StartLine != 3 || p.EndLine != 4 {
		t.Errorf("paragraph lines = %d-%d, want 3-4", p.StartLine, p.EndLine)
	}
	if !strings.Contains(string(l.HTML), "<li>") {
		t.Errorf("list HTML = %q, want <li>", l.HTML)
	}
	if l.StartLine != 6 || l.EndLine != 7 {
		t.Errorf("list lines = %d-%d, want 6-7", l.StartLine, l.EndLine)
	}
}

func TestRenderMarkdownBlocks_CodeFenceLineRange(t *testing.T) {
	// 1: para
	// 2: (blank)
	// 3: ```go
	// 4: x := 1
	// 5: ```
	src := "para\n\n```go\nx := 1\n```\n"
	blocks := RenderMarkdownBlocks([]byte(src))
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	code := blocks[1]
	if !strings.Contains(string(code.HTML), "<pre") || !strings.Contains(string(code.HTML), "x := 1") {
		t.Errorf("code block HTML = %q, want a <pre> with the code", code.HTML)
	}
	// The fenced content spans roughly lines 3..5; assert it covers the
	// code line and stays within the fence.
	if code.StartLine < 3 || code.StartLine > 4 {
		t.Errorf("code start line = %d, want 3-4", code.StartLine)
	}
	if code.EndLine < 4 || code.EndLine > 5 {
		t.Errorf("code end line = %d, want 4-5", code.EndLine)
	}
}

func TestRenderMarkdownBlocks_RawHTMLNotPassedThrough(t *testing.T) {
	src := "intro\n\n<script>alert('xss')</script>\n\n<img src=x onerror=alert(1)>\n"
	blocks := RenderMarkdownBlocks([]byte(src))
	joined := ""
	for _, b := range blocks {
		joined += string(b.HTML)
	}
	if strings.Contains(joined, "<script>") {
		t.Errorf("raw <script> must NOT be passed through; got: %q", joined)
	}
	if strings.Contains(joined, "onerror=alert") {
		t.Errorf("raw event-handler HTML must NOT be passed through; got: %q", joined)
	}
}

func TestRenderMarkdownBlocks_Empty(t *testing.T) {
	if RenderMarkdownBlocks(nil) != nil {
		t.Error("nil src should yield nil")
	}
	if RenderMarkdownBlocks([]byte("   \n\n")) != nil {
		t.Error("blank src should yield nil")
	}
}
