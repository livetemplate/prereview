//go:build browser

// End-to-end for #167: a FILE-level comment (kind=file) carries the #149
// conversation thread, in both directions and LIVE (no reload).
//
// Two bugs met here. The card for file/area comments is a different template
// (commentCardSimple) from the line/text card, and it omitted the thread + reply
// surface entirely. Fixing that exposed the second: the card's update never
// reached the browser, because a range whose item statics change shape (the
// thread block renders no slots while the thread is empty) failed livetemplate's
// range matching and its update was silently dropped. So this test asserts the
// thread appears WITHOUT a reload — a reload-based assertion would pass even with
// the update path broken.
//
// Run: go test -tags=browser -run TestE2E_FileCommentThread ./e2e/...

package e2e

import (
	"os/exec"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_FileCommentThread(t *testing.T) {
	// --agent so the server runs WatchLLMStatus — the live agent-signal fan-out.
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("app.go")

	// `c` opens the file-comment modal → a kind=file comment (no line anchor).
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent("c"),
		chromedp.WaitVisible(`.fc-modal textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.fc-modal textarea[name="body"]`, "this whole file needs a rethink", chromedp.ByQuery),
		chromedp.Click(`.fc-modal button[name="addComment"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.file-comments .inline-comment .body`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add file comment: %v\nstderr: %s", err, p.stderr.String())
	}

	rows := p.readCSV()
	if len(rows) < 2 {
		t.Fatalf("expected a comment row, got %v", rows)
	}
	id := rows[1][0]

	// agent → reviewer: `prereview reply` appears LIVE under the file card.
	const note = "Split app.go into handler.go and router.go."
	if out, err := exec.Command(p.binary, "reply", "--out", p.repo, "--body", note, id).CombinedOutput(); err != nil {
		t.Fatalf("prereview reply: %v\n%s", err, out)
	}
	var body string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.file-comments .inline-comment .thread .thread-agent .thread-body`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.file-comments .inline-comment .thread .thread-body').textContent.trim()`, &body),
	); err != nil {
		t.Fatalf("agent reply never appeared under the file-comment card: %v\nstderr: %s", err, p.stderr.String())
	}
	if body != note {
		t.Errorf("thread body = %q, want %q", body, note)
	}

	// `prereview done` flips the card to "worked on" live — the same dropped-update
	// path, on a plain badge rather than a whole block.
	if out, err := exec.Command(p.binary, "done", "--out", p.repo, id).CombinedOutput(); err != nil {
		t.Fatalf("prereview done: %v\n%s", err, out)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.file-comments .inline-comment .processed-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the worked-on badge never appeared on the file card: %v\nstderr: %s", err, p.stderr.String())
	}

	// reviewer → agent: the card's Reply button posts a follow-up, which threads
	// under the agent's and badges the card "awaiting agent".
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.file-comments .inline-comment button[name='openReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.file-comments .reply-form textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.file-comments .reply-form textarea`, "also move the config parsing out", chromedp.ByQuery),
		chromedp.Click(`.file-comments button[name='postReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.file-comments .inline-comment .thread .thread-reviewer .thread-body`, chromedp.ByQuery),
		chromedp.WaitVisible(`.file-comments .inline-comment .awaiting-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reviewer reply on the file-comment card: %v\nstderr: %s", err, p.stderr.String())
	}
}
