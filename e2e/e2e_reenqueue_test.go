//go:build browser

// Regression guard for the reported bug (#126 follow-up): the per-card
// "Enqueue"/"Re-enqueue" button on a WORKED-ON comment must actually return it to
// the agent's queue — clear the "worked on" state — not just flip a label while
// the comment stays done. The user saw the button toggle but the comment "not
// actually enqueued". Also verifies the manual "Draft" button is gone: the only
// per-card queue action is (re-)enqueue.
//
// Run with: go test -tags=browser -run TestE2E_ReenqueueWorkedOnComment ./e2e/...

package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestE2E_ReenqueueWorkedOnComment(t *testing.T) {
	// --stream so the per-card (re-)enqueue button renders (StreamMode-gated) and
	// the continuous emit engine + WatchLLMStatus (processed fan-out) are live.
	p := bootChromeAgainstPrereview(t, 1200, 800, "--stream")

	var consoleLines []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			consoleLines = append(consoleLines, string(e.Type))
		}
	})
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`.inline-comment`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- console ---\n" +
			strings.Join(consoleLines, "\n") + "\n--- card html ---\n" + html
	}

	p.waitReady()
	p.clickFile("edited.go")

	// Add one comment.
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "redo this please", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v%s", err, diag())
	}

	// A freshly-added comment is queued (not draft, not worked-on) → NO per-card
	// enqueue button, and definitely no "Draft" button (that control was removed).
	var enqueueBtns, draftBtns int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('.inline-comment .enqueue-btn').length`, &enqueueBtns),
		chromedp.Evaluate(`[...document.querySelectorAll('.inline-comment button')].filter(b=>b.textContent.trim()==='Draft').length`, &draftBtns),
	); err != nil {
		t.Fatalf("count buttons: %v%s", err, diag())
	}
	if enqueueBtns != 0 {
		t.Errorf("a queued comment should show no (re-)enqueue button, got %d%s", enqueueBtns, diag())
	}
	if draftBtns != 0 {
		t.Errorf("the manual 'Draft' button must be gone, got %d%s", draftBtns, diag())
	}

	// The agent marks the comment worked-on (separate process, like the skill).
	id := p.readCSV()[1][0]
	if out, err := exec.Command(p.binary, "processed", "--out", p.repo, id).CombinedOutput(); err != nil {
		t.Fatalf("prereview processed: %v\n%s", err, out)
	}

	// Live: the "worked on" badge appears AND a "Re-enqueue" button surfaces.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-comment .processed-badge`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment .enqueue-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("worked-on badge / Re-enqueue button never appeared: %v%s", err, diag())
	}
	var btnText string
	if err := chromedp.Run(p.ctx,
		chromedp.Text(`.inline-comment .enqueue-btn`, &btnText, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("read button text: %v%s", err, diag())
	}
	if strings.TrimSpace(btnText) != "Re-enqueue" {
		t.Errorf("worked-on comment's button = %q, want %q%s", btnText, "Re-enqueue", diag())
	}

	// Click Re-enqueue → the comment must ACTUALLY leave the worked-on state
	// (the bug was it stayed "done"). The badge and the button both disappear.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-comment .enqueue-btn`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("click Re-enqueue: %v%s", err, diag())
	}
	var badgeCount, btnCount int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('.inline-comment .processed-badge').length`, &badgeCount),
		chromedp.Evaluate(`document.querySelectorAll('.inline-comment .enqueue-btn').length`, &btnCount),
	); err != nil {
		t.Fatalf("re-read after re-enqueue: %v%s", err, diag())
	}
	if badgeCount != 0 {
		t.Errorf("after Re-enqueue the 'worked on' badge must be gone (comment back to queued), still %d%s", badgeCount, diag())
	}
	if btnCount != 0 {
		t.Errorf("after Re-enqueue the button must be gone (comment is plain-queued), still %d%s", btnCount, diag())
	}

	// Durable across reload: the un-mark is derived from disk (reenqueued.jsonl),
	// so the comment stays queued (no badge) after a fresh Mount.
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
		chromedp.Sleep(250*time.Millisecond),
		chromedp.Evaluate(`document.querySelectorAll('.inline-comment .processed-badge').length`, &badgeCount),
	); err != nil {
		t.Fatalf("reload: %v%s", err, diag())
	}
	if badgeCount != 0 {
		t.Errorf("after reload the comment must still be queued (no worked-on badge), got %d%s", badgeCount, diag())
	}
}
