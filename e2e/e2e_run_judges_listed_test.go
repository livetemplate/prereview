//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
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
