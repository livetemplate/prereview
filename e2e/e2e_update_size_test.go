//go:build browser

package e2e

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
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

	for round := range 2 { // on and off twice: every flip, not just the first
		for _, st := range []struct{ state, want, what string }{
			{"working", toast, "the working toast appears"},
			{"", "!" + toast, "the working toast goes"},
		} {
			from := mark()
			writeLLMStatusFile(t, p.repo, fmt.Sprintf(`{"state":%q,"message":"Applying your review"}`, st.state))
			waitJSTrueDiag(t, p.ctx, st.want+` && document.querySelectorAll('.code .line').length > 2`, 5*time.Second,
				fmt.Sprintf("round %d: %s and the code stays", round, st.what), diag)
			if n := largestSince(from); n > limit {
				t.Errorf("round %d: %s in a %d-byte frame, want under %d%s", round, st.what, n, limit, diag())
			}
		}
	}
}

// TestE2E_CursorUpdateSize: each code line is a range item with an is-cursor
// conditional, so moving the keyboard cursor flips an {{if}} inside one or two
// kept items. livetemplate before v0.25.15 resent every line for that: about
// 490 KB per ArrowDown on a 300-line file, twice (the cursor reply, then the
// viewport report). Now each item flip is a small ["u"]. Each reply frame must
// stay under 8 KB, and the cursor must land on the right line.
func TestE2E_CursorUpdateSize(t *testing.T) {
	repo := setupFixtureRepo(t)
	var src strings.Builder
	src.WriteString("package edited\n\n")
	for i := range 300 {
		fmt.Fprintf(&src, "func F%d() int { return %d }\n", i, i)
	}
	mustWrite(t, repo, "edited.go", src.String())
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
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
	time.Sleep(time.Second)

	const limit = 8 << 10
	for step := 1; step <= 4; step++ {
		mu.Lock()
		from := len(frames)
		mu.Unlock()
		ctx, cancel := within(p, 10*time.Second)
		err := chromedp.Run(ctx, chromedp.KeyEvent(kb.ArrowDown))
		cancel()
		if err != nil {
			t.Fatalf("ArrowDown %d: %v%s", step, err, diag())
		}
		waitJSTrueDiag(t, p.ctx, fmt.Sprintf(`(document.querySelector('.code .line.is-cursor') || {dataset: {}}).dataset.line === '%d'`, step),
			5*time.Second, fmt.Sprintf("ArrowDown %d puts the cursor on line %d", step, step), diag)
		time.Sleep(700 * time.Millisecond) // the viewport report that follows
		mu.Lock()
		big := 0
		for _, n := range frames[from:] {
			big = max(big, n)
		}
		mu.Unlock()
		if big > limit {
			t.Errorf("ArrowDown %d sent a %d-byte frame, want under %d%s", step, big, limit, diag())
		}
	}
}

