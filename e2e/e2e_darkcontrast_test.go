//go:build browser

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_DarkContrastButtonReadable guards the second half of #124: a Pico
// `.contrast` button (the Queue chip) keeps a Pico-pinned DARK background in every
// mode, but its label color (--pico-contrast-inverse) defaulted to var(--surface),
// which flips DARK in dark mode → the "Queue" text vanished. The fix forces
// --pico-contrast-inverse to var(--text-strong) (light) in dark mode. This asserts
// the Queue button's text is LIGHT on its DARK background in dark mode.
func TestE2E_DarkContrastButtonReadable(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1400, 900, "--stream")
	p.waitReadyAt(1400, 900)
	p.clickFile("edited.go")
	for i := 0; i < 2; i++ { // System("") → Light → Dark
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('header.bar button[name="cycleTheme"]').click()`, nil))
		time.Sleep(400 * time.Millisecond)
	}
	var mode string
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.theme-root')?.getAttribute('data-mode'))||''`, &mode))
	if mode != "dark" {
		t.Fatalf("expected explicit dark, got %q", mode)
	}

	avg := func(css string) float64 {
		var r, g, b int
		if _, err := fmt.Sscanf(strings.TrimSpace(css), "rgb(%d, %d, %d", &r, &g, &b); err != nil {
			t.Fatalf("parse color %q: %v", css, err)
		}
		return float64(r+g+b) / 3
	}
	get := func(sel, prop string) string {
		var v string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			fmt.Sprintf(`getComputedStyle(document.querySelector(%q)).%s`, sel, prop), &v))
		return v
	}

	color := get(`.queue-trigger`, "color")
	bg := get(`.queue-trigger`, "backgroundColor")
	ct, cb := avg(color), avg(bg)
	// Label must be LIGHT (the bug rendered it #002b36 ≈ 32) on a DARK button, with
	// real contrast between them.
	if ct < 140 {
		t.Errorf("dark-mode Queue button text %q is too dark (avg %.0f) — invisible label (#124)", color, ct)
	}
	if ct-cb < 100 {
		t.Errorf("Queue button text (avg %.0f) has too little contrast with its bg %q (avg %.0f)", ct, bg, cb)
	}
}
