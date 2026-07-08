//go:build browser

// End-to-end for #128: read progress. Scrolling a long file marks the lines
// scrolled past as "read" (a left rail), and re-opening the file restores the
// scroll to where the reviewer left off.
//
// Run with: go test -tags=browser -run TestE2E_ReadProgress ./e2e/...

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func setupLongFileRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "t@e.com")
	runCmd(t, dir, "git", "config", "user.name", "T")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "seed.txt", "seed\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	// A long, all-new file so the viewer scrolls; every line is an add.
	var b strings.Builder
	for i := 1; i <= 150; i++ {
		fmt.Fprintf(&b, "line %d — some content to give the row width\n", i)
	}
	mustWrite(t, dir, "long.txt", b.String())
	mustWrite(t, dir, "other.txt", "a\nshort\nfile\n")
	// A long markdown file so read progress can be exercised in Preview mode too.
	var md strings.Builder
	md.WriteString("# Long Guide\n\n")
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&md, "## Section %d\n\nParagraph %d with enough words to render a real block for the reviewer to read.\n\n", i, i)
	}
	mustWrite(t, dir, "guide.md", md.String())
	return dir
}

func TestE2E_ReadProgress(t *testing.T) {
	// Small viewport so a 150-line file is definitely scrollable.
	p := bootChromeAgainstRepo(t, setupLongFileRepo(t), 1000, 650)
	p.waitReady()
	p.clickFile("long.txt")

	evalInt := func(js string) int {
		var n int
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(js, &n))
		return n
	}
	evalFloat := func(js string) float64 {
		var f float64
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(js, &f))
		return f
	}
	// pollInt waits (up to ~6s) for js to satisfy want(n).
	pollInt := func(js string, want func(int) bool, what string) int {
		deadline := time.Now().Add(6 * time.Second)
		var last int
		for time.Now().Before(deadline) {
			last = evalInt(js)
			if want(last) {
				return last
			}
			_ = chromedp.Run(p.ctx, chromedp.Sleep(200*time.Millisecond))
		}
		t.Fatalf("%s: condition never met (last=%d)\nstderr: %s", what, last, p.stderr.String())
		return last
	}

	// Scroll the viewer down past the first screenful.
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`document.querySelector('main.viewer').scrollTo(0, 1400)`, nil)); err != nil {
		t.Fatalf("scroll: %v", err)
	}

	// The debounced viewport-report → ReportViewport → re-render advances the
	// top reading-progress bar (its fill width, as a %, climbs above 0).
	pollInt(`(() => { const f = document.querySelector('.file-head .read-progress-fill'); if (!f) return 0; return Math.round(parseFloat(f.style.width) || 0); })()`,
		func(n int) bool { return n > 5 }, "reading-progress bar fills after scrolling")

	// Switch away and back — the file should re-open at the last read location
	// (viewer scrolled, not at the top).
	p.clickFile("other.txt")
	if err := chromedp.Run(p.ctx, chromedp.Sleep(400*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	p.clickFile("long.txt")

	// Restore is a one-shot lvt-fx:scroll on the last-read top line → the viewer
	// scrollTop settles above 0.
	deadline := time.Now().Add(6 * time.Second)
	var top float64
	for time.Now().Before(deadline) {
		top = evalFloat(`document.querySelector('main.viewer').scrollTop`)
		if top > 100 {
			break
		}
		_ = chromedp.Run(p.ctx, chromedp.Sleep(200*time.Millisecond))
	}
	if top <= 100 {
		t.Errorf("re-opening the file should restore scroll to the last read location, got scrollTop=%.0f\nstderr: %s", top, p.stderr.String())
	}
}

// TestE2E_ReadProgressPreviewAndResume: read progress also works in the rendered
// Markdown Preview view (tracking .md-block, not .line), and the "Resume" chip
// jumps to the read frontier.
func TestE2E_ReadProgressPreviewAndResume(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupLongFileRepo(t), 1000, 650)
	p.waitReady()
	p.clickFile("guide.md") // markdown → opens in Preview (rendered) by default

	evalInt := func(js string) int {
		var n int
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(js, &n))
		return n
	}
	poll := func(js string, want func(int) bool, what string) int {
		deadline := time.Now().Add(6 * time.Second)
		var last int
		for time.Now().Before(deadline) {
			if last = evalInt(js); want(last) {
				return last
			}
			_ = chromedp.Run(p.ctx, chromedp.Sleep(200*time.Millisecond))
		}
		t.Fatalf("%s: never satisfied (last=%d)\nstderr: %s", what, last, p.stderr.String())
		return last
	}

	// Confirm we're in Preview (md blocks present, no code lines).
	poll(`document.querySelectorAll('.md-block').length`, func(n int) bool { return n > 0 }, "preview blocks render")

	// Scroll the rendered view → the progress bar fills in PREVIEW mode.
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('main.viewer').scrollTo(0, 2200)`, nil))
	poll(`(() => { const f=document.querySelector('.file-head .read-progress-fill'); return f ? Math.round(parseFloat(f.style.width)||0) : 0; })()`,
		func(n int) bool { return n > 5 }, "preview reading-progress bar fills")

	// The Resume chip appears once the file is partly read.
	poll(`document.querySelector('button[name="jumpToReadFrontier"]') ? 1 : 0`,
		func(n int) bool { return n == 1 }, "Resume chip appears when partly read")

	// Scroll back to the top, then Resume → the viewer jumps down to the frontier.
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('main.viewer').scrollTo(0, 0)`, nil))
	_ = chromedp.Run(p.ctx, chromedp.Sleep(500*time.Millisecond))
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('button[name="jumpToReadFrontier"]').click()`, nil))
	poll(`Math.round(document.querySelector('main.viewer').scrollTop)`,
		func(n int) bool { return n > 100 }, "Resume jumps to the read frontier")
}
