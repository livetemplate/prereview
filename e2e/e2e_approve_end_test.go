//go:build browser

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/device"
)

// TestE2E_ApproveAndEnd (#278): the End session dialog offers Approve and end. On a
// phone its three buttons stack, Approve and end on top and inside the screen;
// tapping it writes an `end` event carrying approved:true and shows the approved
// banner. (A plain End session's `end` has no approved field: TestE2E_StreamHandoff.)
func TestE2E_ApproveAndEnd(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 390, 844, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(390, 844)
	if err := chromedp.Run(p.ctx, chromedp.Emulate(device.IPhone11), chromedp.Sleep(300*time.Millisecond)); err != nil {
		t.Fatalf("emulate phone: %v", err)
	}
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.queue-dropdown .queue-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.queue-panel button[commandfor='confirm-end-session']`, chromedp.ByQuery),
		chromedp.Click(`.queue-panel button[commandfor='confirm-end-session']`, chromedp.ByQuery),
		chromedp.WaitVisible(`#confirm-end-session[open] button[name='approveAndEnd']`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open the End session dialog: %v%s", err, diag())
	}

	// Stacked on a phone: Approve and end above End session above Cancel, each
	// within the screen.
	got := evalStr(t, p, `(() => {
		const q = s => document.querySelector('#confirm-end-session ' + s).getBoundingClientRect();
		const a = q('button[name="approveAndEnd"]'), e = q('button[name="endSession"]'), c = q('button[command="close"]');
		return [Math.round(a.top), Math.round(e.top), Math.round(c.top), Math.round(Math.min(a.left, e.left, c.left)), Math.round(Math.max(a.right, e.right, c.right)), window.innerWidth].join(',') })()`)
	var at, et, ct, left, right, w int
	if _, err := fmt.Sscanf(got, "%d,%d,%d,%d,%d,%d", &at, &et, &ct, &left, &right, &w); err != nil || !(at < et && et < ct) || left < 0 || right > w {
		t.Errorf("on a phone the buttons should stack Approve, End, Cancel within the screen (tops a,e,c, left, right, width = %s)%s", got, diag())
	}

	if err := chromedp.Run(ctx, chromedp.Click(`#confirm-end-session button[name='approveAndEnd']`, chromedp.ByQuery)); err != nil {
		t.Fatalf("tap Approve and end: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, `(document.querySelector('.banner-stopping') || {textContent: ''}).textContent.includes('Approved. Session ended')`,
		5*time.Second, "the approved banner", diag)

	events := filepath.Join(p.store, "events.jsonl")
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(events)
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if last = lines[len(lines)-1]; strings.Contains(last, `"event":"end"`) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(last, `"event":"end"`) || !strings.Contains(last, `"approved":true`) {
		t.Errorf("the last event should be end with approved:true, got %s%s", last, diag())
	}
}
