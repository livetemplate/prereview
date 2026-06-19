//go:build browser

// End-to-end coverage for issue #24: a ```mermaid fence in the rendered-
// Markdown view must (1) render client-side as an inline SVG diagram,
// (2) stay commentable — a comment on the diagram round-trips to the CSV
// anchored to the fence's real source lines, (3) survive the server DOM
// patch that adding a comment triggers (the lvt-ignore container must keep
// the injected SVG, not revert to raw text), and (4) fall back to the raw
// definition + an error note when a diagram fails to parse.
//
// Per project convention the failure path dumps browser console + server
// stderr + rendered HTML.
//
// Run with: go test -tags=browser -run Mermaid ./...

package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// mermaidDoc exercises both paths: a valid flowchart (source lines 6-7) and a
// deliberately invalid diagram (line 13). Built with explicit \n so the
// fences' backticks can appear literally.
//
//	1: # Mermaid Demo
//	2:
//	3: A flow:
//	4:
//	5: ```mermaid
//	6: graph TD
//	7:   A[Start] --> B[End]
//	8: ```
//	9:
//	10: Broken diagram below:
//	11:
//	12: ```mermaid
//	13: notadiagramtype lol
//	14: ```
const mermaidDoc = "# Mermaid Demo\n\n" +
	"A flow:\n\n" +
	"```mermaid\n" +
	"graph TD\n" +
	"  A[Start] --> B[End]\n" +
	"```\n\n" +
	"Broken diagram below:\n\n" +
	"```mermaid\n" +
	"notadiagramtype lol\n" +
	"```\n"

func setupFixtureMermaidRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	// Commit a base diagram.md, then overwrite it with the real content so it
	// is the single CHANGED file: prereview auto-selects it and enables
	// per-block commenting in the rendered view (an untracked file renders but
	// isn't part of the changeset comments anchor to).
	mustWrite(t, dir, "diagram.md", "# Mermaid Demo\n\nplaceholder\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	mustWrite(t, dir, "diagram.md", mermaidDoc)
	return dir
}

func TestE2E_MermaidRendersAndStaysCommentable(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureMermaidRepo(t), 1200, 800)

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

	// diagram.md is the single changed file, so prereview auto-selects it and
	// renders Markdown by default — no clickFile needed (mirrors
	// TestE2E_MarkdownRenderAndComment). waitReady's 1s ceiling plus the SVG
	// wait below give the deferred client time to connect its WebSocket before
	// the first action-bearing click.
	p.waitReady()

	diag := func(html string) string {
		return "\n--- server ---\n" + p.stderr.String() +
			"\n--- console ---\n" + strings.Join(consoleLines, "\n") +
			"\n--- html ---\n" + html
	}

	// 1. The valid fence renders to an inline <svg>. mermaid lazy-loads the
	//    3.3MB bundle and lays the graph out, so allow a generous window.
	var html string
	ctx, cancel := context.WithTimeout(p.ctx, 25*time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.md-mermaid svg`, chromedp.ByQuery),
		chromedp.OuterHTML(`.md-view`, &html, chromedp.ByQuery),
	)
	cancel()
	if err != nil {
		t.Fatalf("mermaid diagram never rendered to SVG: %v%s", err, diag(html))
	}

	// 2. The broken fence falls back to the chosen UX: an error banner plus
	//    the raw definition kept visible (so the source stays reviewable).
	var failHTML, failText string
	ctx, cancel = context.WithTimeout(p.ctx, 15*time.Second)
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`.md-mermaid-failed .md-mermaid-error`, chromedp.ByQuery),
		chromedp.OuterHTML(`.md-mermaid-failed`, &failHTML, chromedp.ByQuery),
		chromedp.Text(`.md-mermaid-failed`, &failText, chromedp.ByQuery),
	)
	cancel()
	if err != nil {
		t.Fatalf("broken diagram never showed the fallback banner: %v%s", err, diag(html))
	}
	if !strings.Contains(failText, "notadiagramtype lol") {
		t.Errorf("fallback should keep the raw definition visible; text=%q%s", failText, diag(failHTML))
	}

	// 3. Comment on the diagram block. Find the .md-block that owns the SVG
	//    and click its .md-rendered (the selectBlock click target — the SVG
	//    click bubbles to it). The composer then opens for that block's span.
	const clickDiagramBlock = `(() => {
		const svg = document.querySelector('.md-mermaid svg');
		if (!svg) return false;
		const block = svg.closest('.md-block');
		const target = block && block.querySelector('.md-rendered');
		if (!target) return false;
		target.click();
		return true;
	})()`
	var clicked bool
	ctx, cancel = context.WithTimeout(p.ctx, 12*time.Second)
	err = chromedp.Run(ctx,
		// Defensive ceiling: the first action-bearing click is silently lost
		// if the client's WS handshake hasn't completed yet (same rationale as
		// TestE2E_MobileDrawer).
		chromedp.Sleep(2*time.Second),
		chromedp.Evaluate(clickDiagramBlock, &clicked),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
	)
	cancel()
	if err != nil {
		t.Fatalf("composer never opened on diagram click (clicked=%v): %v%s", clicked, err, diag(html))
	}
	ctx, cancel = context.WithTimeout(p.ctx, 10*time.Second)
	err = chromedp.Run(ctx,
		chromedp.SendKeys(`.composer textarea`, "this arrow points the wrong way", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		// After the comment is saved the server re-renders the file region.
		chromedp.WaitVisible(`.md-block .inline-comment .body`, chromedp.ByQuery),
	)
	cancel()
	if err != nil {
		t.Fatalf("saving the diagram comment failed: %v%s", err, diag(html))
	}

	// 4. The SVG must survive that server DOM patch — proof that lvt-ignore
	//    protected the client-injected diagram from morphdom.
	var svgStillThere bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('.md-mermaid svg')`, &svgStillThere),
	); err != nil {
		t.Fatalf("post-comment SVG check failed: %v%s", err, diag(html))
	}
	if !svgStillThere {
		t.Errorf("adding a comment clobbered the rendered SVG — lvt-ignore did not protect it%s", diag(html))
	}

	// The comment round-tripped to the CSV anchored to the fence's source
	// lines (the diagram definition is lines 6-7).
	rows := p.readCSV()
	if len(rows) < 2 {
		t.Fatalf("CSV has no comment row; rows=%v", rows)
	}
	var found bool
	for _, r := range rows[1:] {
		if len(r) < 6 {
			continue
		}
		from, _ := strconv.Atoi(r[2]) // from_line
		to, _ := strconv.Atoi(r[3])   // to_line
		body := r[5]
		if strings.Contains(body, "this arrow points the wrong way") {
			found = true
			// Anchored within the fence's content span (lines 6-7).
			if to < 5 || to > 8 {
				t.Errorf("comment anchored to line %d-%d, want within the diagram fence (≈6-7); row=%v", from, to, r)
			}
		}
	}
	if !found {
		t.Errorf("diagram comment not written to CSV; rows=%v", rows)
	}

	// No console errors — a render throw or a security-level rejection would
	// surface here (warnings tolerated).
	for _, line := range consoleLines {
		if strings.HasPrefix(line, "error ") {
			t.Errorf("browser console error: %s%s", line, diag(html))
		}
	}
}
