//go:build browser

package e2e

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// TestE2E_HeartbeatReconnectsStuckPill is the end-to-end reproduction+fix guard
// for the live-test "stuck working pill". The WebSocket drops (here: the server
// is killed) while the tab stays FOREGROUND and the agent's working→done lands
// during the outage. With no heartbeat, the client — autoReconnect off, only
// visibilitychange triggers a reconnect — sits frozen on the working pill. The
// liveness heartbeat (data-lvt-heartbeat-ms) must notice the dead socket, send
// pings the RESTARTED server answers with {pong:true}, reconnect, and let the
// server's push-on-connect render clear the pill — all with no reload or tab
// switch. Staying connected afterwards proves the server actually handles the
// __ping__ action (a broken pong would loop the socket every interval).
func TestE2E_HeartbeatReconnectsStuckPill(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--skill")

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
	port := p.url[strings.LastIndex(p.url, ":")+1:]
	_ = p.cmd.Process.Kill()
	_, _ = p.cmd.Process.Wait()

	// The agent finishes during the outage (written to disk; survives the kill).
	writeLLMStatusFile(t, p.repo, `{"state":"done"}`)

	// Restart prereview on the SAME port + repo, so the client's heartbeat can
	// reconnect to it. Mirror startPrereview's flags/env.
	restart := exec.Command(p.binary, "--base", "HEAD", "--port", port, "--host", "127.0.0.1", "--skill", p.repo)
	restart.Env = prefsIsolatedEnv(p.repo)
	stdout, err := restart.StdoutPipe()
	if err != nil {
		t.Fatalf("restart stdout pipe: %v", err)
	}
	if err := restart.Start(); err != nil {
		t.Fatalf("restart server: %v", err)
	}
	t.Cleanup(func() { _ = restart.Process.Kill(); _, _ = restart.Process.Wait() })
	// Wait for the restarted server's READY so the port is bound before the
	// heartbeat's reconnect attempt races it.
	readyCh := make(chan struct{}, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "READY") {
				readyCh <- struct{}{}
				return
			}
		}
	}()
	select {
	case <-readyCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("restarted server never printed READY")
	}

	// The heartbeat (2s) must notice the dead socket, reconnect to the restarted
	// server, and the server's on-connect render (status=done) clears the pill —
	// with NO reload and NO visibilitychange.
	waitJSTrue(t, p.ctx,
		`!document.querySelector('.toast.llm-working') || getComputedStyle(document.querySelector('.toast.llm-working')).display === 'none'`,
		25*time.Second, "pill clears on its own via heartbeat reconnect (no reload)")

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
