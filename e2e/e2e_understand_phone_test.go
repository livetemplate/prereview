//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_UnderstandPhoneMenu: on a phone the ⋯ menu always lists Understand
// and Annotations, as the desktop toolbar does. With an agent Understand is
// enabled before any walkthrough or quiz exists; without one it is disabled and
// shows why under its label, since a phone has no hover.
func TestE2E_UnderstandPhoneMenu(t *testing.T) {
	const fileViewWant = "Preview(off: Only for Markdown and HTML files) | Raw(off: Only for Markdown and HTML files) | Diff | Whitespace | Fold(off: No mechanical changes to fold) | Annotate(off: Only on an image or the HTML preview) | Boxes(off: Only on the HTML preview, once it has annotations)"
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
			// The file's view controls match the desktop file header: all seven
			// listed by the same names, and on a Go file the ones that don't
			// apply are disabled with their reason.
			if got := evalStr(t, p, `[...document.querySelectorAll('.more-menu .menu-file-view')].map(b=>b.querySelector('.menu-label').firstChild.textContent.trim()+(b.disabled?'(off: '+b.querySelector('.menu-why').textContent+')':'')).join(' | ')`); got != fileViewWant {
				t.Errorf("menu file controls =\n  %s\nwant\n  %s%s", got, fileViewWant, diag())
			}
			if got := evalInt(t, p, `document.querySelector('.more-menu button[name="toggleUnderstand"]').disabled ? 0 : 1`) == 1; got != tc.enabled {
				t.Errorf("Understand enabled = %v, want %v%s", got, tc.enabled, diag())
			}
			if !tc.enabled {
				// A phone has no hover, so the reason shows under the label.
				if why := evalStr(t, p, `(document.querySelector('.more-menu button[name="toggleUnderstand"] .menu-why')||{}).textContent||''`); why != "No walkthrough or quiz yet, and no agent to make one" {
					t.Errorf("disabled reason = %q%s", why, diag())
				}
				// Annotations is listed too, disabled with its reason.
				if why := evalStr(t, p, `(()=>{const b=document.querySelector('.more-menu button[name="toggleCommentList"]');return b&&b.disabled?(b.querySelector('.menu-why')||{}).textContent||'':''})()`); why != "No annotations yet" {
					t.Errorf("Annotations in the menu: reason = %q, want disabled with No annotations yet%s", why, diag())
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

// TestE2E_PhoneMenuScrolls: on a short phone screen the ⋯ menu is taller than
// the space below the bar. It must fit on screen and scroll itself, since it is
// fixed and scrolling the page can't reach its last items.
func TestE2E_PhoneMenuScrolls(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 375, 560, "--agent")
	diag := captureDiag(t, p)
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(375, 560),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`header.bar .more-trigger`, chromedp.ByQuery),
		chromedp.Sleep(time.Second), // the client attaches its listeners
		chromedp.Click(`.more-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.more-menu.is-open`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open the ⋯ menu: %v%s", err, diag())
	}
	got := evalStr(t, p, `(()=>{const m=document.querySelector('.more-menu');const r=m.getBoundingClientRect();
		if (r.bottom>innerHeight) return 'menu runs off screen: bottom '+r.bottom+' > '+innerHeight;
		if (m.scrollHeight<=m.clientHeight) return 'menu fits without scrolling ('+m.scrollHeight+'px); the test needs a taller menu';
		m.scrollTop=m.scrollHeight;
		const last=[...m.querySelectorAll('button.menu-item')].pop().getBoundingClientRect();
		return last.bottom<=r.bottom+1?'ok':'last item still hidden after scrolling: '+last.bottom+' > '+r.bottom})()`)
	if got != "ok" {
		t.Errorf("%s%s", got, diag())
	}
}
