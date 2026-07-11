//go:build browser

// End-to-end for #147 Phase 8: the "Ask for suggestions" picker. Opening it lists the
// built-in prompts; picking one pre-fills the file-comment composer with the prompt
// body; Save creates an ordinary kind=file comment carrying the prompt (which the
// agent then answers with `prereview suggest`).
//
// Run: go test -tags=browser -run TestE2E_PromptPicker ./e2e/...

package e2e

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_PromptPicker(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800)
	p.waitReady()
	p.clickFile("app.go")

	// Open the picker and choose "Code review".
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.prompts-trigger`, chromedp.ByQuery),
		chromedp.Click(`.prompts-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.prompts-panel .prompt-row`, chromedp.ByQuery),
		chromedp.Evaluate(`[...document.querySelectorAll('.prompt-row')].find(b => b.textContent.trim() === 'Code review').click()`, nil),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open + pick prompt: %v\nstderr: %s", err, p.stderr.String())
	}

	// The composer is pre-filled with the code-review prompt (incl. the propose-via-
	// suggest instruction — the reliability lever).
	var body string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.composer textarea').value`, &body)); err != nil {
		t.Fatalf("read composer: %v", err)
	}
	if !strings.Contains(body, "Review this file for bugs") || !strings.Contains(body, "prereview suggest") {
		t.Errorf("composer not pre-filled with the code-review prompt; got: %q", body)
	}

	// Save → an ordinary kind=file comment carrying the prompt.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("save prompt comment: %v\nstderr: %s", err, p.stderr.String())
	}
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected exactly one comment row, got %d: %v", len(rows)-1, rows)
	}
	kindIdx, bodyIdx := -1, -1
	for i, h := range rows[0] {
		switch h {
		case "kind":
			kindIdx = i
		case "body":
			bodyIdx = i
		}
	}
	if kindIdx < 0 || bodyIdx < 0 {
		t.Fatalf("csv header missing kind/body columns: %v", rows[0])
	}
	if rows[1][kindIdx] != "file" {
		t.Errorf("prompt comment should be kind=file; got %q (row=%v)", rows[1][kindIdx], rows[1])
	}
	if !strings.Contains(rows[1][bodyIdx], "Review this file for bugs") {
		t.Errorf("comment body should carry the prompt; got %q", rows[1][bodyIdx])
	}
}
