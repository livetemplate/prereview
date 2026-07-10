//go:build browser

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// TestE2E_QueuePanelFitsMobileViewport is the regression guard for the reported
// bug "queue pane is trimmed on the left on mobile" (IMG_3941): the Queue
// dropdown, anchored right:0 to its trigger, was pushed off the left edge of a
// phone viewport because the trigger is inset from the viewport's right edge by
// the ⋮ overflow button — so a 22rem panel's left edge landed at a negative x
// and body{overflow-x:hidden} clipped the leading text ("Queued"→"eued",
// "QUEUED" badge→"EUED"). The fix pins .queue-panel to the VIEWPORT on mobile
// (position:fixed, both margins), like .more-menu. This asserts the open panel
// sits fully within the viewport horizontally.
//
// Run with: go test -tags=browser -run QueuePanelFitsMobileViewport ./e2e/...
func TestE2E_QueuePanelFitsMobileViewport(t *testing.T) {
	// Seed a queued comment on a LONG file path before boot. .queue-loc is
	// `flex: 0 0 auto` with no truncation, so a long "<file>:<line>" balloons the
	// panel out to its max-width (100vw − 1rem) — exactly what pushed it off the
	// left edge on the reporter's device (loc "zany-mixing-metcalfe.md:27"). A
	// short loc keeps the panel at its 22rem min-width, which fits and would NOT
	// exercise the bug. This is the deterministic trigger for the clip.
	repo := setupFixtureRepo(t)
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"q1,notes/zany-mixing-metcalfe-configuration-details.md,27,27,,please revise this line,2026-07-05T12:00:00Z,false,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	// A narrow phone viewport is the whole point — 375×812 ≈ the iPhone that
	// produced the bug report (IMG_3941: 1125px physical / 3× = 375 CSS px).
	p := bootChromeAgainstRepo(t, repo, 400, 900, "--agent")

	var consoleLines []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			consoleLines = append(consoleLines, string(e.Type))
		}
	})
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), html)
	}

	// Force the mobile emulated viewport (headless otherwise pins 800×600
	// regardless of window size) and load the page.
	p.waitReadyAt(375, 812)

	// Open the Queue dropdown and measure the panel against the viewport.
	var rect struct {
		Left, Right, Width, VW float64
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.queue-dropdown .queue-trigger`, chromedp.ByQuery),
		chromedp.Click(`.queue-dropdown .queue-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.queue-panel .queue-row.queue-queued`, chromedp.ByQuery),
		chromedp.Evaluate(`(()=>{
			const r=document.querySelector('.queue-panel').getBoundingClientRect();
			return {Left:r.left,Right:r.right,Width:r.width,VW:window.innerWidth};
		})()`, &rect),
	); err != nil {
		t.Fatalf("open queue panel: %v%s", err, diag())
	}
	t.Logf("queue panel rect: left=%.1f right=%.1f width=%.1f viewport=%.1f", rect.Left, rect.Right, rect.Width, rect.VW)

	// Optional visual capture (set PREREVIEW_QUEUE_SHOTS=<dir>).
	if dir := os.Getenv("PREREVIEW_QUEUE_SHOTS"); dir != "" {
		var buf []byte
		if err := chromedp.Run(p.ctx, chromedp.FullScreenshot(&buf, 90)); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "queue-panel-mobile.png"), buf, 0o644)
		}
	}

	// The load-bearing assertion: the panel's left edge must not be off-screen
	// (a ~1px rounding slack), and its right edge must stay within the viewport.
	// Pre-fix, Left was strongly negative (the trigger's right-edge inset).
	const slack = 1.0
	if rect.Left < -slack {
		t.Errorf("queue panel clipped on the LEFT: left=%.1f (want >= 0); rect=%+v%s", rect.Left, rect, diag())
	}
	if rect.Right > rect.VW+slack {
		t.Errorf("queue panel overflows on the RIGHT: right=%.1f > viewport %.1f; rect=%+v%s", rect.Right, rect.VW, rect, diag())
	}
	if rect.Width <= 0 {
		t.Errorf("queue panel has no width: %+v%s", rect, diag())
	}
}
