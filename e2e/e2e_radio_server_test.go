//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_ToolbarRadioFollowsServer pins that the toolbar's view radios show a
// change the server made without a click on them. On a phone the Preview / Raw
// choice lives in the ⋮ menu; switching there and then widening the window
// (a rotated tablet) must leave the desktop Raw radio checked. The client used
// to keep a radio's checked state as the user's unless it had lvt-on:click, so
// the desktop radios kept showing Preview.
func TestE2E_ToolbarRadioFollowsServer(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupImportRepo(t), 390, 844)
	diag := captureDiag(t, p)
	p.waitReadyAt(390, 844)
	ctx, cancel := within(p, 60*time.Second)
	defer cancel()

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.querySelector('button[name="toggleMoreMenu"]').click()`, nil),
		chromedp.WaitVisible(`.more-menu.is-open input[name="view"][value="raw"] + button`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.more-menu input[name="view"][value="raw"] + button').click()`, nil),
		chromedp.Poll(`!document.querySelector('.md-view')`, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.EmulateViewport(1200, 900),
	); err != nil {
		t.Fatalf("switch to Raw from the phone menu: %v%s", err, diag())
	}
	var raw, preview string
	pressed := func(value string) string {
		return `(()=>{const i=document.querySelector('.file-head-view input[name="view"][value="` + value + `"]');return i?i.form.querySelector('button').getAttribute('aria-pressed'):"missing"})()`
	}
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.file-head-view`, chromedp.ByQuery),
		chromedp.Evaluate(pressed("raw"), &raw),
		chromedp.Evaluate(pressed("rendered"), &preview),
	); err != nil {
		t.Fatalf("read the desktop Preview | Raw: %v%s", err, diag())
	}
	if raw != "true" || preview != "false" {
		t.Errorf("desktop Preview | Raw: raw=%s preview=%s, want Raw pressed%s", raw, preview, diag())
	}
}
