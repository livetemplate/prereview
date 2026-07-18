//go:build browser

// Regression guards for #112 — "on manual page reload, resolved comments keeps
// toggling".
//
// These began as a triage probe and are kept because they close a real coverage
// gap: #112 was reported on v0.23.0, declared fixed, re-opened by the reporter
// the same day, and the whole subsystem was then rewritten by #174. It does NOT
// reproduce on post-#174 HEAD (see the verdict below) — and nothing was
// guarding that, so a re-regression would have been silent.
//
// Why this probe and not the existing reload tests: e2e_reload_test.go and
// e2e_reload_stable_test.go both drive the GLOBAL "Show resolved" switch, which
// persists to DISK and is re-read in Mount on both the GET and the WS connect —
// so first paint and morph always agree and they cannot reproduce this. The
// per-row badge peek (#174 ToggledRows) is the only reveal path that hangs off
// the SESSION (lvt:"persist", in-memory store keyed by the livetemplate-id
// cookie), and e2e_badge_toggle_test.go only proves it survives a WS re-render —
// never a full page RELOAD, and never on a resolved comment.
//
// So the ToggledRows-across-reload surface is untested, and this probe targets
// exactly it, taking the report literally: "toggles on EACH reload" is an
// alternation claim (reload N vs N+1), which no existing test checks.
//
//	go test -tags=browser -run TestE2E_Issue112 ./e2e/
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	cdpemulation "github.com/chromedp/cdproto/emulation"
	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// TestE2E_Issue112_ToggledRowsSurvivesRepeatedReload is the load-bearing guard.
//
// VERDICT (2026-07-18, post-#174 HEAD): does NOT reproduce. 8 reloads, the
// revealed card stayed shown every time, livetemplate-id byte-stable (1 distinct
// value). Same under mobile emulation. #174 made the toggle server-owned state,
// which is why.
//
// Seed one RESOLVED comment, reveal it via its green count badge (the
// ToggledRows path), then reload many times and record on each reload both
// whether the card is visible and the exact livetemplate-id cookie value.
//
// Verdict:
//   - visibility alternates with a byte-stable cookie -> real defect (branch 3A)
//   - stable across every reload                      -> does not reproduce on
//     post-#174 HEAD (branch 3-Close); the reporter was on pre-#174 v0.23.0
//   - stable until the cookie changes                 -> environmental (3B)
func TestE2E_Issue112_ToggledRowsSurvivesRepeatedReload(t *testing.T) {
	const reloads = 8

	repo := setupFixtureRepo(t)
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// One RESOLVED comment: starts collapsed behind a green badge.
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,3,3,new,already handled,2026-07-13T12:00:00Z,true,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	p := bootChromeAgainstRepo(t, repo, 1400, 1000)

	// Repo rule: a browser probe must expose console, server stderr, WS frames
	// and rendered HTML, or a red tells you nothing.
	var mu sync.Mutex
	var consoleLines, wsFrames []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		mu.Lock()
		defer mu.Unlock()
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
			wsFrames = append(wsFrames, "recv "+clipFrame(e.Response.PayloadData, 160))
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+clipFrame(e.Response.PayloadData, 160))
		}
	})
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return "\n--- server ---\n" + p.stderr.String() +
			"\n--- console ---\n" + strings.Join(consoleLines, "\n") +
			"\n--- ws frames (last 12) ---\n" + strings.Join(lastFrames(wsFrames, 12), "\n") +
			"\n--- html ---\n" + snippet([]byte(html))
	}

	p.waitReady()
	p.clickFile("edited.go")

	row := `.line-row:has(.line[data-line="3"][data-side="new"])`

	// Precondition: resolved => collapsed behind a green badge.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(row+` .line-mark.is-done`, chromedp.ByQuery),
		chromedp.WaitNotVisible(row+` .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("precondition: a resolved comment should start collapsed behind a green badge: %v%s", err, diag())
	}

	// Reveal it via the badge — this is the ToggledRows (session-persisted) path.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(row+` .line-marks`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("clicking the green badge must reveal the resolved comment: %v%s", err, diag())
	}

	// visible reports whether the resolved card is actually rendered (offsetParent
	// is nil for display:none, which is how .is-resolved collapses).
	visible := func() bool {
		var n int
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`[...document.querySelectorAll('`+row+` .inline-comment')].filter(e=>e.offsetParent).length`, &n))
		return n > 0
	}
	// sessionCookie reads livetemplate-id via CDP: it is HttpOnly, so
	// document.cookie cannot see it.
	sessionCookie := func() string {
		var out string
		_ = chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			cookies, err := cdpnetwork.GetCookies().Do(ctx)
			if err != nil {
				return err
			}
			for _, c := range cookies {
				if c.Name == "livetemplate-id" {
					out = c.Value
				}
			}
			return nil
		}))
		return out
	}

	type sample struct {
		visible bool
		cookie  string
	}
	samples := []sample{{visible: visible(), cookie: sessionCookie()}}
	if !samples[0].visible {
		t.Fatalf("the reveal did not take effect before any reload — probe is not testing what it claims%s", diag())
	}

	for i := 1; i <= reloads; i++ {
		if err := chromedp.Run(p.ctx,
			chromedp.Reload(),
			// Wait for the row itself, not for the card: waiting on the card
			// would presuppose the very visibility under test.
			chromedp.WaitVisible(row+` .line-mark`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("reload %d failed: %v%s", i, err, diag())
		}
		samples = append(samples, sample{visible: visible(), cookie: sessionCookie()})
	}

	var vis, cookies []string
	for _, s := range samples {
		vis = append(vis, map[bool]string{true: "shown", false: "HIDDEN"}[s.visible])
		cookies = append(cookies, s.cookie)
	}
	t.Logf("#112 probe — visibility across %d reloads: %s", reloads, strings.Join(vis, " "))
	t.Logf("#112 probe — livetemplate-id stable: %v (%d distinct)", allEqual(cookies), distinctCount(cookies))

	// (ii) cookie stability — decides 3A vs 3B if visibility does move.
	if !allEqual(cookies) {
		t.Errorf("livetemplate-id changed across reloads (%d distinct values) — the session-scoped "+
			"ToggledRows reveal cannot survive that. Environmental (branch 3B).\ncookies: %v%s",
			distinctCount(cookies), cookies, diag())
	}

	// (i) the alternation claim, taken literally.
	alternations := 0
	for i := 1; i < len(samples); i++ {
		if samples[i].visible != samples[i-1].visible {
			alternations++
		}
	}
	if alternations > 0 {
		t.Errorf("#112 REPRODUCES: the resolved comment's visibility changed %d time(s) across %d "+
			"reloads with a %s cookie — %s.\nsequence: %s%s",
			alternations, reloads,
			map[bool]string{true: "byte-stable", false: "CHANGING"}[allEqual(cookies)],
			map[bool]string{true: "real defect, branch 3A", false: "environmental, branch 3B"}[allEqual(cookies)],
			strings.Join(vis, " "), diag())
	}
}

func clipFrame(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func lastFrames(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func allEqual(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return false
		}
	}
	return true
}

func distinctCount(s []string) int {
	seen := map[string]bool{}
	for _, v := range s {
		seen[v] = true
	}
	return len(seen)
}

// TestE2E_Issue112_SensitivityControl is the negative control for the gate above.
//
// A green gate is worthless if the probe cannot detect the failure it claims to
// rule out. This runs the SAME sequence but clears cookies between reloads,
// which drops the session group and must therefore lose the ToggledRows reveal.
// It asserts the probe SEES that — i.e. it fails loudly if visibility stays
// stable, because that would mean the gate above is blind.
//
// Note (per the plan): losing the reveal on a deliberately dropped cookie is
// session scoping working AS DESIGNED, and a one-time loss is "gone", not
// "toggling". This proves detection power only; it is not a reproduction of #112.
func TestE2E_Issue112_SensitivityControl(t *testing.T) {
	repo := setupFixtureRepo(t)
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,3,3,new,already handled,2026-07-13T12:00:00Z,true,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	p := bootChromeAgainstRepo(t, repo, 1400, 1000)
	p.waitReady()
	p.clickFile("edited.go")
	row := `.line-row:has(.line[data-line="3"][data-side="new"])`

	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(row+` .line-mark.is-done`, chromedp.ByQuery),
		chromedp.Click(row+` .line-marks`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("setup: reveal the resolved comment: %v", err)
	}

	visible := func() bool {
		var n int
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`[...document.querySelectorAll('`+row+` .inline-comment')].filter(e=>e.offsetParent).length`, &n))
		return n > 0
	}
	if !visible() {
		t.Fatalf("control setup failed: the reveal never took effect")
	}

	// Drop the session, then reload.
	if err := chromedp.Run(p.ctx,
		chromedp.ActionFunc(func(ctx context.Context) error { return cdpnetwork.ClearBrowserCookies().Do(ctx) }),
		chromedp.Reload(),
		chromedp.WaitVisible(row+` .line-mark`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reload after clearing cookies: %v", err)
	}

	if visible() {
		t.Errorf("SENSITIVITY FAILURE: the reveal survived a cleared session cookie. Either the " +
			"probe's visibility check is broken (so the gate's green means nothing), or the reveal " +
			"is not actually session-scoped as the design states.")
	} else {
		t.Logf("control OK: dropping the session cookie loses the reveal, so the probe can detect " +
			"a lost ToggledRows — the gate's green is meaningful")
	}
}

// TestE2E_Issue112_MobileCondition re-runs the gate under the REPORTER'S condition:
// a phone-sized viewport with touch enabled and an iOS Safari user agent. The
// desktop gate passing is not sufficient on its own — the report comes from
// mobile Safari over Tailscale, and the badge/composer already have
// coarse-pointer-specific behavior elsewhere in this UI, so the mobile path is
// not merely a narrower desktop.
//
// (What this still cannot emulate: real WebKit, and Tailscale's WS latency.
// Chromium-with-an-iOS-UA is the closest a chromedp probe gets; recorded as a
// limitation of the verdict rather than papered over.)
func TestE2E_Issue112_MobileCondition(t *testing.T) {
	const reloads = 6
	const iosUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 " +
		"(KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"

	repo := setupFixtureRepo(t)
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,3,3,new,already handled,2026-07-13T12:00:00Z,true,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	p := bootChromeAgainstRepo(t, repo, 390, 844)
	if err := chromedp.Run(p.ctx,
		cdpemulation.SetUserAgentOverride(iosUA),
		cdpemulation.SetDeviceMetricsOverride(390, 844, 3, true),
		cdpemulation.SetTouchEmulationEnabled(true).WithMaxTouchPoints(5),
	); err != nil {
		t.Fatalf("mobile emulation: %v", err)
	}
	p.waitReadyAt(390, 844)
	p.clickFile("edited.go")
	row := `.line-row:has(.line[data-line="3"][data-side="new"])`

	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(row+` .line-mark.is-done`, chromedp.ByQuery),
		chromedp.Click(row+` .line-marks`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("mobile: revealing the resolved comment via its badge: %v\nstderr: %s", err, p.stderr.String())
	}

	visible := func() bool {
		var n int
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`[...document.querySelectorAll('`+row+` .inline-comment')].filter(e=>e.offsetParent).length`, &n))
		return n > 0
	}
	seq := []bool{visible()}
	for i := 1; i <= reloads; i++ {
		if err := chromedp.Run(p.ctx,
			chromedp.Reload(),
			chromedp.WaitVisible(row+` .line-mark`, chromedp.ByQuery),
		); err != nil {
			t.Fatalf("mobile reload %d: %v\nstderr: %s", i, err, p.stderr.String())
		}
		seq = append(seq, visible())
	}

	var vis []string
	alternations := 0
	for i, v := range seq {
		vis = append(vis, map[bool]string{true: "shown", false: "HIDDEN"}[v])
		if i > 0 && seq[i] != seq[i-1] {
			alternations++
		}
	}
	t.Logf("#112 mobile probe — visibility across %d reloads: %s", reloads, strings.Join(vis, " "))
	if alternations > 0 {
		t.Errorf("#112 REPRODUCES UNDER MOBILE EMULATION: visibility changed %d time(s) across %d "+
			"reloads.\nsequence: %s\nstderr: %s", alternations, reloads, strings.Join(vis, " "), p.stderr.String())
	}
}
