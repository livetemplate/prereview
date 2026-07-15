//go:build browser

// End-to-end for #164 (secondary ask): while the agent is working, the live toast tells
// the reviewer their REPLY is being applied — not just a generic "working". Reuses the
// existing .toast.llm-working pill; the reply-aware text comes from AgentWorkingLabel and
// shows only while llm-status=working (the agent is simulated by writing the status file).
//
// Run: go test -tags=browser -run TestE2E_ReplyApplyingPill ./e2e/...

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_ReplyApplyingPill(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("app.go")

	// A comment, then a reviewer reply on it → one reply awaiting the agent.
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "reconsider the greeting", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
		chromedp.Click(`.inline-comment button[name='openReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.reply-form textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.reply-form textarea`, "use a warmer tone", chromedp.ByQuery),
		chromedp.Click(`button[name='postReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment .awaiting-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("comment + reply: %v\nstderr: %s", err, p.stderr.String())
	}

	toastMsg := func() (visible bool, text string) {
		_ = chromedp.Run(p.ctx,
			chromedp.Evaluate(`(()=>{const e=document.querySelector('.toast.llm-working');return !!e&&getComputedStyle(e).display!=='none'})()`, &visible),
			chromedp.Evaluate(`(document.querySelector('.toast.llm-working .toast-msg')||{}).textContent||''`, &text),
		)
		return
	}

	// Idle: no working toast yet.
	if vis, _ := toastMsg(); vis {
		t.Fatalf("working toast visible before the agent started%s", "\nstderr: "+p.stderr.String())
	}

	// Agent starts working with NO explicit message → the toast must say it's applying the
	// reply (the reply-aware fallback), not the generic handoff text.
	writeLLMStatusFile(t, p.repo, `{"state":"working"}`)
	waitJSTrue(t, p.ctx,
		`(()=>{const e=document.querySelector('.toast.llm-working .toast-msg');return !!e&&/Applying your reply/.test(e.textContent)})()`,
		4*time.Second, "reply-applying toast")
	if _, txt := toastMsg(); txt != "Applying your reply…" {
		t.Fatalf("toast text = %q, want %q", txt, "Applying your reply…")
	}

	// An explicit agent message still wins (existing behavior preserved).
	writeLLMStatusFile(t, p.repo, `{"state":"working","message":"editing app.go"}`)
	waitJSTrue(t, p.ctx,
		`(()=>{const e=document.querySelector('.toast.llm-working .toast-msg');return !!e&&e.textContent==='editing app.go'})()`,
		4*time.Second, "explicit-message toast")

	// Done → the toast clears.
	writeLLMStatusFile(t, p.repo, `{"state":"done"}`)
	waitPillGone(t, p.ctx,
		`(()=>{const e=document.querySelector('.toast.llm-working');return !!e&&getComputedStyle(e).display!=='none'})()`,
		4*time.Second, "toast after done")
}
