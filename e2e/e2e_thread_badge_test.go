//go:build browser

// End-to-end for the #149/#151 unread signal: when the reviewer replies on a line's
// comment, the line's count badge grows an "unread" dot and the card shows "awaiting
// agent"; when the agent replies back, both clear (the agent has the last word).
//
// Run: go test -tags=browser -run TestE2E_ThreadUnreadBadge ./e2e/...

package e2e

import (
	"os/exec"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_ThreadUnreadBadge(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("app.go")
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "tighten this", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v\nstderr: %s", err, p.stderr.String())
	}
	id := p.readCSV()[1][0]

	present := func(sel string) bool {
		var v bool
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`!!document.querySelector('`+sel+`')`, &v))
		return v
	}
	waitGone := func(sel string) bool {
		for i := 0; i < 40; i++ { // ~4s: the agent-signal watcher polls ~sub-second
			if !present(sel) {
				return true
			}
			_ = chromedp.Run(p.ctx, chromedp.Sleep(100*time.Millisecond))
		}
		return false
	}

	// No unread signal yet (fresh comment, no reply).
	if present(`.line-marks.has-unread`) || present(`.awaiting-badge`) {
		t.Fatal("a fresh comment should not show an unread dot or awaiting badge")
	}

	// Reviewer replies → the line badge grows an unread dot + the card shows awaiting.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-comment button[name='openReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.reply-form textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.reply-form textarea`, "make it warmer", chromedp.ByQuery),
		chromedp.Click(`button[name='postReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.awaiting-badge`, chromedp.ByQuery),
		chromedp.WaitVisible(`.line-marks.has-unread`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reviewer reply / unread signal: %v\nstderr: %s", err, p.stderr.String())
	}

	// Agent replies back → the unread dot and awaiting badge clear (agent-last).
	if out, err := exec.Command(p.binary, "reply", "--out", p.repo, "--body", "Done — warmer wording.", id).CombinedOutput(); err != nil {
		t.Fatalf("agent reply: %v\n%s", err, out)
	}
	if !waitGone(`.line-marks.has-unread`) {
		t.Errorf("the unread dot should clear after the agent replies")
	}
	if !waitGone(`.awaiting-badge`) {
		t.Errorf("the awaiting-agent badge should clear after the agent replies")
	}
}
