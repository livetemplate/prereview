//go:build browser

// End-to-end coverage for the in-app Usage page (issue #75): the /_usage route
// renders the curated docs/usage.md through the app's Markdown renderer, and it
// is linked from the keyboard-help modal and both View-options menus. The
// load-bearing assertion is the conflict guarantee — an exact ServeMux pattern
// plus an extension-less path means a repo file (usage.md, or even a bare
// `_usage`) can never shadow the route.
//
// Run with: go test -tags=browser -run UsagePage ./e2e/...

package e2e

import (
	"strings"
	"testing"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestE2E_UsagePage(t *testing.T) {
	repo := setupFixtureRepo(t)
	// Seed decoy files in the working dir that a naive route could collide
	// with: a `usage.md` (routes to the SPA — .md isn't a static extension)
	// and a bare `_usage` (extension-less — never served from disk). Neither
	// may shadow the /_usage route; if one did, the content assertions below
	// would see this marker instead of the guide.
	mustWrite(t, repo, "usage.md", "# REPO USAGE DECOY\nthis file must NOT be served at /_usage\n")
	mustWrite(t, repo, "_usage", "REPO _USAGE DECOY — must not be served\n")

	p := bootChromeAgainstRepo(t, repo, 1400, 900)

	var consoleLines, wsFrames []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		case *cdpnetwork.EventWebSocketFrameReceived:
			wsFrames = append(wsFrames, "recv "+e.Response.PayloadData)
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+e.Response.PayloadData)
		}
	})

	p.waitReadyAt(1400, 900)

	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.documentElement.outerHTML`, &html))
		return "\n--- server ---\n" + p.stderr.String() +
			"\n--- console ---\n" + strings.Join(consoleLines, "\n") +
			"\n--- ws ---\n" + strings.Join(wsFrames, "\n") +
			"\n--- html (truncated) ---\n" + truncate(html, 4000)
	}
	evalStr := func(js string) string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("eval %q: %v%s", js, err, diag())
		}
		return v
	}
	evalBool := func(js string) bool {
		var v bool
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("eval %q: %v%s", js, err, diag())
		}
		return v
	}

	// 1. The keyboard-help modal links to /_usage (in the DOM regardless of
	//    whether the overlay is currently open).
	if !evalBool(`!!document.querySelector('.kbd-help-modal a[href="/_usage"]')`) {
		t.Fatalf("keyboard-help modal is missing the /_usage link%s", diag())
	}

	// 2. The desktop "View ▾" dropdown holds a Usage-guide link. Open it for
	//    real so the item's reachability through the dropdown is exercised.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.tb-dropdown-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tb-dropdown.open .tb-dropdown-panel a[href="/_usage"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("View dropdown is missing the Usage-guide link: %v%s", err, diag())
	}
	// The link opens in a new tab so the review session is left intact.
	if got := evalStr(`document.querySelector('.tb-dropdown-panel a[href="/_usage"]').target`); got != "_blank" {
		t.Fatalf("Usage-guide link target = %q, want _blank%s", got, diag())
	}

	// 3. Navigate straight to /_usage and assert the CURATED guide renders —
	//    not either decoy. (target=_blank would open a new tab; a direct nav
	//    asserts the same route without the tab-handling flakiness.)
	if err := chromedp.Run(p.ctx, chromedp.Navigate(p.url+"/_usage")); err != nil {
		t.Fatalf("navigate /_usage: %v%s", err, diag())
	}
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.md-rendered h1`, chromedp.ByQuery)); err != nil {
		t.Fatalf("/_usage did not render a heading: %v%s", err, diag())
	}

	h1 := evalStr(`document.querySelector('.md-rendered h1')?.textContent || ''`)
	if !strings.Contains(h1, "Using prereview") {
		t.Fatalf("/_usage h1 = %q, want the curated guide (\"Using prereview\")%s", h1, diag())
	}
	bodyText := evalStr(`document.body.innerText`)
	if strings.Contains(bodyText, "DECOY") {
		t.Fatalf("/_usage served a repo decoy file — route was shadowed%s", diag())
	}
	// Themed by the same .theme-root tokens as the SPA…
	if !evalBool(`!!document.querySelector('.theme-root[data-scheme]')`) {
		t.Fatalf("/_usage is not wrapped in a themed .theme-root%s", diag())
	}
	// …and fenced code is chroma class-highlighted (so /syntax.css applies and
	// it follows the Light/Dark toggle).
	if !evalBool(`!!document.querySelector('.md-rendered .chroma')`) {
		t.Fatalf("/_usage fenced code is not chroma-highlighted%s", diag())
	}

	t.Logf("Usage page verified: modal + View-menu links, /_usage renders the guide, "+
		"decoys not shadowed; %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
}

// truncate caps diagnostic HTML dumps so a failure message stays readable.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
