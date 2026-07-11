//go:build browser

// End-to-end for #149 Phase 3 (agent→reviewer): the coding agent posts a thread
// reply on a comment via `prereview reply`, and it appears LIVE under the comment
// card (watcher fan-out, no reload) so the reviewer sees what the agent did.
//
// Run: go test -tags=browser -run TestE2E_AgentReply ./e2e/...

package e2e

import (
	"os/exec"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_AgentReply(t *testing.T) {
	// --agent so the server runs WatchLLMStatus — the live agent-signal fan-out.
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("app.go")
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "rename this greeting", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v\nstderr: %s", err, p.stderr.String())
	}

	// The comment's id is the first CSV column.
	rows := p.readCSV()
	if len(rows) < 2 {
		t.Fatalf("expected a comment row, got %v", rows)
	}
	id := rows[1][0]

	// The agent posts a reply on that comment.
	const note = "Renamed to Greeting and updated the 2 callers."
	out, err := exec.Command(p.binary, "reply", "--out", p.repo, "--body", note, id).CombinedOutput()
	if err != nil {
		t.Fatalf("prereview reply: %v\n%s", err, out)
	}

	// It appears LIVE under the card (agent-signal watcher fan-out, no reload).
	var body string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-comment .thread .thread-agent .thread-body`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.inline-comment .thread .thread-body').textContent.trim()`, &body),
	); err != nil {
		t.Fatalf("agent reply never appeared live under the card: %v\nstderr: %s", err, p.stderr.String())
	}
	if body != note {
		t.Errorf("thread body = %q, want %q", body, note)
	}
}
