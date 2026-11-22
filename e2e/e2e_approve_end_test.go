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

	// Every button's label is readable on its own background, in every scheme and
	// mode (a filled .secondary rendered End session white on the pale surface).
	for _, look := range []string{"solarized light", "solarized dark", "gruvbox light", "gruvbox dark", "catppuccin light", "catppuccin dark"} {
		scheme, mode, _ := strings.Cut(look, " ")
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(`(() => { const r = document.querySelector('.theme-root'); r.dataset.scheme = `+"`"+scheme+"`"+`; r.dataset.mode = `+"`"+mode+"`"+`; })()`, nil),
			chromedp.Sleep(350*time.Millisecond), // the 100–140ms colour transitions
		); err != nil {
			t.Fatalf("switch to %s: %v", look, err)
		}
		if low := evalStr(t, p, `(() => {
	  const lum = (c) => { const m = c.match(/[\d.]+/g); let [r,g,b] = m.map(Number);
	    if (/^color\(/.test(c)) { r*=255; g*=255; b*=255; }
	    const f = v => { v/=255; return v<=0.03928 ? v/12.92 : Math.pow((v+0.055)/1.055,2.4); };
	    return 0.2126*f(r)+0.7152*f(g)+0.0722*f(b); };
	  const opaque = c => { const m = c.match(/[\d.]+/g); return m && (m.length<4 || Number(m[3])>0.5); };
	  const bg = el => { for (let n = el; n; n = n.parentElement) { const b = getComputedStyle(n).backgroundColor; if (opaque(b)) return b; } return 'rgb(255,255,255)'; };
	  return [...document.querySelectorAll('#confirm-end-session footer button')].map(b => {
	    const a = lum(getComputedStyle(b).color), c = lum(bg(b));
	    const ratio = (Math.max(a,c)+0.05)/(Math.min(a,c)+0.05);
	    return ratio < 4.5 ? b.textContent.trim() + ' ' + ratio.toFixed(2) : '' }).filter(Boolean).join('; ') })()`); low != "" {
			t.Errorf("%s: dialog buttons below 4.5:1 contrast: %s%s", look, low, diag())
		}
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
