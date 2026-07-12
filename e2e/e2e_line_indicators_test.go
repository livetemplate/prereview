//go:build browser

// End-to-end for #136: right-margin per-line indicators. A line carrying a
// comment or a suggestion shows a tiny colour-coded mark on the right; clicking
// the mark toggles that line's inline cards.
//
// Run with: go test -tags=browser -run TestE2E_LineIndicators ./e2e/...

package e2e

import (
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_LineIndicators(t *testing.T) {
	p, _, _ := bootChromeStreamRepo(t, setupSuggestionRepo(t))
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("app.go")

	// A suggestion on line 4 → the row grows a suggestion mark.
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""}
	]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.line-row.has-line-marks .line-mark`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("suggestion line never grew a suggestion mark: %v%s", err, diag())
	}

	// A comment on line 3 → that row grows a comment mark.
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "needs a doc comment", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.line-row.has-line-marks .line-mark`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("comment line never grew a comment mark: %v%s", err, diag())
	}

	// #165 retired the manual per-line collapse: an OPEN suggestion's card always shows,
	// and clicking the badge peeks DONE cards (covered by TestE2E_AnnotationBadges). Here
	// we just confirm the open card stays visible beside its badge.
	rowSel := `.line-row:has(.inline-suggestion[data-key="sg-s1"])`
	var suggestionVisible bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(rowSel+` .inline-suggestion`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('`+rowSel+` .inline-suggestion')?.offsetParent`, &suggestionVisible),
	); err != nil {
		t.Fatalf("open suggestion card: %v%s", err, diag())
	}
	if !suggestionVisible {
		t.Errorf("an open suggestion's card should stay visible%s", diag())
	}
}
