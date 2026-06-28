package gitdiff

import (
	"strings"
	"testing"
)

// TestRenderMarkdownBlocks_Mermaid pins that a ```mermaid fence becomes one
// commentable block carrying the diagram definition for client rendering:
// the lvt-ignore container (so morphdom won't clobber the injected SVG), the
// pre.mermaid the client scans for, the definition preserved verbatim, and a
// source-line span the comment overlay can anchor to.
func TestRenderMarkdownBlocks_Mermaid(t *testing.T) {
	// 1: ```mermaid
	// 2: graph TD
	// 3:   A-->B
	// 4: ```
	src := "```mermaid\ngraph TD\n  A-->B\n```\n"
	blocks := RenderMarkdownBlocks([]byte(src), "")
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (one mermaid fence); blocks=%+v", len(blocks), blocks)
	}
	h := string(blocks[0].HTML)
	if !strings.Contains(h, `class="md-mermaid"`) {
		t.Errorf("mermaid HTML = %q, want a .md-mermaid container", h)
	}
	if !strings.Contains(h, "lvt-ignore") {
		t.Errorf("mermaid HTML = %q, want lvt-ignore so morphdom skips the injected SVG", h)
	}
	if !strings.Contains(h, `<pre class="mermaid">`) {
		t.Errorf("mermaid HTML = %q, want a pre.mermaid the client renders", h)
	}
	// The definition must survive verbatim (the arrow `-->` HTML-escapes to
	// `--&gt;`; the browser decodes it back for textContent).
	if !strings.Contains(h, "graph TD") || !strings.Contains(h, "A--&gt;B") {
		t.Errorf("mermaid HTML = %q, missing/garbled diagram definition", h)
	}
	// Not syntax-highlighted: a mermaid fence must NOT route through chroma.
	if strings.Contains(h, `<span style="color:`) {
		t.Errorf("mermaid HTML = %q, should not be chroma-highlighted", h)
	}
	// Like every code fence, the diagram anchors to its content lines (the
	// definition), not the ``` delimiters — so a comment lands on the lines
	// the reviewer actually sees. Fence is lines 1-4; content is 2-3.
	if blocks[0].StartLine < 1 || blocks[0].StartLine > 2 {
		t.Errorf("mermaid start line = %d, want 1-2 (within the fence)", blocks[0].StartLine)
	}
	if blocks[0].EndLine < blocks[0].StartLine || blocks[0].EndLine > 4 {
		t.Errorf("mermaid end line = %d, want within the fence (≤4)", blocks[0].EndLine)
	}
}

// TestRenderMarkdownBlocks_MermaidEscapesHTML pins the safe-default posture:
// markup inside a mermaid definition (untrusted repo content) is escaped to
// literal text, never passed through as live HTML.
func TestRenderMarkdownBlocks_MermaidEscapesHTML(t *testing.T) {
	src := "```mermaid\ngraph TD\n  A[\"<script>alert(1)</script>\"]\n```\n"
	blocks := RenderMarkdownBlocks([]byte(src), "")
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1; blocks=%+v", len(blocks), blocks)
	}
	h := string(blocks[0].HTML)
	if strings.Contains(h, "<script>") {
		t.Errorf("mermaid HTML = %q, leaked a live <script> tag", h)
	}
	if !strings.Contains(h, "&lt;script&gt;") {
		t.Errorf("mermaid HTML = %q, want the script tag escaped to literal text", h)
	}
}

// TestRenderMarkdownBlocks_MermaidCaseAndInfoString pins that the fence is
// recognised case-insensitively and ignores extra info-string words, matching
// GitHub. Each still yields a single rendered diagram block.
func TestRenderMarkdownBlocks_MermaidCaseAndInfoString(t *testing.T) {
	for _, info := range []string{"Mermaid", "MERMAID", "mermaid theme=dark"} {
		src := "```" + info + "\ngraph TD\n  A-->B\n```\n"
		blocks := RenderMarkdownBlocks([]byte(src), "")
		if len(blocks) != 1 {
			t.Fatalf("info %q: got %d blocks, want 1", info, len(blocks))
		}
		if !strings.Contains(string(blocks[0].HTML), `<pre class="mermaid">`) {
			t.Errorf("info %q: HTML = %q, want a rendered mermaid block", info, blocks[0].HTML)
		}
	}
}

// TestRenderMarkdownBlocks_NonMermaidFenceStillHighlights guards the
// regression risk of the new switch case: an ordinary ```go fence must keep
// routing through chroma syntax highlighting, untouched by mermaid handling.
func TestRenderMarkdownBlocks_NonMermaidFenceStillHighlights(t *testing.T) {
	src := "```go\nfunc main() {}\n```\n"
	blocks := RenderMarkdownBlocks([]byte(src), "")
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1; blocks=%+v", len(blocks), blocks)
	}
	h := string(blocks[0].HTML)
	if strings.Contains(h, "md-mermaid") {
		t.Errorf("go fence misrouted to mermaid: %q", h)
	}
	if !strings.Contains(h, `class="chroma"`) || !strings.Contains(h, `<span class="`) {
		t.Errorf("go fence not chroma-highlighted: %q", h)
	}
}
