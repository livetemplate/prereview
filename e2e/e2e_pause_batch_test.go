//go:build browser

// End-to-end: the pause → batch → resume contract in --agent mode. Pausing the
// queue HOLDS snapshot emissions (comments still persist to disk); resuming
// delivers ONE coalesced snapshot of everything queued. This is what makes the
// agent's `watch` block while the reviewer batches, then hand it the whole batch
// as a single set — the behaviour SKILL.md documents.
// Run with: go test -tags=browser -run TestE2E_PauseBatchResume ./e2e/...

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/internal/review"
)

func TestE2E_PauseBatchResume(t *testing.T) {
	p, stdoutBuf, _ := bootChromeStream(t)
	p.waitReady()
	p.clickFile("edited.go")

	// Toggle pause via the #118 "q" window binding. Blur any focused input first
	// so the key reaches the window handler instead of typing into a textarea.
	togglePause := func() {
		if err := chromedp.Run(p.ctx,
			chromedp.Evaluate(`document.activeElement&&document.activeElement.blur&&document.activeElement.blur()`, nil),
			chromedp.KeyEvent("q"),
		); err != nil {
			t.Fatalf("toggle pause: %v", err)
		}
	}
	waitPaused := func(want bool) {
		for i := 0; i < 40; i++ {
			var present bool
			_ = chromedp.Run(p.ctx, chromedp.Evaluate(`!!document.querySelector('.queue-dropdown.is-paused')`, &present))
			if present == want {
				return
			}
			time.Sleep(75 * time.Millisecond)
		}
		t.Fatalf("queue paused-state never became %v", want)
	}

	// Pause, then queue two comments while paused.
	togglePause()
	waitPaused(true)
	addLineComment(t, p, 3, 3, "batched note one")
	addLineComment(t, p, 0, 4, "batched note two")

	// Well past the ~400ms emit debounce: a paused queue must emit NO snapshot.
	time.Sleep(1300 * time.Millisecond)
	if snaps := handoffEvents(parseStreamEvents(stdoutBuf.String())); len(snaps) != 0 {
		t.Fatalf("paused queue emitted %d snapshot(s); want 0 (emissions held until resume)\n--- stdout ---\n%s",
			len(snaps), stdoutBuf.String())
	}

	// Resume → exactly one coalesced snapshot carrying BOTH batched comments.
	togglePause()
	waitPaused(false)
	waitStream(t, stdoutBuf, func(evs []review.StreamEvent) bool {
		s := handoffEvents(evs)
		return len(s) >= 1 && len(s[len(s)-1].CommentList()) == 2
	}, "one coalesced snapshot with both batched comments after resume", func() string {
		return "\n--- stdout ---\n" + stdoutBuf.String() + "\n--- server ---\n" + p.stderr.String()
	})
}
