//go:build browser

// End-to-end coverage for issue #88 item 3: the "worked on" badge. The coding
// agent calls `prereview processed <id>` after addressing a comment; the running
// review server (skill mode) watches .prereview/processed.jsonl and pushes a
// per-comment badge to every open tab — no reload needed. The badge is durable
// (survives a reload) and scoped to exactly the marked comment.
//
// Run with: go test -tags=browser -run TestE2E_ProcessedBadge ./e2e/...

package e2e

import (
	"os/exec"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_ProcessedBadge(t *testing.T) {
	// --agent so the server runs WatchLLMStatus (the only mode with an agent
	// writing signals) — that's the live badge-push path under test.
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("edited.go")

	addComment := func(oldNum, newNum int, body string) {
		p.clickLine(oldNum, newNum)
		if err := chromedp.Run(p.ctx,
			chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
			chromedp.SendKeys(`.composer textarea`, body, chromedp.ByQuery),
			chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
			chromedp.Sleep(250*time.Millisecond),
		); err != nil {
			t.Fatalf("add comment %q: %v\nstderr: %s", body, err, p.stderr.String())
		}
	}
	addComment(3, 3, "first-note")
	addComment(0, 4, "second-note")

	// The first row in the CSV is the first comment added ("first-note").
	rows := p.readCSV()
	if len(rows) != 3 { // header + 2
		t.Fatalf("want header + 2 comments, got %d rows: %v", len(rows), rows)
	}
	markedID := rows[1][0]

	// The agent marks ONLY the first comment as worked-on (separate process).
	if out, err := exec.Command(p.binary, "processed", "--out", p.repo, markedID).CombinedOutput(); err != nil {
		t.Fatalf("prereview processed: %v\n%s", err, out)
	}

	// Badge appears LIVE (watcher fan-out, no reload) on exactly one card.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-comment .processed-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("worked-on badge never appeared live: %v\nstderr: %s", err, p.stderr.String())
	}
	badgedBody := func() (string, int) {
		var body string
		var count int
		if err := chromedp.Run(p.ctx,
			chromedp.Evaluate(`document.querySelectorAll('.inline-comment .processed-badge').length`, &count),
			chromedp.Evaluate(`([...document.querySelectorAll('.inline-comment')].find(c=>c.querySelector('.processed-badge'))?.querySelector('.body')?.textContent||"").trim()`, &body),
		); err != nil {
			t.Fatalf("read badged card: %v", err)
		}
		return body, count
	}
	if body, count := badgedBody(); count != 1 || body != "first-note" {
		t.Fatalf("badge should be on exactly the marked (first-note) card; got count=%d body=%q", count, body)
	}

	// Durable: reload → the badge is re-derived from processed.jsonl on Mount.
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.inline-comment .processed-badge`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
	); err != nil {
		t.Fatalf("after reload, badge missing: %v\nstderr: %s", err, p.stderr.String())
	}
	if body, count := badgedBody(); count != 1 || body != "first-note" {
		t.Fatalf("after reload, badge wrong; got count=%d body=%q", count, body)
	}
}
