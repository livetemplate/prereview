//go:build browser

// End-to-end for #159 M4.3a: accepting a suggestion is queued work for the agent,
// so it gets the same feedback a comment does — the toolbar Queue button pulses,
// its count rises, the accepted suggestion shows up as a "queued" row in the queue
// panel (with the ✦ suggestion mark), AND a transient bottom-right toast confirms
// the accept (the suggestion box itself stays put until the agent applies).
//
// Run: go test -tags=browser -run TestE2E_AcceptQueuesSuggestion ./e2e/...

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_AcceptQueuesSuggestion(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("app.go")

	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\"","note":"shorter greeting"}
	]`)

	queueID := func() string {
		var id string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.queue-trigger')||{}).id||""`, &id))
		return id
	}
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.inline-suggestion .sg-old`, chromedp.ByQuery)); err != nil {
		t.Fatalf("suggestion box never rendered: %v%s", err, diag())
	}
	before := queueID()
	if before == "" {
		t.Fatalf("queue trigger button (agent mode) not found%s", diag())
	}

	// Accept the suggestion — the card collapses behind its badge (#165), so PEEK line 4
	// to confirm the verdict badge.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='acceptSuggestion']`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accept suggestion: %v%s", err, diag())
	}
	p.peekRow(4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.sg-verdict-badge.sg-accept`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accepted verdict badge: %v%s", err, diag())
	}

	// 1) The Queue button id (pulse driver) bumped — the accept enqueued work,
	//    exactly like adding a comment (silent: no toast, the button reacts).
	_ = chromedp.Run(p.ctx, chromedp.Sleep(150*time.Millisecond))
	if after := queueID(); after == before {
		t.Errorf("accepting a suggestion should bump the queue button id (drives the pulse); stayed %q%s", before, diag())
	}

	// 2) The queue panel lists the accepted suggestion as a "queued" row carrying
	//    the ✦ suggestion mark and routing its jump to jumpToSuggestion.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.queue-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.queue-row.queue-kind-suggestion.queue-queued`, chromedp.ByQuery),
		chromedp.WaitVisible(`.queue-row.queue-kind-suggestion .queue-kind-icon`, chromedp.ByQuery),
		chromedp.WaitVisible(`.queue-row.queue-kind-suggestion button[name='jumpToSuggestion']`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accepted suggestion should appear as a queued ✦ row in the queue panel: %v%s", err, diag())
	}

	// The row's badge reads "queued" and its body echoes the note.
	var rowBadge, rowBody string
	_ = chromedp.Run(p.ctx,
		chromedp.Evaluate(`(document.querySelector('.queue-row.queue-kind-suggestion .queue-badge')||{}).textContent||""`, &rowBadge),
		chromedp.Evaluate(`(document.querySelector('.queue-row.queue-kind-suggestion .queue-body')||{}).textContent||""`, &rowBody),
	)
	if rowBadge != "queued" {
		t.Errorf("suggestion queue badge = %q, want %q", rowBadge, "queued")
	}
	if rowBody != "shorter greeting" {
		t.Errorf("suggestion queue body = %q, want the note", rowBody)
	}
}
