//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_ResolvedStableAcrossReloads is a regression guard for #112 ("resolved
// comments toggle on manual reload"). It asserts that across repeated reloads a
// resolved comment's DATA (CSV `resolved` col) and VISIBILITY (with Show-resolved
// on) both stay stable, with no flicker between the SSR render and the WS-connect
// render. NOTE: as of writing this does NOT reproduce #112 — the two obvious
// mechanisms (CSV persistence, an SSR-vs-connect ShowResolved mismatch) are both
// ruled out here. #112 needs the reporter's specific conditions to pin down; this
// test locks in the stability we DO have so a future regression is caught.
func TestE2E_ResolvedStableAcrossReloads(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")

	// Add a comment on new line 4.
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "resolve me", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v\nstderr: %s", err, p.stderr.String())
	}

	// Resolve it.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='toggleResolved']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("resolve: %v\nstderr: %s", err, p.stderr.String())
	}

	// The CSV is the source of truth: assert resolved=true there once.
	rows := p.readCSV()
	if len(rows) != 2 || rows[1][7] != "true" {
		t.Fatalf("after resolve, CSV resolved col = %q, want true (rows=%v)", func() string {
			if len(rows) == 2 {
				return rows[1][7]
			}
			return "?"
		}(), rows)
	}

	// Turn Show-resolved ON (a durable pref) so the resolved comment should stay
	// visible — this is the condition #112 needs.
	p.openViewItem("toggleShowResolved")
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.inline-comment.is-resolved`, chromedp.ByQuery)); err != nil {
		t.Fatalf("show resolved: %v\nstderr: %s", err, p.stderr.String())
	}

	// Reload several times; the resolved comment's DATA and VISIBILITY must both
	// be stable — no flicker between the SSR render and the WS-connect render.
	for i := 0; i < 5; i++ {
		var resolvedVisibleAtSSR, resolvedVisibleAfterConnect bool
		if err := chromedp.Run(p.ctx,
			chromedp.Navigate(p.url),
			chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
			// Right after SSR parse, before the WS morph settles.
			chromedp.Evaluate(`!!document.querySelector('.inline-comment.is-resolved')`, &resolvedVisibleAtSSR),
			chromedp.Sleep(900*time.Millisecond),
			chromedp.Evaluate(`!!document.querySelector('.inline-comment.is-resolved')`, &resolvedVisibleAfterConnect),
		); err != nil {
			t.Fatalf("reload %d: %v", i, err)
		}
		rows := p.readCSV()
		if len(rows) != 2 || rows[1][7] != "true" {
			t.Errorf("#112: reload %d — resolved data flipped (rows=%v)", i, rows)
		}
		if resolvedVisibleAtSSR != resolvedVisibleAfterConnect {
			t.Errorf("#112: reload %d — resolved comment flickered (SSR visible=%v, after connect=%v); the SSR and connect renders disagree on Show-resolved",
				i, resolvedVisibleAtSSR, resolvedVisibleAfterConnect)
		}
	}
}
