package gitdiff

import (
	"strings"
	"testing"
)

func TestRenderMarkdownBlocks_LineRangesAndHTML(t *testing.T) {
	// 1: # Title
	// 2: (blank)
	// 3: first paragraph line.   (one sentence per line → splits per line)
	// 4: continues here.
	// 5: (blank)
	// 6: - item one
	// 7: - item two
	src := "# Title\n\nfirst paragraph line.\ncontinues here.\n\n- item one\n- item two\n"
	blocks := RenderMarkdownBlocks([]byte(src))
	// heading(1) · prose line(3) · prose line(4) · item(6) · item(7):
	// the 2-line paragraph splits per source line and the 2-item list
	// splits per item, so every line is independently commentable.
	if len(blocks) != 5 {
		t.Fatalf("got %d blocks, want 5 (heading, 2 prose lines, 2 items); blocks=%+v", len(blocks), blocks)
	}

	h, p1, p2, l1, l2 := blocks[0], blocks[1], blocks[2], blocks[3], blocks[4]
	if !strings.Contains(string(h.HTML), "<h1") || !strings.Contains(string(h.HTML), "Title") {
		t.Errorf("heading HTML = %q, want an <h1>Title", h.HTML)
	}
	if h.StartLine != 1 || h.EndLine != 1 {
		t.Errorf("heading lines = %d-%d, want 1-1", h.StartLine, h.EndLine)
	}
	if !strings.Contains(string(p1.HTML), "<p>") || !strings.Contains(string(p1.HTML), "first paragraph line") {
		t.Errorf("prose line 1 HTML = %q, want <p>first paragraph line", p1.HTML)
	}
	if p1.StartLine != 3 || p1.EndLine != 3 {
		t.Errorf("prose line 1 = %d-%d, want 3-3 (paragraph split per source line)", p1.StartLine, p1.EndLine)
	}
	if !strings.Contains(string(p2.HTML), "continues here") {
		t.Errorf("prose line 2 HTML = %q, want 'continues here'", p2.HTML)
	}
	if p2.StartLine != 4 || p2.EndLine != 4 {
		t.Errorf("prose line 2 = %d-%d, want 4-4", p2.StartLine, p2.EndLine)
	}
	if !strings.Contains(string(l1.HTML), "<li>") || !strings.Contains(string(l1.HTML), "item one") {
		t.Errorf("list item 1 HTML = %q, want <li>item one", l1.HTML)
	}
	if l1.StartLine != 6 || l1.EndLine != 6 {
		t.Errorf("list item 1 lines = %d-%d, want 6-6", l1.StartLine, l1.EndLine)
	}
	if !strings.Contains(string(l2.HTML), "item two") {
		t.Errorf("list item 2 HTML = %q, want item two", l2.HTML)
	}
	if l2.StartLine != 7 || l2.EndLine != 7 {
		t.Errorf("list item 2 lines = %d-%d, want 7-7", l2.StartLine, l2.EndLine)
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

// TestRenderMarkdownBlocks_GFMTable pins the per-row contract: a GFM
// table is descended one level so the header row and EACH body row are
// independently-commentable blocks anchored to their own source line
// (the whole point — comment on use-case row D, not the whole table).
// It also pins that the pipe syntax never leaks as literal text and
// that the trailing paragraph is not swallowed.
func TestRenderMarkdownBlocks_GFMTable(t *testing.T) {
	// 1: before
	// 2: (blank)
	// 3: | Col A | Col B |
	// 4: |-------|-------|
	// 5: | a1 | b1 |
	// 6: | a2 | b2 |
	// 7: (blank)
	// 8: after
	src := "before\n\n| Col A | Col B |\n|-------|-------|\n| a1 | b1 |\n| a2 | b2 |\n\nafter\n"
	blocks := RenderMarkdownBlocks([]byte(src))
	// before(1) · header(3) · row(5) · row(6) · after(8)
	if len(blocks) != 5 {
		t.Fatalf("got %d blocks, want 5 (before, header, row, row, after); blocks=%+v", len(blocks), blocks)
	}

	hdr, r1, r2, after := blocks[1], blocks[2], blocks[3], blocks[4]
	if !strings.Contains(string(hdr.HTML), "<th") || !strings.Contains(string(hdr.HTML), "Col A") || !strings.Contains(string(hdr.HTML), "Col B") {
		t.Errorf("header block HTML = %q, want <th>Col A/Col B", hdr.HTML)
	}
	if hdr.StartLine != 3 || hdr.EndLine != 3 {
		t.Errorf("header lines = %d-%d, want 3-3", hdr.StartLine, hdr.EndLine)
	}
	for _, row := range []struct {
		b              MarkdownBlock
		a, bcell, want string
		line           int
	}{
		{r1, "a1", "b1", "row 1", 5},
		{r2, "a2", "b2", "row 2", 6},
	} {
		if !strings.Contains(string(row.b.HTML), "<td") || !strings.Contains(string(row.b.HTML), row.a) || !strings.Contains(string(row.b.HTML), row.bcell) {
			t.Errorf("%s HTML = %q, want <td>%s/%s", row.want, row.b.HTML, row.a, row.bcell)
		}
		if !strings.Contains(string(row.b.HTML), `class="md-solo-table"`) {
			t.Errorf("%s HTML = %q, want a wrapping <table class=\"md-solo-table\">", row.want, row.b.HTML)
		}
		if row.b.StartLine != row.line || row.b.EndLine != row.line {
			t.Errorf("%s lines = %d-%d, want %d-%d (single source line)", row.want, row.b.StartLine, row.b.EndLine, row.line, row.line)
		}
	}

	for _, b := range blocks {
		if strings.Contains(string(b.HTML), "|---") || strings.Contains(string(b.HTML), "| Col A |") {
			t.Errorf("raw pipe table syntax leaked into HTML: %q", b.HTML)
		}
	}
	if after.StartLine != 8 {
		t.Errorf("trailing paragraph StartLine = %d, want 8 (table did not bleed)", after.StartLine)
	}
}

// TestRenderMarkdownBlocks_ListPerItem pins that each list item is its
// own commentable block with its own source line, numbering preserved.
func TestRenderMarkdownBlocks_ListPerItem(t *testing.T) {
	// 1: 1. first
	// 2: 2. second
	// 3: 3. third
	src := "1. first\n2. second\n3. third\n"
	blocks := RenderMarkdownBlocks([]byte(src))
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (one per ordered item); blocks=%+v", len(blocks), blocks)
	}
	for i, want := range []struct {
		text  string
		start string
		line  int
	}{
		{"first", `<ol start="1">`, 1},
		{"second", `<ol start="2">`, 2},
		{"third", `<ol start="3">`, 3},
	} {
		b := blocks[i]
		if !strings.Contains(string(b.HTML), "<li>") || !strings.Contains(string(b.HTML), want.text) {
			t.Errorf("item %d HTML = %q, want <li>%s", i+1, b.HTML, want.text)
		}
		if !strings.Contains(string(b.HTML), want.start) {
			t.Errorf("item %d HTML = %q, want ordered wrapper %s (numbering preserved)", i+1, b.HTML, want.start)
		}
		if b.StartLine != want.line || b.EndLine != want.line {
			t.Errorf("item %d lines = %d-%d, want %d-%d", i+1, b.StartLine, b.EndLine, want.line, want.line)
		}
	}

	// Unordered list → each item wrapped in <ul>, one block per item.
	ub := RenderMarkdownBlocks([]byte("- a\n- b\n"))
	if len(ub) != 2 {
		t.Fatalf("unordered: got %d blocks, want 2", len(ub))
	}
	for i, b := range ub {
		if !strings.Contains(string(b.HTML), "<ul>") || !strings.Contains(string(b.HTML), "<li>") {
			t.Errorf("unordered item %d HTML = %q, want <ul><li>", i+1, b.HTML)
		}
	}
}

// TestRenderMarkdownBlocks_TablePerRow pins the header + per-body-row
// split for a 3-row table and that every wrapped row is a valid,
// single-<tr> table fragment (no unbalanced <tbody> leak).
func TestRenderMarkdownBlocks_TablePerRow(t *testing.T) {
	// 1: | # | Use case |
	// 2: |---|----------|
	// 3: | C | chat     |
	// 4: | D | auth room |
	src := "| # | Use case |\n|---|----------|\n| C | chat |\n| D | auth room |\n"
	blocks := RenderMarkdownBlocks([]byte(src))
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (header + 2 rows); blocks=%+v", len(blocks), blocks)
	}
	hdr, rowC, rowD := blocks[0], blocks[1], blocks[2]

	if !strings.Contains(string(hdr.HTML), "<thead>") || !strings.Contains(string(hdr.HTML), "<th") {
		t.Errorf("header HTML = %q, want <thead><th>", hdr.HTML)
	}
	if c := strings.Count(string(rowD.HTML), "<tr"); c != 1 {
		t.Errorf("row D must contain exactly one <tr> (clean fragment), got %d in %q", c, rowD.HTML)
	}
	if strings.Contains(string(rowD.HTML), "<thead") || !strings.Contains(string(rowD.HTML), "<tbody>") {
		t.Errorf("row D HTML = %q, want a <tbody> body row with no leaked <thead>", rowD.HTML)
	}
	if !strings.Contains(string(rowD.HTML), "auth room") {
		t.Errorf("row D HTML = %q, want cell text 'auth room'", rowD.HTML)
	}
	// The whole point: row D anchors to its own source line (4), so a
	// comment on it round-trips to raw view line 4 — not the table.
	if rowD.StartLine != 4 || rowD.EndLine != 4 {
		t.Errorf("row D lines = %d-%d, want 4-4", rowD.StartLine, rowD.EndLine)
	}
	if rowC.StartLine != 3 || rowC.EndLine != 3 {
		t.Errorf("row C lines = %d-%d, want 3-3", rowC.StartLine, rowC.EndLine)
	}
	if hdr.StartLine != 1 {
		t.Errorf("header StartLine = %d, want 1", hdr.StartLine)
	}
}

// TestRenderMarkdownBlocks_ProsePerLine pins that a paragraph authored
// one-sentence-per-line splits into one block per source line (so a
// comment targets a single prose line, not the whole 15-line range),
// inline formatting survives per line, and a continuation line that
// would misfire a block rule standalone falls back to safe escaped
// text on its own line.
func TestRenderMarkdownBlocks_ProsePerLine(t *testing.T) {
	// 1: intro **bold** and `code`.
	// 2: 3) a & b paren-ordered.    (start!=1 → stays a paragraph line)
	// 3: tail plain line            (one sentence per line → splits per line)
	src := "intro **bold** and `code`.\n3) a & b paren-ordered.\ntail plain line\n"
	blocks := RenderMarkdownBlocks([]byte(src))
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (one per source line); blocks=%+v", len(blocks), blocks)
	}

	b0, b1, b2 := blocks[0], blocks[1], blocks[2]
	if !strings.Contains(string(b0.HTML), "<strong>bold</strong>") || !strings.Contains(string(b0.HTML), "<code>code</code>") {
		t.Errorf("line 1 HTML = %q, want inline bold+code preserved", b0.HTML)
	}
	if b0.StartLine != 1 || b0.EndLine != 1 {
		t.Errorf("line 1 = %d-%d, want 1-1", b0.StartLine, b0.EndLine)
	}

	// Misfire guard: `3) …` renders as <ol> standalone → fallback to
	// HTML-escaped text in a <p>, anchored to its own line.
	if strings.Contains(string(b1.HTML), "<ol") || strings.Contains(string(b1.HTML), "<li>") {
		t.Errorf("line 2 must NOT misfire as a list; got %q", b1.HTML)
	}
	if !strings.HasPrefix(string(b1.HTML), "<p>") || !strings.Contains(string(b1.HTML), "3) a &amp; b paren-ordered") {
		t.Errorf("line 2 HTML = %q, want escaped literal text in a <p>", b1.HTML)
	}
	if b1.StartLine != 2 || b1.EndLine != 2 {
		t.Errorf("line 2 = %d-%d, want 2-2", b1.StartLine, b1.EndLine)
	}

	if !strings.Contains(string(b2.HTML), "tail plain line") || b2.StartLine != 3 || b2.EndLine != 3 {
		t.Errorf("line 3 = %q @ %d-%d, want 'tail plain line' @ 3-3", b2.HTML, b2.StartLine, b2.EndLine)
	}

	// A single-source-line paragraph stays exactly one block.
	one := RenderMarkdownBlocks([]byte("just one sentence here\n"))
	if len(one) != 1 || one[0].StartLine != 1 || one[0].EndLine != 1 {
		t.Fatalf("single-line paragraph: got %+v, want 1 block @ 1-1", one)
	}
	if !strings.Contains(string(one[0].HTML), "<p>just one sentence here</p>") {
		t.Errorf("single-line paragraph HTML = %q", one[0].HTML)
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

// TestRenderMarkdownBlocks_HardWrapReflow pins that a hard-wrapped
// paragraph (lines break mid-sentence) reflows into ONE CommonMark
// paragraph block — a sentence is never split across visual lines —
// while a one-sentence-per-line paragraph still splits per source line,
// and a mixed document gets both behaviours.
func TestRenderMarkdownBlocks_HardWrapReflow(t *testing.T) {
	// Hard-wrapped at ~80 cols: line 1 ends "naming," (no terminal
	// punctuation) → the whole paragraph must reflow into one block,
	// not three, and the `Phoenix.PubSub` code span that straddled a
	// wrap point must render as a single <code> (it would be lost on a
	// per-line split). This is the exact shape the user reported.
	// 1: **The design:** … publish/subscribe naming,
	// 2: where … identity. This is the `Phoenix.PubSub`
	// 3: model. … unless you `Publish`.
	src := "**The design:** a single primitive — a named topic, with classic publish/subscribe naming,\n" +
		"where per-identity targeting is a topic derived from the identity. This is the `Phoenix.PubSub`\n" +
		"model. Per-connection state is the default; nothing fans out unless you `Publish`.\n"
	blocks := RenderMarkdownBlocks([]byte(src))
	if len(blocks) != 1 {
		t.Fatalf("hard-wrapped paragraph: got %d blocks, want 1 reflowed block; blocks=%+v", len(blocks), blocks)
	}
	b := blocks[0]
	if b.StartLine != 1 || b.EndLine != 3 {
		t.Errorf("reflowed block lines = %d-%d, want 1-3 (full source span stays commentable)", b.StartLine, b.EndLine)
	}
	h := string(b.HTML)
	if !strings.HasPrefix(h, "<p>") || !strings.HasSuffix(h, "</p>") {
		t.Errorf("reflowed block must be a single <p>…</p>; got %q", h)
	}
	if !strings.Contains(h, "<strong>The design:</strong>") {
		t.Errorf("inline bold lost; HTML = %q", h)
	}
	if !strings.Contains(h, "<code>Phoenix.PubSub</code>") || !strings.Contains(h, "<code>Publish</code>") {
		t.Errorf("code spans across/at wrap boundary not preserved; HTML = %q", h)
	}
	// The previously mid-sentence-broken boundary is now in one block.
	if !strings.Contains(h, "publish/subscribe naming,") || !strings.Contains(h, "where per-identity targeting") {
		t.Errorf("sentence across the wrap not contiguous in one block; HTML = %q", h)
	}

	// One sentence per line → still one block per source line.
	osl := RenderMarkdownBlocks([]byte("First sentence here.\nSecond sentence here.\nThird and last sentence.\n"))
	if len(osl) != 3 {
		t.Fatalf("one-sentence-per-line: got %d blocks, want 3; blocks=%+v", len(osl), osl)
	}
	for i, want := range []string{"First sentence", "Second sentence", "Third and last"} {
		if osl[i].StartLine != i+1 || osl[i].EndLine != i+1 {
			t.Errorf("osl block %d lines = %d-%d, want %d-%d", i, osl[i].StartLine, osl[i].EndLine, i+1, i+1)
		}
		if !strings.Contains(string(osl[i].HTML), want) {
			t.Errorf("osl block %d HTML = %q, want %q", i, osl[i].HTML, want)
		}
	}

	// Mixed: a one-sentence-per-line paragraph (2 lines → 2 blocks) and
	// a hard-wrapped paragraph (2 lines → 1 reflowed block).
	mix := RenderMarkdownBlocks([]byte(
		"Standalone sentence one.\nStandalone sentence two.\n\n" +
			"Hard wrapped paragraph that continues\nonto a second physical line here.\n"))
	if len(mix) != 3 {
		t.Fatalf("mixed doc: got %d blocks, want 3 (2 split + 1 reflowed); blocks=%+v", len(mix), mix)
	}
	if mix[0].StartLine != 1 || mix[0].EndLine != 1 || mix[1].StartLine != 2 || mix[1].EndLine != 2 {
		t.Errorf("mixed: first paragraph must split per line; got %+v / %+v", mix[0], mix[1])
	}
	if mix[2].StartLine != 4 || mix[2].EndLine != 5 {
		t.Errorf("mixed: hard-wrapped paragraph must be one block @ 4-5; got %+v", mix[2])
	}
	if !strings.Contains(string(mix[2].HTML), "Hard wrapped paragraph that continues") ||
		!strings.Contains(string(mix[2].HTML), "onto a second physical line here.") {
		t.Errorf("mixed: reflowed block must contain both source lines; got %q", mix[2].HTML)
	}
}

// TestRenderMarkdownBlocks_ThematicBreakLineRanges pins that nodes
// without source segments — goldmark's ThematicBreak is the load-bearing
// example, since it stores no Lines() data — still get unique [Start,End]
// ranges instead of collapsing to [1, 1]. Without the cursor fallback,
// every `---` separator anchored to line 1, so a multi-section document
// rendered a comment / composer on L1 once per separator (the user-
// reported bug: ~20 stacked composers).
func TestRenderMarkdownBlocks_ThematicBreakLineRanges(t *testing.T) {
	// 1: # H1
	// 2: (blank)
	// 3: para A.
	// 4: (blank)
	// 5: ---
	// 6: (blank)
	// 7: ## H2
	// 8: (blank)
	// 9: para B.
	// 10: (blank)
	// 11: ---
	// 12: (blank)
	// 13: ## H3
	src := "# H1\n\npara A.\n\n---\n\n## H2\n\npara B.\n\n---\n\n## H3\n"
	blocks := RenderMarkdownBlocks([]byte(src))

	// Sanity: we got both <hr>s plus the surrounding blocks.
	var hrs []MarkdownBlock
	for _, b := range blocks {
		if strings.Contains(string(b.HTML), "<hr") {
			hrs = append(hrs, b)
		}
	}
	if len(hrs) != 2 {
		t.Fatalf("got %d <hr> blocks, want 2; blocks=%+v", len(hrs), blocks)
	}

	// The invariant the template depends on: every block has a unique
	// range and ranges do not overlap. Walk in emit order and check.
	for i, b := range blocks {
		if b.StartLine > b.EndLine {
			t.Errorf("block %d: StartLine %d > EndLine %d", i, b.StartLine, b.EndLine)
		}
		if i > 0 && b.StartLine <= blocks[i-1].EndLine {
			t.Errorf("block %d at [%d,%d] overlaps prior block at [%d,%d]; HTML=%q",
				i, b.StartLine, b.EndLine, blocks[i-1].StartLine, blocks[i-1].EndLine, b.HTML)
		}
	}

	// Neither <hr> should collapse to line 1 (the smoking-gun symptom).
	for i, hr := range hrs {
		if hr.StartLine == 1 {
			t.Errorf("hr %d collapsed to line 1 (pre-fix bug)", i)
		}
	}
}

// TestEndsSentence pins the terminal-punctuation rule that decides
// one-sentence-per-line vs hard-wrapped: only . ! ? (after stripping
// trailing whitespace and inline-close markers) end a sentence; , : ;
// and em-dash, and a line ending in an inline code span, do not.
func TestEndsSentence(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"ends here.", true},
		{"a question?", true},
		{"excited!", true},
		{"trailing spaces.   ", true},
		{"ends with `code`.", true},
		{`quoted sentence."`, true},
		{"(a parenthetical.)", true},
		{"bold sentence.**", true},
		{"see Fig. 3.", true},
		{"naming,", false},
		{"ends in colon:", false},
		{"ends in semicolon;", false},
		{"em dash —", false},
		{"This is the `Phoenix.PubSub`", false}, // the real reported line
		{"v2.2.0", false},
		{"   ", false},
		{"", false},
	} {
		if got := endsSentence([]byte(tc.line)); got != tc.want {
			t.Errorf("endsSentence(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestExtractHeadings(t *testing.T) {
	t.Run("empty source returns nil", func(t *testing.T) {
		if got := ExtractHeadings(nil); got != nil {
			t.Errorf("ExtractHeadings(nil) = %v, want nil", got)
		}
		if got := ExtractHeadings([]byte("   \n\n   ")); got != nil {
			t.Errorf("ExtractHeadings(whitespace) = %v, want nil", got)
		}
	})

	t.Run("source with no headings returns nil", func(t *testing.T) {
		src := []byte("just a paragraph.\n\nanother paragraph.\n")
		if got := ExtractHeadings(src); got != nil {
			t.Errorf("ExtractHeadings(no-headings) = %v, want nil", got)
		}
	})

	t.Run("captures level, id, and text in document order", func(t *testing.T) {
		src := []byte("# Top Title\n\n## Sub One\n\n### Deeper\n\n## Sub Two\n")
		got := ExtractHeadings(src)
		want := []Heading{
			{Level: 1, ID: "top-title", Text: "Top Title"},
			{Level: 2, ID: "sub-one", Text: "Sub One"},
			{Level: 3, ID: "deeper", Text: "Deeper"},
			{Level: 2, ID: "sub-two", Text: "Sub Two"},
		}
		if len(got) != len(want) {
			t.Fatalf("got %d headings, want %d: %+v", len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("heading[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("disambiguates duplicate slugs with -1 -2 suffix", func(t *testing.T) {
		src := []byte("# Notes\n\n## Notes\n\n## Notes\n")
		got := ExtractHeadings(src)
		if len(got) != 3 {
			t.Fatalf("got %d headings, want 3", len(got))
		}
		if got[0].ID != "notes" || got[1].ID != "notes-1" || got[2].ID != "notes-2" {
			t.Errorf("dup-slug ids = [%q, %q, %q], want [\"notes\", \"notes-1\", \"notes-2\"]",
				got[0].ID, got[1].ID, got[2].ID)
		}
	})

	t.Run("handles inline emphasis in heading text", func(t *testing.T) {
		// goldmark walks inline children; emphasis nodes wrap a Text node
		// whose Segment carries the plain text, so the visible label is
		// preserved without the *…* markers.
		src := []byte("# Hello *world*\n")
		got := ExtractHeadings(src)
		if len(got) != 1 {
			t.Fatalf("got %d headings, want 1", len(got))
		}
		if got[0].Text != "Hello world" {
			t.Errorf("heading text = %q, want \"Hello world\"", got[0].Text)
		}
		if got[0].ID != "hello-world" {
			t.Errorf("heading id = %q, want \"hello-world\"", got[0].ID)
		}
	})
}

func TestRenderMarkdownBlocks_HeadingHasID(t *testing.T) {
	// AutoHeadingID flows through to the rendered HTML so the TOC links
	// can deep-link to each heading's id without any post-processing.
	src := []byte("# Hello World\n\n## Sub-section\n")
	blocks := RenderMarkdownBlocks(src)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if !strings.Contains(string(blocks[0].HTML), `id="hello-world"`) {
		t.Errorf("h1 HTML missing id attribute: %s", blocks[0].HTML)
	}
	if !strings.Contains(string(blocks[1].HTML), `id="sub-section"`) {
		t.Errorf("h2 HTML missing id attribute: %s", blocks[1].HTML)
	}
}
