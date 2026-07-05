//go:build browser

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_DarkModeTextReadable is the regression guard for #124 ("dark mode makes
// text unreadable"). Pico applies `color: var(--pico-color)` on <body>, which is
// an ancestor of the .theme-root wrapper and stuck on Pico's light default
// (#373c44 ≈ rgb(55,60,68)); that dark color inherited into the themed subtree, so
// in DARK mode any text without its own component color rendered dark-on-dark. The
// fix re-declares `color` on .theme-root so it resolves from the themed
// --pico-color. This asserts the base text is LIGHT on a DARK surface in dark mode.
func TestE2E_DarkModeTextReadable(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1400, 900)
	p.waitReadyAt(1400, 900)
	p.clickFile("edited.go")

	dataMode := func() string {
		var v string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.theme-root')?.getAttribute('data-mode'))||''`, &v))
		return v
	}
	// System("") → Light → Dark.
	for i := 0; i < 2; i++ {
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('header.bar button[name="cycleTheme"]').click()`, nil))
		time.Sleep(400 * time.Millisecond)
	}
	if dataMode() != "dark" {
		t.Fatalf("expected explicit dark, got %q", dataMode())
	}

	// avgChannel returns the mean of the r,g,b of a "rgb(r, g, b)" / "rgba(...)".
	avg := func(css string) float64 {
		var r, g, b int
		if _, err := fmt.Sscanf(strings.TrimSpace(css), "rgb(%d, %d, %d", &r, &g, &b); err != nil {
			if _, err2 := fmt.Sscanf(strings.TrimSpace(css), "rgba(%d, %d, %d", &r, &g, &b); err2 != nil {
				t.Fatalf("parse color %q: %v", css, err)
			}
		}
		return float64(r+g+b) / 3
	}
	get := func(sel, prop string) string {
		var v string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			fmt.Sprintf(`getComputedStyle(document.querySelector(%q)).%s`, sel, prop), &v))
		return v
	}

	// Base text color must be LIGHT in dark mode (the bug rendered it #373c44 ≈ 61).
	textColor := get(`main.viewer`, "color")
	if a := avg(textColor); a < 120 {
		t.Errorf("dark-mode base text color %q is too dark (avg %.0f) — dark-on-dark (#124)", textColor, a)
	}
	// The surface behind it must be DARK, so contrast actually exists.
	surface := get(`.layout`, "backgroundColor")
	if a := avg(surface); a > 90 {
		t.Errorf("dark-mode surface %q is not dark (avg %.0f)", surface, a)
	}
}
