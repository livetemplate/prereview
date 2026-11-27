//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

// TestE2E_ResolvedHiddenInMarkdown: with the file's filter on Open, a resolved
// comment in rendered Markdown folds behind its block's badge, whether it was
// resolved before the page loaded or with the card's Resolve button. The badge
// peeks at it, and choosing Open in the filter folds it again, even with Open
// already on: the peek is saved across reloads, so the filter must clear it.
func TestE2E_ResolvedHiddenInMarkdown(t *testing.T) {
	dir := setupImportRepo(t)
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	seedReviewStore(t, dir,
		csv.Row{ID: "cres", File: "plan.md", FromLine: 3, ToLine: 3, Side: "new", Body: "RESOLVED-BEFORE", Kind: "line", CreatedAt: at, Resolved: true},
		csv.Row{ID: "copen", File: "plan.md", FromLine: 5, ToLine: 7, Side: "new", Body: "RESOLVE-ME", Kind: "line", CreatedAt: at})
	p := bootChromeAgainstRepo(t, dir, 390, 844, "--agent")
	diag := captureDiag(t, p)
	ctx, cancel := within(p, 30*time.Second)
	defer cancel()
	shown := func(id string) string {
		return `(()=>{const c=document.querySelector('.md-view .inline-comment[data-key="` + id + `"]');return c?getComputedStyle(c).display!=='none':'missing'})()`
	}
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(390, 844),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.md-view .inline-comment[data-key="copen"]`, chromedp.ByQuery),
		chromedp.Sleep(time.Second), // the client attaches its listeners
	); err != nil {
		t.Fatalf("open plan.md: %v%s", err, diag())
	}
	if got := evalStr(t, p, `String(`+shown("cres")+`)`); got != "false" {
		t.Errorf("a comment resolved before the page loaded shows = %s, want folded%s", got, diag())
	}
	if err := chromedp.Run(ctx, chromedp.Click(`.md-view .inline-comment[data-key="copen"] button[name="toggleResolved"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("resolve from the card: %v%s", err, diag())
	}
	waitJSDiag(t, p, `!!document.querySelector('.md-view .inline-comment[data-key="copen"].is-resolved')`, "the card is resolved", diag)
	if got := evalStr(t, p, `String(`+shown("copen")+`)`); got != "false" {
		t.Errorf("a comment resolved from its card shows = %s, want folded%s", got, diag())
	}

	// Peek with the block's green badge, then choose Open: the card folds again.
	if err := chromedp.Run(ctx, chromedp.Click(`.md-block:has(.inline-comment[data-key="copen"]) .md-block-marks`, chromedp.ByQuery)); err != nil {
		t.Fatalf("tap the block's badge: %v%s", err, diag())
	}
	waitJSDiag(t, p, shown("copen")+` === true`, "the badge shows the resolved card", diag)
	p.clickFilterRow("setAnnShow", "open")
	waitJSDiag(t, p, shown("copen")+` === false`, "choosing Open folds the peeked card again", diag)
	if got := evalStr(t, p, `String(`+shown("cres")+`)`); got != "false" {
		t.Errorf("the other resolved card shows = %s after choosing Open%s", got, diag())
	}
}
