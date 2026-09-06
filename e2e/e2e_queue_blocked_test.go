//go:build browser

// End-to-end coverage for #203: a comment whose anchor the agent's own edit destroyed
// must stay VISIBLE in the work queue as "blocked", not vanish from it.
//
// Before this, QueueState returned "" for an outdated comment, so the row left both the
// queued and the done count. The panel has no total, so a row leaving looked exactly like
// a row being finished — the reviewer watched the numbers stop moving with nothing to
// click on. Drift is not a lifecycle exit: only a human resolve takes work out of the
// queue.
//
// Run with: go test -tags=browser -run TestE2E_QueueBlockedRow ./e2e/...

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestE2E_QueueBlockedRow(t *testing.T) {
	dir := setupFixtureRepoReanchor(t)
	p := bootChromeAgainstRepo(t, dir, 1200, 800, "--agent")

	var mu sync.Mutex
	var consoleLines, wsFrames []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		case *cdpnetwork.EventWebSocketFrameReceived:
			wsFrames = append(wsFrames, "recv "+e.Response.PayloadData)
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+e.Response.PayloadData)
		}
	})
	if err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.Enable().Do(ctx)
	})); err != nil {
		t.Fatalf("enable network domain: %v", err)
	}
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.waitReady()
	p.clickFile("docs.md")
	if !clickMdBlock(p, "TARGET sentence to anchor a comment on.") {
		t.Fatalf("could not find TARGET block%s", diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "please rework this", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v%s", err, diag())
	}

	// Baseline: one queued row, no blocked count.
	openQueue := func() {
		if err := chromedp.Run(p.ctx,
			chromedp.WaitVisible(`.queue-dropdown .queue-trigger`, chromedp.ByQuery),
			chromedp.Click(`.queue-dropdown .queue-trigger`, chromedp.ByQuery),
			chromedp.Sleep(250*time.Millisecond),
		); err != nil {
			t.Fatalf("open queue panel: %v%s", err, diag())
		}
	}
	openQueue()
	var queuedRows int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('.queue-row.queue-queued').length`, &queuedRows),
	); err != nil {
		t.Fatalf("read queue: %v%s", err, diag())
	}
	if queuedRows != 1 {
		t.Fatalf("baseline queued rows = %d, want 1%s", queuedRows, diag())
	}

	// The agent rewrites the document. The anchored sentence is gone AND its
	// before-context moved, so re-anchoring cannot place it and it is not an
	// in-place edit either (#196's "edited" needs both neighbours intact) →
	// anchor_status=outdated. This is the everyday case: the agent's own work is
	// what breaks the anchors of the comments it has not reached yet.
	if err := os.WriteFile(filepath.Join(dir, "docs.md"),
		[]byte("# Reanchor Fixture\n\nRewritten opening, nothing like before.\nA second rewritten line here too.\nThe target line is entirely replaced.\nTrailing sentence after the target.\n"),
		0o644); err != nil {
		t.Fatalf("rewrite doc: %v", err)
	}

	p.waitReady()
	p.clickFile("docs.md")
	openQueue()

	var blockedRows, allRows int
	var rowBadge, legend string
	if err := chromedp.Run(p.ctx,
		chromedp.Sleep(700*time.Millisecond),
		chromedp.Evaluate(`document.querySelectorAll('.queue-row.queue-blocked').length`, &blockedRows),
		chromedp.Evaluate(`document.querySelectorAll('.queue-row').length`, &allRows),
		chromedp.Evaluate(`(document.querySelector('.queue-row.queue-blocked .queue-badge')||{}).textContent||""`, &rowBadge),
		chromedp.Evaluate(`(document.querySelector('.queue-legend')||{}).textContent||""`, &legend),
	); err != nil {
		t.Fatalf("read queue after drift: %v%s", err, diag())
	}

	rows := p.readCSV()
	if len(rows) < 2 {
		t.Fatalf("comment row missing from the CSV%s", diag())
	}
	if rows[1][9] != "outdated" {
		t.Fatalf("fixture did not drift: anchor_status = %q, want outdated — the test is not "+
			"exercising the bug%s", rows[1][9], diag())
	}
	if allRows != 1 {
		t.Errorf("queue has %d rows, want 1 — a drifted comment must not vanish from the "+
			"queue; a row leaving is indistinguishable from a row being finished%s", allRows, diag())
	}
	if blockedRows != 1 {
		t.Errorf("blocked rows = %d, want 1%s", blockedRows, diag())
	}
	if got := strings.TrimSpace(rowBadge); got != "blocked" {
		t.Errorf("row badge = %q, want \"blocked\"%s", got, diag())
	}
	if !strings.Contains(legend, "need re-anchoring") {
		t.Errorf("legend = %q, must name the blocked work so the stall is explained, not just "+
			"counted%s", legend, diag())
	}

	mu.Lock()
	for _, l := range consoleLines {
		if strings.HasPrefix(l, "error") {
			t.Errorf("browser console error%s", diag())
			break
		}
	}
	mu.Unlock()
}

// setupFixtureRepoEightNotes is a doc with eight independently-anchorable paragraphs —
// enough to reach the "5 or so and up" scale where #203 was reported.
func setupFixtureRepoEightNotes(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "notes.md", "# Notes\n\nseed.\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	var b strings.Builder
	b.WriteString("# Notes\n\n")
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&b, "Paragraph %d is the anchor for note number %d.\n\n", i, i)
	}
	mustWrite(t, dir, "notes.md", b.String())
	return dir
}

// TestE2E_QueueDoneReachesTotalAfterDrift is #203 end to end, through the real binary and
// a real browser, at the scale it was reported: eight comments, the agent marks them all
// done, and then its edits destroy most of the anchors.
//
// Before the fix Done fell back to the number of comments whose anchors happened to
// survive — the reviewer watched it climb and then drop, never reaching eight.
func TestE2E_QueueDoneReachesTotalAfterDrift(t *testing.T) {
	dir := setupFixtureRepoEightNotes(t)
	p := bootChromeAgainstRepo(t, dir, 1200, 800, "--agent")

	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return fmt.Sprintf("\n--- server ---\n%s\n--- html ---\n%s", p.stderr.String(), html)
	}

	p.waitReady()
	p.clickFile("notes.md")
	for i := 1; i <= 8; i++ {
		phrase := fmt.Sprintf("Paragraph %d is the anchor", i)
		if !clickMdBlock(p, phrase) {
			t.Fatalf("could not find block %q%s", phrase, diag())
		}
		if err := chromedp.Run(p.ctx,
			chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
			chromedp.SendKeys(`.composer textarea`, fmt.Sprintf("note %d", i), chromedp.ByQuery),
			chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
		); err != nil {
			t.Fatalf("add comment %d: %v%s", i, err, diag())
		}
	}
	if rows := p.readCSV(); len(rows) != 9 { // header + 8
		t.Fatalf("want header + 8 comments, got %d rows%s", len(rows), diag())
	}

	// The agent marks the whole batch. --all-open must cover all eight.
	out, err := exec.Command(p.binary, "done", "--out", p.repo, "--all-open").CombinedOutput()
	if err != nil {
		t.Fatalf("prereview done --all-open: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "marked 8 comment(s)") {
		t.Fatalf("--all-open should have marked all 8; got: %s", out)
	}

	// Now the agent's edits land. Paragraphs 1-5 are RESTRUCTURED — merged into a single
	// section — which is what acting on several related comments at once actually looks
	// like. That matters: a straight in-place rewrite keeps both neighbours intact and
	// #196 classifies it "edited" (still actionable), so it is specifically restructuring
	// that destroys an anchor. Paragraphs 6-8 survive so some anchors stay placeable.
	var b strings.Builder
	b.WriteString("# Notes\n\n")
	b.WriteString("A single merged section now covers everything the first five paragraphs said.\n\n")
	for i := 6; i <= 8; i++ {
		fmt.Fprintf(&b, "Paragraph %d is the anchor for note number %d.\n\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("rewrite doc: %v", err)
	}

	p.waitReady()
	p.clickFile("notes.md")
	// Re-anchoring runs on Mount and self-heals the CSV; give that write a beat before
	// reading it back (the same wait the #196 re-anchor tests use).
	_ = chromedp.Run(p.ctx, chromedp.Sleep(900*time.Millisecond))

	var outdated int
	var statuses []string
	for _, r := range p.readCSV()[1:] {
		statuses = append(statuses, r[9])
		if r[9] == "outdated" {
			outdated++
		}
	}
	if outdated == 0 {
		t.Fatalf("no anchor drifted (statuses: %v) — the test is not exercising #203%s", statuses, diag())
	}

	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.queue-dropdown .queue-trigger`, chromedp.ByQuery),
		chromedp.Click(`.queue-dropdown .queue-trigger`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("open queue panel: %v%s", err, diag())
	}
	var doneCount, doneRows int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`parseInt((document.querySelector('.queue-legend .q-done')||{}).textContent||"0",10)`, &doneCount),
		chromedp.Evaluate(`document.querySelectorAll('.queue-row.queue-done').length`, &doneRows),
	); err != nil {
		t.Fatalf("read queue counts: %v%s", err, diag())
	}
	if doneCount != 8 {
		t.Errorf("Done = %d, want 8 with %d anchors drifted — the agent finished every comment; "+
			"its own edits breaking the anchors must not un-do them%s", doneCount, outdated, diag())
	}
	if doneRows != 8 {
		t.Errorf("done rows = %d, want 8%s", doneRows, diag())
	}
}
