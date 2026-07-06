//go:build browser

// End-to-end for the suggestion hide feature: a reviewer can hide a single
// suggestion or a whole group of alternatives from view, and bring them back via
// "Show hidden". Hiding is a pure declutter — it never records a decision.
//
// Run with: go test -tags=browser -run TestE2E_HideSuggestions ./e2e/...

package e2e

import (
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_HideSuggestions(t *testing.T) {
	p, _, _ := bootChromeStreamRepo(t, setupSuggestionRepo(t))
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("app.go")

	// A group of two alternatives on line 4, plus a standalone on line 3.
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"alt1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""},
	  {"id":"alt2","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hey\""},
	  {"id":"lone","file":"app.go","from_line":3,"to_line":3,"original":"func Greet() string {","proposed":"func Greet() (string) {"}
	]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-lone"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-alt1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("suggestions never appeared: %v%s", err, diag())
	}

	// Hide the standalone → it drops; the group stays.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.inline-suggestion[data-key="sg-lone"] button[name="hideSuggestion"]').click()`, nil),
		chromedp.WaitNotPresent(`.inline-suggestion[data-key="sg-lone"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-alt1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("hiding the standalone should drop only it: %v%s", err, diag())
	}

	// Hide the whole group via any member's "Hide all" → both alternatives drop.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.inline-suggestion[data-key="sg-alt1"] button[name="hideSuggestionGroup"]').click()`, nil),
		chromedp.WaitNotPresent(`.inline-suggestion[data-key="sg-alt1"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.inline-suggestion[data-key="sg-alt2"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("hiding the group should drop every alternative: %v%s", err, diag())
	}

	// "Show hidden suggestions" (View menu) brings all three back.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('button[name="showHiddenSuggestions"]').click()`, nil),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-lone"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-alt1"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-alt2"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("show-hidden should restore every suggestion: %v%s", err, diag())
	}

	// Hiding recorded no decision (view-only): the decision-count status is absent.
	var decisionText string
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`(document.querySelector('.sg-decision-count')?.textContent||"")`, &decisionText))
	if decisionText != "" {
		t.Errorf("hiding must not record a decision, but saw decision status %q", decisionText)
	}
}
