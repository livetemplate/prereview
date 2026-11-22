//go:build browser

package e2e

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// TestE2E_FlipUpdateSize (#273): the agent's "working" toast is an {{if}} inside
// the page body, which sits in one {{if .ExternalMode}}…{{else}}. livetemplate
// v0.25.10 resent the whole body each time such an {{if}} flipped (about 40 KB
// here, 420 KB on a large review), so every `prereview status working` resent the
// page. v0.25.14 sends only the flipped slot. Each reply frame must stay small,
// and the page must still show the toast appearing and going, since a
// mis-applied smaller update would break the page.
func TestE2E_FlipUpdateSize(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1200, 800, "--agent")
	diag := captureDiag(t, p)

	var mu sync.Mutex
	var frames []int
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*network.EventWebSocketFrameReceived); ok {
			mu.Lock()
			frames = append(frames, len(e.Response.PayloadData))
			mu.Unlock()
		}
	})
	p.waitReady()
	p.clickFile("edited.go")

	mark := func() int { mu.Lock(); defer mu.Unlock(); return len(frames) }
	// largestSince lets the reply settle, then returns the largest frame
	// received after from.
	largestSince := func(from int) int {
		time.Sleep(700 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		big := 0
		for _, n := range frames[from:] {
			big = max(big, n)
		}
		return big
	}
	const limit = 4 << 10 // the toast is a few hundred bytes; the old resend was ~40 KB
	toast := `!!document.querySelector('.toast.llm-working')`

	// Round 0 is a warm-up: the agent's first "working" also resends the code
	// lines once (a separate, one-time change, not a flip), so only later rounds
	// are measured.
	for round := range 3 {
		for _, st := range []struct{ state, want, what string }{
			{"working", toast, "the working toast appears"},
			{"", "!" + toast, "the working toast goes"},
		} {
			from := mark()
			writeLLMStatusFile(t, p.repo, fmt.Sprintf(`{"state":%q,"message":"Applying your review"}`, st.state))
			waitJSTrueDiag(t, p.ctx, st.want+` && document.querySelectorAll('.code .line').length > 2`, 5*time.Second,
				fmt.Sprintf("round %d: %s and the code stays", round, st.what), diag)
			if n := largestSince(from); round > 0 && n > limit {
				t.Errorf("round %d: %s in a %d-byte frame, want under %d%s", round, st.what, n, limit, diag())
			}
		}
	}
}
