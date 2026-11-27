//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

// TestE2E_RunJudgesAlwaysListed: the file header's Checks button is a menu even
// on a file with no judges. Run judges is listed, greyed out with its reason,
// and All checks still opens the Checks page.
func TestE2E_RunJudgesAlwaysListed(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupImportRepo(t), 1280, 900, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(1280, 900)
	ctx, cancel := within(p, 15*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.checks-dropdown .checks-trigger`, chromedp.ByQuery),
		chromedp.Click(`.checks-dropdown .checks-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.checks-dropdown.open `+runJudgesBtn, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open the Checks menu on a file with no judges: %v%s", err, diag())
	}
	if got := evalStr(t, p, `(()=>{const b=document.querySelector('`+runJudgesBtn+`');return b.disabled?(b.querySelector('.menu-why')||{}).textContent||'':'enabled'})()`); got != "No judges on this file" {
		t.Errorf("Run judges = %q, want disabled with No judges on this file%s", got, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(`.checks-panel button[name="openChecks"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("All checks should open the Checks page: %v%s", err, diag())
	}
}

// TestE2E_FileHeadMenuAboveFilterBar: on a phone the file header and the
// Annotations filter bar stick separately, and the bar used to paint over a menu
// opened from the header, hiding Run judges. A tap on Run judges must reach it.
func TestE2E_FileHeadMenuAboveFilterBar(t *testing.T) {
	dir := setupImportRepo(t)
	// An open comment makes the filter bar show under the header.
	seedReviewStore(t, dir, csv.Row{ID: "c1", File: "plan.md", FromLine: 3, ToLine: 3, Side: "new",
		Body: "a note", Kind: "line", CreatedAt: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)})
	p := bootChromeAgainstRepo(t, dir, 390, 844, "--agent")
	diag := captureDiag(t, p)
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(390, 844),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.checks-dropdown .checks-trigger`, chromedp.ByQuery),
		chromedp.Sleep(time.Second), // the client attaches its listeners
	); err != nil {
		t.Fatalf("open plan.md on a phone: %v%s", err, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.ann-nav .ann-filter-trigger`, chromedp.ByQuery),
		chromedp.Click(`.checks-dropdown .checks-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.checks-dropdown.open `+runJudgesBtn, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open the Checks menu: %v%s", err, diag())
	}
	got := evalStr(t, p, `(()=>{const b=document.querySelector('`+runJudgesBtn+`');const r=b.getBoundingClientRect();const hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return hit&&(hit===b||b.contains(hit)||hit===b.closest('form'))?'ok':'covered by '+(hit?hit.className||hit.tagName:'nothing')})()`)
	if got != "ok" {
		t.Errorf("Run judges in the Checks menu is %s%s", got, diag())
	}
}
