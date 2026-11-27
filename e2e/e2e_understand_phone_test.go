//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_UnderstandPhoneMenu: on a phone the ⋯ menu always lists Understand,
// as the desktop toolbar does. With an agent it is enabled before any
// walkthrough or quiz exists; without one it is disabled and says why.
func TestE2E_UnderstandPhoneMenu(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		enabled bool
	}{
		{"agent", []string{"--agent"}, true},
		{"no agent", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 375, 812, tc.args...)
			diag := captureDiag(t, p)
			ctx, cancel := within(p, 20*time.Second)
			defer cancel()
			if err := chromedp.Run(ctx,
				chromedp.EmulateViewport(375, 812),
				chromedp.Navigate(p.url),
				chromedp.WaitVisible(`header.bar .more-trigger`, chromedp.ByQuery),
				chromedp.Sleep(time.Second), // the client attaches its listeners
				chromedp.Click(`.more-trigger`, chromedp.ByQuery),
				chromedp.WaitVisible(`.more-menu.is-open button[name="toggleUnderstand"]`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("Understand in the ⋯ menu: %v%s", err, diag())
			}
			if got := evalInt(t, p, `document.querySelector('.more-menu button[name="toggleUnderstand"]').disabled ? 0 : 1`) == 1; got != tc.enabled {
				t.Errorf("Understand enabled = %v, want %v%s", got, tc.enabled, diag())
			}
			if !tc.enabled {
				if title := evalStr(t, p, `document.querySelector('.more-menu button[name="toggleUnderstand"]').title`); title != "No walkthrough or quiz yet, and no agent to make one" {
					t.Errorf("disabled title = %q", title)
				}
				return
			}
			if err := chromedp.Run(ctx, chromedp.Click(`.more-menu button[name="toggleUnderstand"]`, chromedp.ByQuery),
				chromedp.WaitVisible(`.understand-tabs`, chromedp.ByQuery)); err != nil {
				t.Fatalf("open Understand from the ⋯ menu: %v%s", err, diag())
			}
		})
	}
}
