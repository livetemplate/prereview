//go:build browser

// End-to-end test for the inbound LLM-status signal (issue #78): the agent
// writes .prereview/llm-status.json and the running server shows it live in the
// browser, across every open tab, plus for a tab opened mid-work (Mount path).
//
// The agent is simulated by writing the status file directly — no real LLM is
// needed. Multi-tab is exercised by two chromedp contexts sharing ONE browser
// (chromedp.NewContext(parentTabCtx)), which share the cookie jar and therefore
// the livetemplate session group — exactly "two tabs of the same browser".

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// writeLLMStatusFile writes the agent-status file atomically (temp + rename),
// mirroring what the skill instructs the agent to do.
// waitPillGone polls document.querySelector until the working pill is no longer
// visible, or fails after timeout. Used instead of chromedp.WaitNotPresent/
// WaitNotVisible, which timed out unreliably across multiple tabs in this test
// even when the DOM had already dropped the pill.
func waitPillGone(t *testing.T, ctx context.Context, visibleJS string, timeout time.Duration, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		var visible bool
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = chromedp.Run(cctx, chromedp.Evaluate(visibleJS, &visible))
		cancel()
		if lastErr == nil && !visible {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: working pill still visible after %s (lastErr=%v)", label, timeout, lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitJSTrue polls until the given JS expression evaluates truthy, or fails
// after timeout. Used instead of chromedp.WaitVisible/WaitNotVisible for
// disappearance/content checks, which proved unreliable in these tests.
func waitJSTrue(t *testing.T, ctx context.Context, js string, timeout time.Duration, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		var ok bool
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = chromedp.Run(cctx, chromedp.Evaluate(js, &ok))
		cancel()
		if lastErr == nil && ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: condition never became true within %s (lastErr=%v)", label, timeout, lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func writeLLMStatusFile(t *testing.T, repo, body string) {
	t.Helper()
	dir := filepath.Join(repo, ".prereview")
	tmp := filepath.Join(dir, ".llm-status.tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatalf("write status tmp: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "llm-status.json")); err != nil {
		t.Fatalf("rename status file: %v", err)
	}
}

func TestE2E_LLMStatusMultiTab(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}
	chromium := findChromium(t)

	binary := filepath.Join(t.TempDir(), "prereview")
	build := exec.Command("go", "build", "-o", binary, "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	repo := setupFixtureRepo(t)
	// --skill starts the llm-status watcher (skillMode gates it); it also renders
	// the Hand off button, which we don't use here.
	url, srv, stderr := startPrereview(t, binary, repo, "--skill")
	defer func() {
		_ = srv.Process.Kill()
		_, _ = srv.Process.Wait()
	}()

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.WindowSize(1200, 800),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer allocCancel()

	// Tab 1 owns the browser; tabs 2 and 3 are created from it so they share the
	// cookie jar (same livetemplate session group = same browser, many tabs).
	ctx1, cancel1 := chromedp.NewContext(allocCtx)
	defer cancel1()

	// Observability (per CLAUDE.md): console + WS frames on tab 1, server stderr
	// shared, and a per-tab HTML dump on failure.
	var mu sync.Mutex
	var consoleLines, wsFrames []string
	chromedp.ListenTarget(ctx1, func(ev any) {
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
			wsFrames = append(wsFrames, "recv "+e.Response.PayloadData)
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+e.Response.PayloadData)
		}
	})
	diag := func(ctx context.Context) string {
		var html string
		// Bounded: never let diagnostics hang the test if the tab is wedged.
		dctx, dcancel := context.WithTimeout(ctx, 3*time.Second)
		_ = chromedp.Run(dctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		dcancel()
		mu.Lock()
		defer mu.Unlock()
		return "\n--- server ---\n" + stderr.String() +
			"\n--- console (tab1) ---\n" + strings.Join(consoleLines, "\n") +
			"\n--- ws (tab1) ---\n" + strings.Join(wsFrames, "\n") +
			"\n--- html ---\n" + html
	}

	nav := func(ctx context.Context) error {
		return chromedp.Run(ctx,
			chromedp.ActionFunc(func(ctx context.Context) error { return cdpnetwork.Enable().Do(ctx) }),
			chromedp.EmulateViewport(1200, 800),
			chromedp.Navigate(url),
			chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
			// Let the deferred client parse + open its WebSocket so the tab is a
			// live member of the session group (TriggerAction targets connections).
			chromedp.Sleep(1500*time.Millisecond),
		)
	}

	if err := nav(ctx1); err != nil {
		t.Fatalf("tab1 nav: %v%s", err, diag(ctx1))
	}
	ctx2, cancel2 := chromedp.NewContext(ctx1)
	defer cancel2()
	if err := nav(ctx2); err != nil {
		t.Fatalf("tab2 nav: %v%s", err, diag(ctx2))
	}

	// Idle: neither tab shows the working pill before any status write. The pill
	// node is always in the DOM (stable slot); "shown" means visible, so assert
	// on visibility, not presence.
	pillVisibleJS := `(() => { const e = document.querySelector('.toast.llm-working'); return !!e && getComputedStyle(e).display !== 'none'; })()`
	for i, ctx := range []context.Context{ctx1, ctx2} {
		var visible bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(pillVisibleJS, &visible)); err != nil {
			t.Fatalf("tab%d idle check: %v", i+1, err)
		}
		if visible {
			t.Errorf("tab%d: working pill visible before any status write%s", i+1, diag(ctx))
		}
	}

	// Agent starts working → BOTH open tabs light up live (watcher → fan-out).
	writeLLMStatusFile(t, repo, `{"state":"working","message":"Applying your review"}`)
	for i, ctx := range []context.Context{ctx1, ctx2} {
		wc, cancel := context.WithTimeout(ctx, 6*time.Second)
		var msg string
		err := chromedp.Run(wc,
			chromedp.WaitVisible(`.toast.llm-working`, chromedp.ByQuery),
			chromedp.Text(`.toast.llm-working .toast-msg`, &msg, chromedp.ByQuery),
		)
		cancel()
		if err != nil {
			t.Fatalf("tab%d did not show working pill live: %v%s", i+1, err, diag(ctx))
		}
		if !strings.Contains(msg, "Applying your review") {
			t.Errorf("tab%d pill message = %q, want the agent's message", i+1, msg)
		}
		t.Logf("tab%d showed working pill live", i+1)
	}

	// Agent finishes → the pill clears LIVE on both open tabs. This is issue
	// #78's core requirement, "works across multiple open tabs," in both
	// directions. We poll document.querySelector rather than
	// chromedp.WaitNotPresent (the latter proved unreliable across tabs here).
	// (SSR-baseline tabs — opened/reloaded mid-work — are covered separately by
	// TestE2E_LLMStatusReloadedTabClears and TestE2E_LLMStatusTwoTabJoinerClears;
	// this test stays at two tabs because driving a THIRD chromedp context in one
	// browser is unreliable — a harness limitation, not a product one.)
	writeLLMStatusFile(t, repo, `{"state":"done"}`)
	for i, ctx := range []context.Context{ctx1, ctx2} {
		waitPillGone(t, ctx, pillVisibleJS, 10*time.Second, fmt.Sprintf("tab%d clear-on-done", i+1))
		t.Logf("tab%d cleared working pill live on done", i+1)
	}

	mu.Lock()
	for _, l := range consoleLines {
		if strings.HasPrefix(l, "error ") {
			t.Errorf("browser console error: %s", l)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
}

// TestE2E_LLMStatusReloadedTabClears isolates the SSR-baseline scenario with a
// SINGLE reliably-driven tab (no flaky 3-tab chromedp driving): the tab reloads
// mid-work, so the reloaded page's SSR contains the pill; then the agent finishes
// and the pill must clear LIVE. This is the definitive check for the suspected
// "mid-work-joiner wedge" — if it passes, the client handles an SSR-rendered pill
// cleared over WS just fine (the earlier multi-tab failures were chromedp driving
// a 3rd tab, not a real page wedge).
func TestE2E_LLMStatusReloadedTabClears(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--skill")
	p.waitReady()

	pillVisibleJS := `(() => { const e = document.querySelector('.toast.llm-working'); return !!e && getComputedStyle(e).display !== 'none'; })()`

	// Agent starts working → pill shows live.
	writeLLMStatusFile(t, p.repo, `{"state":"working","message":"working on it"}`)
	waitJSTrue(t, p.ctx, pillVisibleJS, 8*time.Second, "pill shows live")

	// Reload while working → the reloaded page is SERVER-RENDERED with the pill
	// (SSR baseline), then its client reconnects.
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.toast.llm-working`, chromedp.ByQuery),
		chromedp.Sleep(1500*time.Millisecond), // let the reconnected WS settle
	); err != nil {
		t.Fatalf("reload while working: %v\nstderr: %s", err, p.stderr.String())
	}
	t.Logf("reloaded tab shows the SSR-rendered pill")

	// Agent finishes → the pill must clear LIVE on this SSR-baseline tab.
	writeLLMStatusFile(t, p.repo, `{"state":"done"}`)
	waitJSTrue(t, p.ctx,
		`!document.querySelector('.toast.llm-working') || getComputedStyle(document.querySelector('.toast.llm-working')).display === 'none'`,
		10*time.Second, "pill clears live on the reloaded (SSR-baseline) tab")
	t.Logf("SSR-baseline tab cleared the pill live")
}

// TestE2E_LLMStatusTwoTabJoinerClears is the discriminating test for the
// suspected multi-tab wedge: TWO tabs in one browser (reliable to drive; only a
// 3rd chromedp context ever stalled), where tab2 joins MID-WORK (SSR-baseline)
// so the group has multiple connections when the clear fans out. tab2 must clear
// the pill live. Passing this (with the single-tab reload test) makes "no client
// bug — the earlier 3-tab failure was chromedp driving" airtight.
func TestE2E_LLMStatusTwoTabJoinerClears(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}
	chromium := findChromium(t)
	binary := filepath.Join(t.TempDir(), "prereview")
	if out, err := exec.Command("go", "build", "-o", binary, "..").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	repo := setupFixtureRepo(t)
	url, srv, stderr := startPrereview(t, binary, repo, "--skill")
	defer func() { _ = srv.Process.Kill(); _, _ = srv.Process.Wait() }()

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium), chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true), chromedp.Flag("no-sandbox", true),
		chromedp.WindowSize(1200, 800),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer allocCancel()
	ctx1, cancel1 := chromedp.NewContext(allocCtx)
	defer cancel1()

	pillVisibleJS := `(() => { const e = document.querySelector('.toast.llm-working'); return !!e && getComputedStyle(e).display !== 'none'; })()`
	nav := func(ctx context.Context) error {
		return chromedp.Run(ctx,
			chromedp.EmulateViewport(1200, 800), chromedp.Navigate(url),
			chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
			chromedp.Sleep(1500*time.Millisecond),
		)
	}
	if err := nav(ctx1); err != nil {
		t.Fatalf("tab1 nav: %v\nstderr: %s", err, stderr.String())
	}

	// Agent starts working; tab1 (already open) shows the pill live.
	writeLLMStatusFile(t, repo, `{"state":"working","message":"working"}`)
	waitJSTrue(t, ctx1, pillVisibleJS, 8*time.Second, "tab1 pill live")

	// tab2 joins MID-WORK → its SSR carries the pill; two connections now.
	ctx2, cancel2 := chromedp.NewContext(ctx1)
	defer cancel2()
	if err := chromedp.Run(ctx2,
		chromedp.EmulateViewport(1200, 800), chromedp.Navigate(url),
		chromedp.WaitVisible(`.toast.llm-working`, chromedp.ByQuery),
		chromedp.Sleep(1500*time.Millisecond), // let tab2's WS join the group
	); err != nil {
		t.Fatalf("tab2 mid-work nav: %v\nstderr: %s", err, stderr.String())
	}
	t.Logf("tab2 (mid-work joiner) shows the SSR pill")

	// Agent finishes → the pill clears LIVE on BOTH tabs (multi-connection group).
	writeLLMStatusFile(t, repo, `{"state":"done"}`)
	goneJS := `!document.querySelector('.toast.llm-working') || getComputedStyle(document.querySelector('.toast.llm-working')).display === 'none'`
	waitJSTrue(t, ctx1, goneJS, 10*time.Second, "tab1 clears live")
	waitJSTrue(t, ctx2, goneJS, 10*time.Second, "tab2 (SSR-baseline) clears live")
	t.Logf("both tabs cleared live — no client wedge with multiple connections")
}

// TestE2E_LLMStatusRefreshOnDone covers P2: when the agent finishes (status
// working→done) after editing files, an already-open tab shows a non-intrusive
// "Changes applied — Refresh diff" affordance; clicking it reloads the diff to
// the agent's edits and clears the affordance. Single tab, opened before work
// starts (so no SSR-baseline hydration issue — see TestE2E_LLMStatusMultiTab).
func TestE2E_LLMStatusRefreshOnDone(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--skill")
	p.waitReady()
	p.clickFile("edited.go")

	// The fixture's edited.go currently returns "hello world".
	var before string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.code').textContent`, &before)); err != nil {
		t.Fatalf("read initial diff: %v", err)
	}
	if !strings.Contains(before, "hello world") {
		t.Fatalf("pre-edit diff should contain the fixture content 'hello world'; got: %s", before)
	}

	// Agent starts working — wait until the server observes it (pill shows) so
	// the later done write is a genuine working→done transition. (A real agent
	// works for seconds between the two writes; writing them back-to-back would
	// coalesce under the ~0.75s status poll and skip the transition.)
	writeLLMStatusFile(t, p.repo, `{"state":"working","message":"editing edited.go"}`)
	waitJSTrue(t, p.ctx,
		`(() => { const e = document.querySelector('.toast.llm-working'); return !!e && getComputedStyle(e).display !== 'none'; })()`,
		10*time.Second, "working pill appears before edit")

	// Agent edits the file on disk, then finishes.
	mustWrite(t, p.repo, "edited.go", "package edited\n\nfunc Hello() string {\n\treturn \"REFRESHED BY AGENT\"\n}\n")
	writeLLMStatusFile(t, p.repo, `{"state":"done"}`)

	// The refresh affordance appears live (working→done transition).
	refreshVisibleJS := `(() => { const e = document.querySelector('.refresh-prompt'); return !!e && getComputedStyle(e).display !== 'none'; })()`
	waitJSTrue(t, p.ctx, refreshVisibleJS, 10*time.Second, "refresh affordance appears on done")

	// The diff has NOT auto-refreshed yet — the old content is still shown (the
	// affordance is a prompt, not an auto-reload).
	var midway string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.code').textContent`, &midway)); err != nil {
		t.Fatalf("read midway diff: %v", err)
	}
	if strings.Contains(midway, "REFRESHED BY AGENT") {
		t.Errorf("diff should not auto-refresh before the user clicks; got new content already: %s", midway)
	}

	// Click Refresh diff → the diff reloads to the agent's edit, affordance clears.
	if err := chromedp.Run(p.ctx, chromedp.Click(`button[name="refreshDiff"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("click Refresh diff: %v\nstderr: %s", err, p.stderr.String())
	}
	waitJSTrue(t, p.ctx,
		`(document.querySelector('.code')||{}).textContent?.includes('REFRESHED BY AGENT')||false`,
		10*time.Second, "diff shows the agent's edit after refresh")
	waitJSTrue(t, p.ctx,
		`!document.querySelector('.refresh-prompt') || getComputedStyle(document.querySelector('.refresh-prompt')).display === 'none'`,
		10*time.Second, "refresh affordance clears after refresh")

	// Batch boundary — the user handed off again while the agent was working, so
	// after it finishes one batch (refresh bar armed) it immediately starts the
	// queued next batch. The new "working" must SUPERSEDE the stale refresh bar so
	// the working pill and the refresh bar are never shown together (refreshing
	// mid-batch would show a half-applied diff).
	pillJS := `(() => { const e = document.querySelector('.toast.llm-working'); return !!e && getComputedStyle(e).display !== 'none'; })()`
	refreshGoneJS := `!document.querySelector('.refresh-prompt') || getComputedStyle(document.querySelector('.refresh-prompt')).display === 'none'`
	writeLLMStatusFile(t, p.repo, `{"state":"working","message":"batch 2"}`)
	waitJSTrue(t, p.ctx, pillJS, 8*time.Second, "batch 2 working pill shows")
	writeLLMStatusFile(t, p.repo, `{"state":"done"}`)
	waitJSTrue(t, p.ctx, refreshVisibleJS, 10*time.Second, "batch 2 done re-arms the refresh bar")
	writeLLMStatusFile(t, p.repo, `{"state":"working","message":"batch 3"}`)
	waitJSTrue(t, p.ctx, pillJS, 8*time.Second, "batch 3 working pill returns")
	waitJSTrue(t, p.ctx, refreshGoneJS, 8*time.Second, "new working supersedes the stale refresh bar")
}
