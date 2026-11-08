//go:build browser

// #205: the agent settles a comment by replying with no edit. `prereview reply
// --answered` must reach the reviewer live as a distinct outcome — the card and
// the queue read "answered" (no ↺), no stall banner fires while the agent waits —
// and a reviewer reply hands the comment back to the agent. Plus the ↺ fix found
// while planning it: re-enqueueing a done comment the agent also replied on must
// put it back in the agent's snapshot.
//
// Run: go test -tags=browser -run 'TestE2E_AnsweredOutcome|TestE2E_ReenqueueAfterReply' ./e2e/...

package e2e

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/internal/review"
)

// lastSnapshotHas reports whether the newest snapshot hands the agent comment id
// with a thread whose last entry is the reviewer's and contains bodyHas.
func lastSnapshotHas(id, bodyHas string) func([]review.StreamEvent) bool {
	return func(evs []review.StreamEvent) bool {
		snaps := handoffEvents(evs)
		if len(snaps) == 0 {
			return false
		}
		for _, c := range snaps[len(snaps)-1].CommentList() {
			if c.ID == id && len(c.Thread) > 0 {
				last := c.Thread[len(c.Thread)-1]
				return last.Author == review.AuthorReviewer && strings.Contains(last.Body, bodyHas)
			}
		}
		return false
	}
}

// within bounds a browser wait, so a regression fails the test with diagnostics
// instead of hanging until the package timeout (WaitVisible has no deadline).
func within(p *runningPrereview, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(p.ctx, d)
}

func countSel(t *testing.T, p *runningPrereview, sel string) int {
	t.Helper()
	return evalInt(t, p, `document.querySelectorAll(`+"`"+sel+"`"+`).length`)
}

func TestE2E_AnsweredOutcome(t *testing.T) {
	t.Setenv("PREREVIEW_AGENT_STALE", "2s")
	t.Setenv("PREREVIEW_AGENT_STALL_AFTER", "3s")
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")
	addLineComment(t, p, 3, 3, "why return a literal here?")
	id := p.readCSV()[1][0]
	// The agent read the queue and is now off working (not attached) — the state in
	// which silence past AgentStallAfter reads as a stall when work is outstanding.
	stampCursor(t, p, review.AgentCursor{Mode: review.AgentCursorWatch, State: review.AgentCursorDelivered, DeliveredSeq: 1000})

	if out, err := exec.Command(p.binary, "reply", "--out", p.repo, "--answered",
		"--body", "It is a fixture: the literal is the point.", id).CombinedOutput(); err != nil {
		t.Fatalf("prereview reply --answered: %v\n%s", err, out)
	}
	ctx, cancel := within(p, 15*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`.inline-comment .answered-badge`, chromedp.ByQuery)); err != nil {
		t.Fatalf("answered badge never appeared live: %v%s", err, diag())
	}
	if n := countSel(t, p, `.inline-comment .processed-badge`); n != 0 {
		t.Fatalf("an answered comment must not read 'worked on' (%d badges)%s", n, diag())
	}
	if n := countSel(t, p, `.queue-row.queue-answered`); n != 1 {
		t.Fatalf("want one answered queue row, got %d%s", n, diag())
	}
	if n := countSel(t, p, `.queue-row.queue-answered .queue-reenqueue`); n != 0 {
		t.Fatalf("an answered row must have no ↺ (it would poison the done netting)%s", diag())
	}
	legend := evalStr(t, p, `(document.querySelector('.queue-legend')||{}).textContent||""`)
	if !strings.Contains(legend, "1 answered") || !strings.Contains(legend, "0 queued") {
		t.Fatalf("legend %q, want 0 queued and 1 answered%s", legend, diag())
	}

	// The agent is quiet past AgentStallAfter, but it owes nothing: no stall.
	time.Sleep(5 * time.Second)
	if stallPromptShown(p) {
		phase, label := agentPhaseShown(p)
		t.Fatalf("stall prompt on an answered queue (phase %q, %q)%s", phase, label, diag())
	}

	// The reviewer pushes back → queued again, and the agent is handed it.
	ctx2, cancel2 := within(p, 15*time.Second)
	defer cancel2()
	if err := chromedp.Run(ctx2,
		chromedp.Click(`.inline-comment button[name='openReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.reply-form textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.reply-form textarea`, "make it a constant anyway", chromedp.ByQuery),
		chromedp.Click(`button[name='postReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment .awaiting-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reviewer reply: %v%s", err, diag())
	}
	if n := countSel(t, p, `.inline-comment .answered-badge`); n != 0 {
		t.Fatalf("an answered badge must give way once the reviewer replies%s", diag())
	}
	if n := countSel(t, p, `.queue-row.queue-queued`); n != 1 {
		t.Fatalf("the pushed-back comment must read queued, got %d queued rows%s", n, diag())
	}
	waitDurable(t, p, lastSnapshotHas(id, "make it a constant"), "the pushback in the agent's snapshot", diag)
}

func TestE2E_ReenqueueAfterReply(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")
	addLineComment(t, p, 3, 3, "rename this")
	id := p.readCSV()[1][0]
	// Let the comment's own snapshot land first. A save during an in-flight emit arms
	// no new one (#203 D1, planned separately), and done/reply emit nothing, so a ↺
	// racing this emit would never reach the stream.
	waitDurable(t, p, func(evs []review.StreamEvent) bool { return len(handoffEvents(evs)) > 0 }, "the comment's first snapshot", diag)

	// The normal finish: done, then a reply saying what changed.
	for _, args := range [][]string{
		{"done", "--out", p.repo, id},
		{"reply", "--out", p.repo, "--body", "Renamed it.", id},
	} {
		if out, err := exec.Command(p.binary, args...).CombinedOutput(); err != nil {
			t.Fatalf("prereview %s: %v\n%s", args[0], err, out)
		}
	}
	ctx, cancel := within(p, 15*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.inline-comment .processed-badge`, chromedp.ByQuery),
		chromedp.WaitReady(`.queue-row.queue-done .queue-reenqueue`, chromedp.ByQuery),
		// The ↺ lives in the closed Queue dropdown; submit it without opening it.
		chromedp.Evaluate(`document.querySelector('.queue-row.queue-done .queue-reenqueue').click()`, nil),
		chromedp.WaitVisible(`.inline-comment .awaiting-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("done → reply → ↺: %v%s", err, diag())
	}
	waitDurable(t, p, lastSnapshotHas(id, "Re-enqueued"), "the re-enqueued comment back in the agent's snapshot", diag)
}
