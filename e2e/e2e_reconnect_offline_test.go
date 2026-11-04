//go:build browser

// End-to-end for livetemplate/client#160: a page that loses its socket while the
// network is down must go live again once the network returns, with no reload.
// Before the fix, the heartbeat's reconnect ran while offline, fell back to
// HTTP, and the client never tried the WebSocket again: a phone that slept
// through a state change kept showing the old state until a refresh.
//
// Run: go test -tags=browser -run 'TestE2E_ReconnectAfterOffline' ./e2e/...

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/internal/review"
)

func setOffline(t *testing.T, p *runningPrereview, offline bool) {
	t.Helper()
	if err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.EmulateNetworkConditions(offline, 0, -1, -1).Do(ctx)
	})); err != nil {
		t.Fatalf("emulate offline=%v: %v", offline, err)
	}
}

// consoleShows reports whether the captured browser console contains want.
func consoleShows(diag func() string, want string) bool {
	d := diag()
	start := strings.Index(d, "\n--- console ---\n")
	end := strings.Index(d, "\n--- ws ---\n")
	return start >= 0 && end > start && strings.Contains(d[start:end], want)
}

func TestE2E_ReconnectAfterOffline(t *testing.T) {
	p, diag := cursorHarness(t)

	// Live: an attached watch, so the dot is solid green.
	stop := heartbeat(t, p, 0, 500*time.Millisecond)
	waitPhase(t, p, "watching", "Agent connected", 5*time.Second, diag)

	// The network drops. Stay offline until the client has actually fallen back
	// to HTTP — the latch this test is about. A fixed sleep shorter than the
	// heartbeat (10s) plus its pong deadline (5s) would never reach it, and the
	// test would pass against the broken client.
	setOffline(t, p, true)
	deadline := time.Now().Add(40 * time.Second)
	for !consoleShows(diag, "falling back to HTTP") {
		if time.Now().After(deadline) {
			setOffline(t, p, false)
			t.Fatalf("client never fell back to HTTP while offline%s", diag())
		}
		time.Sleep(200 * time.Millisecond)
	}

	// While the page is offline, the server state changes: the agent took the
	// batch and its watch exited.
	stop()
	stampCursor(t, p, review.AgentCursor{Mode: review.AgentCursorWatch, State: review.AgentCursorDelivered, DeliveredSeq: 0})

	// The network returns. Without a reload, the page must catch up. The bound
	// covers one heartbeat in case the browser's `online` event never fires.
	setOffline(t, p, false)
	took := waitPhase(t, p, "working", "Agent picked up your queue at", 20*time.Second, diag)
	t.Logf("caught up %v after the network returned", took.Round(100*time.Millisecond))
}
