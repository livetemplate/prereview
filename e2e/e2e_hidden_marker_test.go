//go:build browser

// End-to-end for #233: hiding a suggestion leaves a "1 hidden suggestion · Show"
// marker where its card was, so the reviewer brings it back from the line instead of
// the View menu. In Diff view the row also stays out of the fold that would
// otherwise swallow it. The suggestions arrive by `prereview suggest` after the page
// has loaded, so every render here is a live update.
//
// Run with: go test -tags=browser -run TestE2E_HiddenSuggestionMarker ./e2e/...

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_HiddenSuggestionMarker(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 390, 844, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(390, 844)
	ctx, cancel := within(p, 40*time.Second)
	defer cancel()

	// hideAndBringBack hides card, expects a marker in scope, taps its Show, and
	// expects the card back. It never opens the View menu.
	hideAndBringBack := func(where, card, scope string) {
		t.Helper()
		if err := chromedp.Run(ctx,
			chromedp.WaitVisible(card, chromedp.ByQuery),
			chromedp.Click(card+` button[name="hideSuggestion"]`, chromedp.ByQuery),
			chromedp.WaitNotPresent(card, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("%s: hide the suggestion: %v%s", where, err, diag())
		}
		marker := scope + ` .sg-hidden-marker`
		if err := chromedp.Run(ctx, chromedp.WaitVisible(marker, chromedp.ByQuery)); err != nil {
			t.Fatalf("%s: no marker where the hidden suggestion was: %v%s", where, err, diag())
		}
		if got := strings.TrimSpace(textOf(t, p, marker+` .sg-hidden-label`)); got != "1 hidden suggestion" {
			t.Errorf("%s: marker label = %q, want \"1 hidden suggestion\"%s", where, got, diag())
		}
		if err := chromedp.Run(ctx,
			chromedp.Click(marker+` button[name="unhideSuggestions"]`, chromedp.ByQuery),
			chromedp.WaitVisible(card, chromedp.ByQuery),
			chromedp.WaitNotPresent(marker, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("%s: Show on the marker didn't bring the suggestion back: %v%s", where, err, diag())
		}
	}

	// Diff view: folded.go changes only line 1, so line 11 sits in a fold unless
	// something on it keeps it out.
	p.clickFile("folded.go")
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"far","file":"folded.go","from_line":11,"to_line":11,"original":"func keepMe() {}","proposed":"func keepMe() { _ = 1 }"}
	]`)
	row := `.line-row:has(.line[data-line="11"][data-side="new"])`
	hideAndBringBack("diff view", `.inline-suggestion[data-key="sg-far"]`, row)

	// Rendered Markdown: the marker sits in the block the suggestion belonged to.
	p.clickFile("notes.md")
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"doc","file":"notes.md","from_line":3,"to_line":3,"original":"The API might returns an error.","proposed":"The API may return an error."}
	]`)
	hideAndBringBack("markdown view", `.inline-suggestion[data-key="sg-doc"]`, `.md-view`)
}
