//go:build browser

// End-to-end for #129: queued-comment confirmation. A saved comment shows a
// "queued" badge, and the Queue button's id embeds the enqueue tick (which drives
// its one-shot pulse) so it bumps on each genuine enqueue.
//
// Run with: go test -tags=browser -run TestE2E_QueuedConfirmation ./e2e/...

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_QueuedConfirmation(t *testing.T) {
	p, _, _ := bootChromeStreamRepo(t, setupSuggestionRepo(t))
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("app.go")

	// The Queue button's id embeds the enqueue tick — capture it before enqueuing.
	queueID := func() string {
		var id string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.queue-trigger')||{}).id||""`, &id))
		return id
	}
	before := queueID()
	if before == "" {
		t.Fatalf("queue trigger button (stream mode) not found%s", diag())
	}

	// Save a comment on line 3.
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "please tighten this", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment .queued-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("saved comment should show a queued badge: %v%s", err, diag())
	}

	// The badge reads "queued".
	var badge string
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`(document.querySelector('.inline-comment .queued-badge')||{}).textContent||""`, &badge))
	if badge != "queued" {
		t.Errorf("queued badge text = %q, want %q", badge, "queued")
	}

	// Saving the comment enqueued it → the button id (pulse driver) changed.
	_ = chromedp.Run(p.ctx, chromedp.Sleep(150*time.Millisecond))
	after := queueID()
	if after == before {
		t.Errorf("enqueuing a comment should bump the queue button id (drives the pulse); stayed %q", before)
	}
}
