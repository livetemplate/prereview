//go:build browser

// End-to-end for #116: when the inline suggestion boxes are toggled off and the
// agent submits a BRAND-NEW suggestion, the boxes auto-reveal so the fresh
// proposal isn't silently swallowed by the hidden toggle.
//
// Run with: go test -tags=browser -run TestE2E_RevealSuggestionsOnSubmit ./e2e/...

package e2e

import (
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_RevealSuggestionsOnSubmit(t *testing.T) {
	p, _, _ := bootChromeStreamRepo(t, setupSuggestionRepo(t))
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("app.go")

	// First suggestion appears live.
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""}
	]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-s1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("first suggestion never appeared: %v%s", err, diag())
	}

	// Toggle the inline boxes OFF (the reviewer declutters to read the diff).
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('button[name="toggleSuggestions"]').click()`, nil),
		chromedp.WaitNotPresent(`.inline-suggestion`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("toggling suggestions off should hide the boxes: %v%s", err, diag())
	}

	// The agent submits a BRAND-NEW suggestion while hidden → boxes auto-reveal
	// (#116). Both s1 and the new s2 are shown again.
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s2","file":"app.go","from_line":3,"to_line":3,"original":"func Greet() string {","proposed":"func Greet() (string) {"}
	]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-s2"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-s1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a new suggestion must auto-reveal the hidden boxes (#116): %v%s", err, diag())
	}
}
