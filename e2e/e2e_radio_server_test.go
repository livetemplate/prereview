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
		chromedp.WaitVisible(`.more-menu.is-open button[name="toggleRawMarkdown"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.more-menu button[name="toggleRawMarkdown"]').click()`, nil),
		chromedp.Poll(`!document.querySelector('.md-view')`, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.EmulateViewport(1200, 900),
	); err != nil {
		t.Fatalf("switch to Raw from the phone menu: %v%s", err, diag())
	}
	var raw, preview bool
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`form[aria-label="Markdown view"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('form[aria-label="Markdown view"] input[value="raw"]').checked`, &raw),
		chromedp.Evaluate(`document.querySelector('form[aria-label="Markdown view"] input[value="rendered"]').checked`, &preview),
	); err != nil {
		t.Fatalf("read the desktop radios: %v%s", err, diag())
	}
	if !raw || preview {
		t.Errorf("desktop Markdown radios: raw=%v preview=%v, want Raw checked%s", raw, preview, diag())
	}
}
