package gitdiff

import (
	"slices"
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

// TestRenderMarkdownBlocks_MermaidKeys pins the data-key contract: identical
// diagrams get distinct keys (morphdom indexes keys across the patch root, so a
// shared key leaves a stale copy behind when one is edited), an unrelated line
// moving leaves every key alone, and editing a definition changes its key.
func TestRenderMarkdownBlocks_MermaidKeys(t *testing.T) {
	fence := "```mermaid\ngraph TD\n  A-->B\n```\n"
	keys := func(src, path string) []string {
		var out []string
		for _, b := range RenderMarkdownBlocks([]byte(src), path) {
			h := string(b.HTML)
			if i := strings.Index(h, `data-key="`); i >= 0 {
				out = append(out, strings.SplitN(h[i+len(`data-key="`):], `"`, 2)[0])
			}
		}
		return out
	}

	twin := keys(fence+"\n"+fence, "a.md")
	if len(twin) != 2 || twin[0] == twin[1] {
		t.Fatalf("identical diagrams need distinct keys, got %v", twin)
	}
	if shifted := keys("# Title\n\nIntro.\n\n"+fence+"\n"+fence, "a.md"); !slices.Equal(shifted, twin) {
		t.Errorf("an unrelated line moved the keys: %v, want %v", shifted, twin)
	}
	if other := keys(fence, "b.md"); other[0] == twin[0] {
		t.Errorf("the same diagram in another file shares key %s", other[0])
	}
	edited := keys(strings.Replace(fence, "A-->B", "A-->C", 1)+"\n"+fence, "a.md")
	if edited[0] == twin[0] {
		t.Errorf("editing a definition kept its key %s", edited[0])
	}
}
