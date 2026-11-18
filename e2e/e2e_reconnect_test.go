//go:build browser

package e2e

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// TestE2E_HeartbeatReconnectsStuckPill guards the live-test "stuck working
// pill" across a server restart. The WebSocket drops (the server is killed)
// while the tab stays foreground, and the agent's working→done lands during the
// outage. The liveness heartbeat (data-lvt-heartbeat-ms) must notice the dead
// socket. The restarted server has a new launch token, so the tab's cookie no
// longer admits it, and the tab stays on the stale pill until the reviewer opens
// the newly printed link. Once they switch back, the tab reconnects, the
// server's push-on-connect render clears the pill with no reload, and it stays
// connected: a broken __ping__ pong would loop the socket every interval.
// Heartbeat-only recovery against the same server, with no tab switch, is
// TestE2E_ReconnectAfterOffline.
func TestE2E_HeartbeatReconnectsStuckPill(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")

	var console []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				}
			}
			console = append(console, strings.Join(parts, " "))
		}
	})
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")

	// Shrink the heartbeat to 2s for a fast, deterministic test (production ships
	// 10s). TS `private` isn't enforced at runtime, so we re-arm via the exposed
	// client instance — same code path autoInit uses.
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
		const c = window.liveTemplateClient;
		c.stopHeartbeat(); c.heartbeatMs = 2000; c.startHeartbeat();
		return c.heartbeatMs;
	})()`, nil)); err != nil {
		t.Fatalf("re-arm heartbeat: %v\nstderr: %s", err, p.stderr.String())
	}

	pillVisibleJS := `(() => { const e = document.querySelector('.toast.llm-working'); return !!e && getComputedStyle(e).display !== 'none'; })()`

	// Agent starts working → pill shows live (confirms the WS is live pre-drop).
	writeLLMStatusFile(t, p.repo, `{"state":"working","message":"editing edited.go"}`)
	waitJSTrue(t, p.ctx, pillVisibleJS, 8*time.Second, "pill shows before drop")

	// Capture the port, then KILL the server — the socket drops mid-work.
	ready, err := url.Parse(p.url)
	if err != nil {
		t.Fatalf("parse READY %q: %v", p.url, err)
	}
	port := ready.Port()
	_ = p.cmd.Process.Kill()
	_, _ = p.cmd.Process.Wait()

	// The agent finishes during the outage (written to disk; survives the kill).
	writeLLMStatusFile(t, p.repo, `{"state":"done"}`)

	// Restart prereview on the SAME port + repo (a later --port wins over the
	// harness's --port 0), so the client's heartbeat can reach it. startPrereview
	// returns once READY is printed, so the port is bound before the heartbeat's
	// reconnect attempt races it.
	restarted, _, restart, restartLog := startPrereview(t, p.binary, p.repo, "--agent", "--port", port)
	t.Cleanup(func() { _ = restart.Process.Kill(); _, _ = restart.Process.Wait() })
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("--- restarted server ---\n%s%s", restartLog.String(), diag())
		}
	})

	// The restart minted a new launch token, so the tab's cookie no longer
	// admits it: through several heartbeats it stays on the stale pill.
	time.Sleep(6 * time.Second)
	var stuck bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(pillVisibleJS, &stuck)); err != nil {
		t.Fatalf("pre-link check: %v", err)
	}
	if !stuck {
		t.Fatal("the tab reconnected to a restarted server without its new token")
	}

	// The reviewer opens the newly printed link in another tab, then switches
	// back. That tab's cookie covers the host, and becoming visible makes the
	// first tab retry at once (its timed retries are backed off by now), so it
	// reconnects and the server's on-connect render (status=done) clears the
	// pill, with NO reload.
	second, closeSecond := chromedp.NewContext(p.ctx)
	if err := chromedp.Run(second,
		chromedp.Navigate(restarted),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open the restarted server's link: %v", err)
	}
	closeSecond()
	if err := chromedp.Run(p.ctx, page.BringToFront()); err != nil {
		t.Fatalf("switch back to the first tab: %v", err)
	}
	waitJSTrue(t, p.ctx, "!"+pillVisibleJS, 25*time.Second, "pill clears via a reconnect (no reload)")

	// Prove the reconnect is STABLE: if the restarted server didn't answer
	// __ping__, the heartbeat would tear the socket down every 2s. Assert the
	// working pill does not reappear and the connection holds for several cycles.
	time.Sleep(7 * time.Second)
	var pillBack bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(pillVisibleJS, &pillBack)); err != nil {
		t.Fatalf("post-recovery check: %v", err)
	}
	if pillBack {
		t.Errorf("pill reappeared after recovery — the reconnect is not stable (server pong likely not handled)")
	}

	dead := 0
	for _, l := range console {
		if strings.Contains(l, "socket dead") {
			dead++
		}
	}
	t.Logf("heartbeat declared the socket dead %d time(s); recovered and held stable", dead)
	if dead == 0 {
		t.Errorf("expected the heartbeat to detect the dead socket at least once (console had no 'socket dead')")
	}
}
