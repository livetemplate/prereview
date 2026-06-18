//go:build browser

// End-to-end coverage for issue #20: the rendered-Markdown view must show
// the full GitHub-flavoured feature set — syntax-highlighted code fences,
// GitHub alerts (> [!NOTE]), footnotes ([^1]), and emoji shortcodes
// (:rocket:). Boots prereview against a repo holding one Markdown file that
// exercises all four, then reads the rendered DOM (the same per-block
// .md-rendered surface the reviewer comments on). Per project convention the
// failure path dumps browser console + server stderr + rendered HTML.
//
// Run with: go test -tags=browser -run Markdown ./...

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// gfmDoc is the fixture Markdown. Built with explicit \n (not a raw string)
// so the ```go fence's backticks can appear literally. Covers, in order: a
// heading, a footnote reference, a NOTE alert, a highlighted Go fence, an
// emoji shortcode, and the footnote definition.
const gfmDoc = "# GFM Demo\n\n" +
	"A claim worth a footnote.[^1]\n\n" +
	"> [!NOTE]\n" +
	"> Useful information users should know.\n\n" +
	"```go\n" +
	"func main() { return }\n" +
	"```\n\n" +
	"Ship it :rocket:\n\n" +
	"[^1]: The supporting note.\n"

// setupFixtureGFMRepo builds a repo whose working tree adds one untracked
// Markdown file (gfm.md). Untracked files show in the drawer and render, so
// no base mutation is needed.
func setupFixtureGFMRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	mustWrite(t, dir, "keep.go", "package keep\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	mustWrite(t, dir, "gfm.md", gfmDoc)
	return dir
}

func TestE2E_MarkdownRendersFullGFM(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureGFMRepo(t), 1200, 800)

	// Capture browser console so a render/JS error surfaces in diagnostics.
	var consoleLines []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		}
	})

	p.waitReady()
	p.clickFile("gfm.md")

	var html, text string
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.md-rendered`, chromedp.ByQuery),
		chromedp.OuterHTML(`.md-view`, &html, chromedp.ByQuery),
		// textContent decodes HTML entities, so the emoji codepoint reads as
		// the literal rune here rather than &#x1f680;.
		chromedp.Evaluate(`document.querySelector('.md-view').textContent`, &text),
	)
	cancel()

	diag := func() string {
		return "\n--- server ---\n" + p.stderr.String() +
			"\n--- console ---\n" + strings.Join(consoleLines, "\n") +
			"\n--- html ---\n" + html
	}
	if err != nil {
		t.Fatalf("rendered markdown never appeared: %v%s", err, diag())
	}

	// 1. Syntax-highlighted code fence — chroma inline-styled spans.
	if !strings.Contains(html, `<span style="color:`) {
		t.Errorf("no chroma-highlighted spans in rendered code fence%s", diag())
	}
	// 2. GitHub NOTE alert — the callout box + its octicon, marker not leaked.
	if !strings.Contains(html, "md-alert-note") {
		t.Errorf("NOTE alert not rendered as a .md-alert callout%s", diag())
	}
	if !strings.Contains(html, "<svg") {
		t.Errorf("alert octicon SVG missing%s", diag())
	}
	if strings.Contains(text, "[!NOTE]") {
		t.Errorf("alert marker [!NOTE] leaked as literal text%s", diag())
	}
	// 3. Footnote — a footnote-ref link whose href stays a plain intra-doc
	//    anchor (the linkrewrite.go fix), plus the definition text.
	if !strings.Contains(html, "footnote-ref") || !strings.Contains(html, `href="#fn:1"`) {
		t.Errorf("footnote reference not rendered with an intact #fn:1 anchor%s", diag())
	}
	if !strings.Contains(text, "The supporting note.") {
		t.Errorf("footnote definition text missing%s", diag())
	}
	// 4. Emoji — rendered as the Unicode rocket, not the literal shortcode.
	if !strings.Contains(text, "\U0001F680") {
		t.Errorf("emoji :rocket: not rendered as 🚀%s", diag())
	}
	if strings.Contains(text, ":rocket:") {
		t.Errorf("emoji shortcode left literal%s", diag())
	}

	// Risk #1: clicking the footnote ref changes the URL hash to #fn:1, which
	// is NOT a deep-link the SPA router understands. Verify the click neither
	// throws nor routes the viewer away from gfm.md — the footnote must be
	// navigable without breaking the page.
	clickCtx, clickCancel := context.WithTimeout(p.ctx, 10*time.Second)
	var stillOnFile bool
	err = chromedp.Run(clickCtx,
		chromedp.Click(`.md-rendered a.footnote-ref`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Evaluate(`!!document.evaluate(
			"//main[contains(@class,'viewer')]//strong[normalize-space(text())='gfm.md']",
			document, null, XPathResult.BOOLEAN_TYPE, null).booleanValue`, &stillOnFile),
	)
	clickCancel()
	if err != nil {
		t.Errorf("clicking footnote ref failed: %v%s", err, diag())
	} else if !stillOnFile {
		t.Errorf("clicking footnote ref routed the viewer away from gfm.md (SPA mis-handled #fn:1)%s", diag())
	}

	// No console errors (warnings tolerated) — covers a footnote-click throw.
	for _, line := range consoleLines {
		if strings.HasPrefix(line, "error ") {
			t.Errorf("browser console error: %s", line)
		}
	}
}
