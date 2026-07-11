//go:build browser

// End-to-end for #149 Phase 4 (reviewer→agent, the loop closes): the reviewer replies
// under a card to steer the agent; the reply RE-SURFACES the comment on the agent's
// snapshot (the unread-reply overlay) even after it was resolved; the agent responds
// and the comment drops back out. Uses `prereview comments --json` as the synchronous
// view of the actionable snapshot (same set `watch` ships).
//
// Run: go test -tags=browser -run TestE2E_ReviewerReply ./e2e/...

package e2e

import (
	"encoding/json"
	"os/exec"
	"slices"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_ReviewerReplyRoundTrip(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("app.go")
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "fix the greeting", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v\nstderr: %s", err, p.stderr.String())
	}
	id := p.readCSV()[1][0]

	// actionable returns the ids in the current snapshot (comments --json), plus
	// whether the named id's thread ends with a reviewer entry.
	actionable := func() (ids []string, reviewerLast bool) {
		out, err := exec.Command(p.binary, "comments", "--out", p.repo, "--json").Output()
		if err != nil {
			t.Fatalf("comments --json: %v", err)
		}
		var cs []struct {
			ID     string `json:"id"`
			Thread []struct {
				Author string `json:"author"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(out, &cs); err != nil {
			t.Fatalf("parse comments json: %v\n%s", err, out)
		}
		for _, c := range cs {
			ids = append(ids, c.ID)
			if c.ID == id && len(c.Thread) > 0 && c.Thread[len(c.Thread)-1].Author == "reviewer" {
				reviewerLast = true
			}
		}
		return ids, reviewerLast
	}

	// 1) Fresh comment is actionable.
	if got, _ := actionable(); !slices.Contains(got, id) {
		t.Fatalf("fresh comment should be actionable; snapshot ids=%v", got)
	}

	// 2) Resolve it, then Show-resolved so it stays visible to reply on. It drops
	//    out of the snapshot (resolved, no thread).
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-comment button[name='toggleResolved']`, chromedp.ByQuery),
		chromedp.Sleep(300e6),
	); err != nil {
		t.Fatalf("resolve: %v\nstderr: %s", err, p.stderr.String())
	}
	p.openViewItem("toggleShowResolved")
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.inline-comment.is-resolved`, chromedp.ByQuery)); err != nil {
		t.Fatalf("show resolved: %v", err)
	}
	if got, _ := actionable(); slices.Contains(got, id) {
		t.Fatalf("resolved comment (no thread) should NOT be actionable; snapshot ids=%v", got)
	}

	// 3) Reviewer replies to steer — the comment RE-SURFACES with the reviewer's
	//    reply, and the card shows "awaiting agent".
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-comment button[name='openReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.reply-form textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.reply-form textarea`, "actually, use a warmer greeting", chromedp.ByQuery),
		chromedp.Click(`button[name='postReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment .thread .thread-reviewer .thread-body`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment .awaiting-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reviewer reply: %v\nstderr: %s", err, p.stderr.String())
	}
	if got, revLast := actionable(); !slices.Contains(got, id) || !revLast {
		t.Fatalf("after the reviewer reply, the resolved comment must re-surface with a reviewer-last thread; ids=%v reviewerLast=%v", got, revLast)
	}

	// 4) The agent responds (`prereview reply`) — the comment drops back out
	//    (agent-last, awaiting the reviewer), and the response renders live.
	if out, err := exec.Command(p.binary, "reply", "--out", p.repo, "--body", "Done — warmer greeting applied.", id).CombinedOutput(); err != nil {
		t.Fatalf("agent reply: %v\n%s", err, out)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-comment .thread .thread-agent .thread-body`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("agent reply never rendered: %v\nstderr: %s", err, p.stderr.String())
	}
	if got, _ := actionable(); slices.Contains(got, id) {
		t.Fatalf("after the agent responds, the comment should drop out (agent-last); ids=%v", got)
	}
}
