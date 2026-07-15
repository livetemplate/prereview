//go:build browser

// End-to-end for #164 (reviewer-facing half): a reply on a settled comment must reopen
// it as "queued" work in the toolbar Queue count — the count that agent-facing
// actionableComments already re-surfaces. Repro: the agent marks a comment DONE, the
// reviewer replies to steer, and the Queue count must move the row back from done→queued
// (not silently stay "done" with the reply invisible in the count).
//
// Run: go test -tags=browser -run TestE2E_QueueReopensOnReply ./e2e/...

package e2e

import (
	"os/exec"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_QueueReopensOnReply(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("app.go")

	// A fresh comment → queued: 1 queued, 0 done.
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "shorten to 100k", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v\nstderr: %s", err, p.stderr.String())
	}
	id := p.readCSV()[1][0]

	// queueNums reads the queue-legend counts (always rendered inside the panel, even at
	// zero — unlike the toolbar button spans, which are conditional), so the numbers are
	// unambiguous across states.
	queueNums := func() (queued, done string) {
		if err := chromedp.Run(p.ctx,
			chromedp.Evaluate(`(document.querySelector('.queue-legend .q-queued')||{}).textContent||""`, &queued),
			chromedp.Evaluate(`(document.querySelector('.queue-legend .q-done')||{}).textContent||""`, &done),
		); err != nil {
			t.Fatalf("read queue counts: %v", err)
		}
		return queued, done
	}

	if q, d := queueNums(); q != "1" || d != "0" {
		t.Fatalf("fresh comment: queued=%q done=%q, want 1/0", q, d)
	}

	// The agent marks it DONE (separate process) → the badge and counts update live:
	// 0 queued, 1 done.
	if out, err := exec.Command(p.binary, "done", "--out", p.repo, id).CombinedOutput(); err != nil {
		t.Fatalf("prereview done: %v\n%s", err, out)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-comment .processed-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("done badge never appeared live: %v\nstderr: %s", err, p.stderr.String())
	}
	if q, d := queueNums(); q != "0" || d != "1" {
		t.Fatalf("after done: queued=%q done=%q, want 0/1", q, d)
	}

	// The reviewer replies to steer — the #164 fix: the done comment REOPENS as queued
	// work, so the count moves back to 1 queued / 0 done and the card shows "awaiting agent".
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-comment button[name='openReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.reply-form textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.reply-form textarea`, "1000 can be written as 1k", chromedp.ByQuery),
		chromedp.Click(`button[name='postReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment .awaiting-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reviewer reply: %v\nstderr: %s", err, p.stderr.String())
	}
	if q, d := queueNums(); q != "1" || d != "0" {
		t.Fatalf("after the reviewer reply, the done comment must reopen as queued: queued=%q done=%q, want 1/0%s", q, d, "\nstderr: "+p.stderr.String())
	}
}
