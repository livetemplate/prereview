//go:build browser

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_UnsavedBecomesADraft pins the draft model (#105/#119): composer text
// that is NOT saved but abandoned by navigating away (switching files) is kept
// as a DRAFT comment (enqueued=false), not lost — and shows in the Queue as a
// draft. Requires --stream (drafts are a continuous-enqueue concept).
func TestE2E_UnsavedBecomesADraft(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--stream")
	p.waitReady()
	p.clickFile("edited.go")

	// Open the composer on new line 4 and type WITHOUT clicking Save.
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "unsaved thought", chromedp.ByQuery),
		// Let the debounced draft-body autosave land before navigating away.
		chromedp.Sleep(700*time.Millisecond),
	); err != nil {
		t.Fatalf("type: %v\nstderr: %s", err, p.stderr.String())
	}

	// Navigate away WITHOUT Save — switch to another file.
	p.clickFile("fresh.go")

	// The unsaved text must survive as a DRAFT (enqueued col = false).
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 draft row, got %d: %v", len(rows), rows)
	}
	row := rows[1]
	if !strings.Contains(row[5], "unsaved thought") {
		t.Errorf("draft body = %q, missing the typed text", row[5])
	}
	// enqueued is the last (17th) column; a draft persists it as "false".
	if enq := row[len(row)-1]; enq != "false" {
		t.Errorf("unsaved-then-abandoned comment should be a draft (enqueued=false), got %q", enq)
	}

	// And it should surface in the Queue as a draft.
	var draftRows int
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.queue-dropdown .queue-trigger`, chromedp.ByQuery),
		chromedp.Click(`.queue-dropdown .queue-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.queue-row.queue-draft`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('.queue-row.queue-draft').length`, &draftRows),
	); err != nil {
		t.Fatalf("open queue: %v\nstderr: %s", err, p.stderr.String())
	}
	if draftRows != 1 {
		t.Errorf("queue should show 1 draft, got %d", draftRows)
	}
}
