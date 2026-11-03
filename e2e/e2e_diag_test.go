//go:build browser

package e2e

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// captureDiag wires browser console + WebSocket frame capture onto p's tab and
// returns a dump of the four things a failing e2e needs: the server log, the
// console, the WS traffic, and the rendered HTML. Frames and HTML are clipped
// (and Cloned, so a stored clip never pins a huge payload).
func captureDiag(t *testing.T, p *runningPrereview) func() string {
	t.Helper()
	var mu sync.Mutex
	var consoleLines, wsFrames []string
	clip := func(s string) string { return strings.Clone(clipFrame(s, 400)) }
	chromedp.ListenTarget(p.ctx, func(ev any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		case *cdpnetwork.EventWebSocketFrameReceived:
			wsFrames = append(wsFrames, "recv "+clip(e.Response.PayloadData))
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+clip(e.Response.PayloadData))
		}
	})
	if err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.Enable().Do(ctx)
	})); err != nil {
		t.Fatalf("enable network domain: %v", err)
	}
	return func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), clip(html))
	}
}
