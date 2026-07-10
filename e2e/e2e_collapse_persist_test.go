//go:build browser

// Regression guard for #112 ("resolved comments toggle on manual reload"). The real
// cause: the per-line card collapse (the right-margin mark button) was a CLIENT-ONLY
// class (.cards-collapsed via lvt-el), so a full page refresh lost it and the
// collapsed/"hidden" cards reappeared. The fix moves collapse into persisted server
// state (CollapsedLines, lvt:"persist") + a toggleLineCollapse action, so a collapse
// survives a refresh. This test collapses a line's cards, refreshes, and asserts they
// STAY collapsed — it fails on the pre-fix (client-only) behaviour.
//
// Run: go test -tags=browser -run TestE2E_CollapsePersists ./e2e/...

package e2e

import (
	"testing"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

func TestE2E_CollapsePersistsAcrossRefresh(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupTwoChangedFilesRepo(t), 1200, 800)
	p.waitReady()
	p.clickFile("target.go")
	p.clickLine(0, 3)
	// Collapse via the mark, and in the SAME synchronous evaluate check the row got
	// .cards-collapsed — this is the OPTIMISTIC client toggle (lvt-el), which fires in
	// the click handler before any server round-trip. A regression that drops lvt-el
	// (leaving only the server action) would make this false: collapse would still
	// work + persist, but laggily on a high-latency phone.
	var optimistic bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "collapse me", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const btn = document.querySelector('.line-marks');
			const row = btn.closest('.line-row');
			btn.click();
			return row.classList.contains('cards-collapsed');
		})()`, &optimistic),
		chromedp.Sleep(300e6),
	); err != nil {
		t.Fatalf("seed+collapse: %v\nstderr: %s", err, p.stderr.String())
	}
	if !optimistic {
		t.Errorf("collapse was not applied optimistically on click — the instant client-side lvt-el toggle is missing (collapse would feel laggy on a high-latency phone)")
	}

	visible := func(label string) bool {
		var v bool
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`(() => { const el = document.querySelector('.inline-comment'); return !!el && el.offsetParent !== null; })()`, &v))
		t.Logf("#112 %-26s comment visible=%v", label, v)
		return v
	}
	if visible("after collapse") {
		t.Fatalf("collapse should hide the card, but it stayed visible\nstderr: %s", p.stderr.String())
	}

	// PURE SSR (JS off ⇒ no WS morph): the collapse must be in the SERVER render.
	if err := chromedp.Run(p.ctx, emulation.SetScriptExecutionDisabled(true)); err != nil {
		t.Fatalf("disable JS: %v", err)
	}
	if err := chromedp.Run(p.ctx, chromedp.Navigate(p.url), chromedp.WaitVisible(`.line-row`, chromedp.ByQuery)); err != nil {
		t.Fatalf("ssr reload: %v", err)
	}
	if visible("SSR after refresh") {
		t.Errorf("#112: collapse lost in the SSR render — CollapsedLines not persisted into the GET")
	}

	// Hydrated reload: collapse must still hold after the WS reconnect.
	if err := chromedp.Run(p.ctx, emulation.SetScriptExecutionDisabled(false)); err != nil {
		t.Fatalf("re-enable JS: %v", err)
	}
	if err := chromedp.Run(p.ctx, chromedp.Navigate(p.url), chromedp.WaitVisible(`.line-row`, chromedp.ByQuery), chromedp.Sleep(600e6)); err != nil {
		t.Fatalf("ws reload: %v", err)
	}
	if visible("WS after refresh") {
		t.Errorf("#112 REGRESSION: collapsed comment reappeared after refresh — collapse did not survive the reload")
	}

	// And clicking the mark again expands it (toggle still works through the server).
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.line-marks').click()`, nil),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("expand via toggle: %v\nstderr: %s", err, p.stderr.String())
	}
	if !visible("after re-expand") {
		t.Errorf("#112: toggling the mark again should re-expand the cards")
	}
}
