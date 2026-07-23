//go:build browser

// End-to-end for Tier B: a comment whose exact line is later edited IN PLACE
// (its before/after context intact) shows a deterministic "likely addressed"
// badge on the next re-anchor — no `prereview done` needed. This is the signal
// that fills the gap when the agent addresses a comment but skips `done`.
//
// Run: go test -tags=browser -run TestE2E_EditedInPlaceBadge ./e2e/...

package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_EditedInPlaceBadge(t *testing.T) {
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "seed.txt", "seed\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	// The working change under review: a code file (line-numbered diff view, so
	// clickLine works) with a clearly commentable line bracketed by stable
	// neighbors on lines 3 and 5.
	doc := filepath.Join(dir, "doc.go")
	if err := os.WriteFile(doc, []byte("package doc\n\nfunc run() {\n\tclaim := \"the original wording\"\n\tprintln(claim)\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := bootChromeAgainstRepo(t, dir, 1400, 900, "--agent")
	p.waitReady()
	p.clickFile("doc.go")
	p.clickLine(0, 4) // the `claim := ...` line
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "reword this claim", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v\nstderr: %s", err, p.stderr.String())
	}

	// No badge yet — the line is unchanged since the comment was anchored.
	var before bool
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`!!document.querySelector('.edited-badge')`, &before))
	if before {
		t.Fatal("'likely addressed' badge present before any edit")
	}

	// RAW edit: change exactly the commented line in place, neighbors intact — no
	// prereview command whatsoever.
	if err := os.WriteFile(doc, []byte("package doc\n\nfunc run() {\n\tclaim := \"the REVISED wording\"\n\tprintln(claim)\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reload → re-anchor → the commented line's text changed with intact context,
	// so the comment reads "likely addressed".
	p.waitReady()
	p.clickFile("doc.go")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.edited-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("comment never showed the 'likely addressed' badge after an in-place edit: %v\nstderr: %s", err, p.stderr.String())
	}
}
