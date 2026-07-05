//go:build browser

// End-to-end for #117: accepting one alternative in a group auto-rejects the
// others, the auto-reject reads as "alternative", and undoing the accept re-opens
// the whole group.
//
// Run with: go test -tags=browser -run TestE2E_GroupedSuggestionAutoReject ./e2e/...

package e2e

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_GroupedSuggestionAutoReject(t *testing.T) {
	p, _, _ := bootChromeStreamRepo(t, setupSuggestionRepo(t))
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("app.go")

	// Two alternatives for the SAME text/area (line 4) — a group.
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"alt1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""},
	  {"id":"alt2","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hey\""}
	]`)

	// Accept alt1 → alt1 accepted, alt2 auto-rejected (no manual reject click).
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-alt1"] button[name="acceptSuggestion"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-alt2"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.inline-suggestion[data-key="sg-alt1"] button[name="acceptSuggestion"]').click()`, nil),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-alt1"] .sg-verdict-badge.sg-accept`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-alt2"] .sg-verdict-badge.sg-reject`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accept alt1 → auto-reject alt2: %v%s", err, diag())
	}

	// The auto-reject reads "alternative", not "rejected".
	var altText string
	if err := chromedp.Run(p.ctx, chromedp.Text(`.inline-suggestion[data-key="sg-alt2"] .sg-verdict-badge.sg-reject`, &altText, chromedp.ByQuery)); err != nil {
		t.Fatalf("read alt2 badge: %v%s", err, diag())
	}
	if strings.TrimSpace(altText) != "alternative" {
		t.Errorf("auto-rejected alternative badge = %q, want %q", strings.TrimSpace(altText), "alternative")
	}

	// Undo alt1's accept → the whole group re-opens (no verdict badges anywhere).
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.inline-suggestion[data-key="sg-alt1"] button[name="clearSuggestionDecision"]').click()`, nil),
		chromedp.WaitNotPresent(`.inline-suggestion .sg-verdict-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("undo accept should re-open the group: %v%s", err, diag())
	}
}
