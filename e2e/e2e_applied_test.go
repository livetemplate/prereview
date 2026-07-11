//go:build browser

// End-to-end for #159 Phase 10 (applied ack): after the reviewer accepts a suggestion,
// the agent applies the edit and runs `prereview applied <id>`; the card flips LIVE from
// "accepted" to "applied" (via the agent-signal watcher), the "Edit applied to the file"
// status appears, and the Undo is gone (undoing an applied edit needs a revert — a
// follow-up — not a desyncing decision-clear).
//
// Run: go test -tags=browser -run TestE2E_AppliedAck ./e2e/...

package e2e

import (
	"os/exec"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_AppliedAck(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("app.go")

	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""}
	]`)

	// Accept the suggestion → "accepted" badge + Undo present.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion .sg-old`, chromedp.ByQuery),
		chromedp.Click(`button[name='acceptSuggestion']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.sg-verdict-badge.sg-accept`, chromedp.ByQuery),
		chromedp.WaitVisible(`button[name='clearSuggestionDecision']`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accept suggestion: %v\nstderr: %s", err, p.stderr.String())
	}
	var verdict string
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.sg-verdict-badge.sg-accept').textContent.trim()`, &verdict))
	if verdict != "accepted" {
		t.Errorf("before applied ack, badge should read 'accepted'; got %q", verdict)
	}

	// The agent applies the edit and acks it.
	if out, err := exec.Command(p.binary, "applied", "--out", p.repo, "s1").CombinedOutput(); err != nil {
		t.Fatalf("prereview applied: %v\n%s", err, out)
	}

	// LIVE (M4.3b): the applied ack collapses the box out of the diff to a ✦ badge in
	// the right margin — this is how the applied state now surfaces (declutter). The
	// re-expand → applied status → no-Undo details are covered by
	// TestE2E_AppliedCollapsesToBadge; here we just assert the ack pushes live.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.line-margin .applied-badge`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.inline-suggestion[data-key="sg-s1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("applied ack should collapse the box to a ✦ badge live: %v\nstderr: %s", err, p.stderr.String())
	}
}
