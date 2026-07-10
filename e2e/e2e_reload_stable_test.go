//go:build browser

// Strengthened regression guard for #112 ("resolved comments toggle on manual
// reload"). The original guard (e2e_reload_test.go) couldn't see the reported bug;
// this widens coverage to the conditions it omitted and pins down the stability we
// DO have so a future regression is caught.
//
// The reporter reviews from a phone over Tailscale (high WS-connect latency), so the
// SSR paint is visible for a real beat before the WS morph. The load-bearing check
// here isolates the PURE SSR render (JavaScript disabled ⇒ the WebSocket never
// connects ⇒ no morph) and asserts it already matches the WS-hydrated render — for a
// resolved comment on a NON-FIRST changed file, the case most likely to expose an
// SSR-vs-persisted-view divergence. It does NOT, on current HEAD: SSR honours the
// persisted file selection + resolved state, so there is nothing to flash.
//
// RULED OUT here (could not reproduce a toggle): non-first-file persisted selection,
// pure-SSR vs WS-hydrated divergence, agent mode, and rapid repeated reloads — in
// both --agent and plain mode. CDP's Network.emulateNetworkConditions does NOT
// throttle WebSocket frames, so true WS latency can't be simulated in-process; the
// JS-off probe is the deterministic stand-in (SSR with no hydration at all).
//
// Run: go test -tags=browser -run TestE2E_ResolvedReload ./e2e/...

package e2e

import (
	"testing"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

func setupTwoChangedFilesRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "seed.txt", "seed\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	// Two NEW (added) files. `a_other.go` sorts first; the user's resolved comment
	// goes on `target.go`, a later file — so if the SSR auto-selected the first
	// changed file (the hypothesised bug), it would render a_other.go, not target.go.
	mustWrite(t, dir, "a_other.go", "package other\n\nfunc Other() {}\n")
	mustWrite(t, dir, "target.go", "package target\n\nfunc Target() {}\n\nfunc Line5() {}\n")
	return dir
}

// resolvedReloadStable seeds a resolved comment on a non-first file, then asserts the
// pure-SSR render (JS off) and the WS-hydrated render agree — across several reloads.
func resolvedReloadStable(t *testing.T, extraArgs ...string) {
	p := bootChromeAgainstRepo(t, setupTwoChangedFilesRepo(t), 1200, 800, extraArgs...)
	p.waitReady()

	p.clickFile("target.go")
	p.clickLine(0, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "resolve me on target", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
		chromedp.Click(`button[name='toggleResolved']`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("seed resolved comment: %v\nstderr: %s", err, p.stderr.String())
	}
	p.openViewItem("toggleShowResolved")
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.inline-comment.is-resolved`, chromedp.ByQuery)); err != nil {
		t.Fatalf("show resolved: %v\nstderr: %s", err, p.stderr.String())
	}

	read := func(label string) (file string, resolved bool, comments int) {
		if err := chromedp.Run(p.ctx,
			chromedp.Navigate(p.url),
			chromedp.WaitVisible(`.fh-base`, chromedp.ByQuery),
			chromedp.Evaluate(`(document.querySelector('.fh-base')?.textContent||'').trim()`, &file),
			chromedp.Evaluate(`!!document.querySelector('.inline-comment.is-resolved')`, &resolved),
			chromedp.Evaluate(`document.querySelectorAll('.inline-comment').length`, &comments),
		); err != nil {
			t.Fatalf("%s read: %v\nstderr: %s", label, err, p.stderr.String())
		}
		return
	}

	// Several reload rounds; each compares the pure SSR paint to the hydrated DOM.
	for round := 0; round < 4; round++ {
		// PURE SSR: JS off ⇒ the WebSocket never connects ⇒ the DOM is exactly what
		// the server rendered for the GET (no morph).
		if err := chromedp.Run(p.ctx, emulation.SetScriptExecutionDisabled(true)); err != nil {
			t.Fatalf("disable JS: %v", err)
		}
		ssrFile, ssrResolved, ssrComments := read("SSR")

		// POST-HYDRATION: JS on ⇒ the WS connects and morphs.
		if err := chromedp.Run(p.ctx, emulation.SetScriptExecutionDisabled(false)); err != nil {
			t.Fatalf("re-enable JS: %v", err)
		}
		wsFile, wsResolved, wsComments := read("WS")

		t.Logf("round %d: SSR{file=%q resolved=%v n=%d}  WS{file=%q resolved=%v n=%d}",
			round, ssrFile, ssrResolved, ssrComments, wsFile, wsResolved, wsComments)

		if ssrFile != wsFile {
			t.Errorf("#112 round %d: SSR renders file %q but WS hydrates to %q — the persisted selection isn't in the SSR paint",
				round, ssrFile, wsFile)
		}
		if ssrResolved != wsResolved || ssrComments != wsComments {
			t.Errorf("#112 round %d: SSR shows resolved=%v/%d comments, WS shows resolved=%v/%d — they diverge",
				round, ssrResolved, ssrComments, wsResolved, wsComments)
		}
		if !wsResolved || wsComments != 1 {
			t.Errorf("#112 round %d: expected the resolved comment stable (resolved=true, 1 comment), got resolved=%v/%d",
				round, wsResolved, wsComments)
		}
	}
}

// Plain (non-agent) review — the reporter's most likely mode.
func TestE2E_ResolvedReloadStable(t *testing.T) {
	resolvedReloadStable(t)
}

// Agent mode runs the llm-status / processed watchers and the live re-render path,
// a plausible reporter condition the original guard never exercised.
func TestE2E_ResolvedReloadStableAgent(t *testing.T) {
	resolvedReloadStable(t, "--agent")
}
