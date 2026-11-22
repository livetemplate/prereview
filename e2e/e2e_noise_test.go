//go:build browser

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

// setupWhitespaceRepo: ws.go is re-indented (spaces to a tab) and one value
// changed, with an open comment on the old side of line 3.
func setupWhitespaceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "ws.go", "func f() {\n  a := 1\n  b := 2\n  c := 3\n}\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "ws.go", "func f() {\n\ta := 1\n\tb := 2\n\tc := 4\n}\n")
	seedReviewStore(t, dir, csv.Row{ID: "c1", File: "ws.go", FromLine: 3, ToLine: 3, Side: "old",
		Body: "why two spaces here", Kind: "line", CreatedAt: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)})
	return dir
}

// TestE2E_HideWhitespace (#115 B1): with "Hide whitespace changes" on, a
// re-indented line reads as one unchanged row; a line whose value changed still
// shows as changed; the old line with a comment stays as it was, comment shown;
// and the setting survives a reload.
func TestE2E_HideWhitespace(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupWhitespaceRepo(t), 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("ws.go")
	rows := func(kind string) int { return evalInt(t, p, `document.querySelectorAll('.code .line.`+kind+`').length`) }
	if d, a := rows("del"), rows("add"); d != 3 || a != 3 {
		t.Fatalf("before: %d deleted and %d added rows, want 3 and 3%s", d, a, diag())
	}

	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector("button[name='toggleHideWhitespace']").click()`, nil)); err != nil {
		t.Fatalf("turn the setting on: %v%s", err, diag())
	}
	paired := `!!document.querySelector('.code .line[data-key="L2-2"]')`
	waitJSTrueDiag(t, p.ctx, paired, 5*time.Second, "the re-indented line 2 reads as one unchanged row", diag)
	// b (old 3) carries the comment, so it stays; c changed value. Both stay changed.
	if d, a := rows("del"), rows("add"); d != 2 || a != 2 {
		t.Errorf("with the setting on: %d deleted and %d added rows, want 2 and 2%s", d, a, diag())
	}
	if !strings.Contains(evalStr(t, p, `document.querySelector('.code').textContent`), "why two spaces here") {
		t.Errorf("the comment on old line 3 must still show%s", diag())
	}

	if err := chromedp.Run(p.ctx, chromedp.Reload()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	p.waitReady()
	waitJSTrueDiag(t, p.ctx, paired, 5*time.Second, "the setting survives a reload", diag)
}

// TestE2E_MovedCode (#115 B2): a block moved from a.go into a new file is
// labelled at both ends; the label opens the other end at its first line.
func TestE2E_MovedCode(t *testing.T) {
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	block := "\tif err := charge(order); err != nil {\n\t\treturn fmt.Errorf(\"charge: %w\", err)\n\t}\n\tlog.Printf(\"charged %s\", order.ID)\n"
	mustWrite(t, dir, "a.go", "package a\n\nfunc A() {\n"+block+"}\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "a.go", "package a\n\nfunc A() {\n}\n")
	mustWrite(t, dir, "b.go", "package b\n\nfunc B() {\n"+block+"}\n")

	p := bootChromeAgainstRepo(t, dir, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("b.go")
	waitJSTrueDiag(t, p.ctx, `!!document.querySelector('.code .move-mark')`, 5*time.Second, "b.go shows the move's label", diag)
	if got := evalStr(t, p, `document.querySelector('.code .move-mark').textContent.trim()`); got != "Moved from a.go L4" {
		t.Errorf("b.go's label = %q, want %q%s", got, "Moved from a.go L4", diag())
	}
	if n := evalInt(t, p, `document.querySelectorAll('.code .moved-flag').length`); n != 4 {
		t.Errorf("all 4 moved rows must be flagged, got %d%s", n, diag())
	}

	ctx, cancel := within(p, 10*time.Second)
	err := chromedp.Run(ctx, chromedp.Click(`.code .move-mark button[name="jumpToAnchor"]`, chromedp.ByQuery))
	cancel()
	if err != nil {
		t.Fatalf("tap the label: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, `!!document.querySelector('.code .line.is-cursor[data-key="L4-0"]')`, 5*time.Second, "the label opens a.go at the deleted line 4", diag)
	if got := evalStr(t, p, `document.querySelector('.code .move-mark').textContent.trim()`); got != "Moved to b.go L4" {
		t.Errorf("a.go's label = %q, want %q%s", got, "Moved to b.go L4", diag())
	}
}

// TestE2E_ChangedSinceViewed (#115 B3): a file marked viewed shows as changed
// since you viewed it once it is edited on disk and the review refreshes.
func TestE2E_ChangedSinceViewed(t *testing.T) {
	t.Setenv("PREREVIEW_EDIT_SETTLE", "1s")
	repo := setupFixtureRepo(t)
	p := bootChromeAgainstRepo(t, repo, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")
	row := `.file-row:has(input[name="path"][value="edited.go"])`
	ctx, cancel := within(p, 10*time.Second)
	err := chromedp.Run(ctx, chromedp.Click(row+` button[name="toggleViewed"]`, chromedp.ByQuery))
	cancel()
	if err != nil {
		t.Fatalf("mark edited.go viewed: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, `!!document.querySelector('`+row+`.is-viewed')`, 5*time.Second, "edited.go reads viewed", diag)

	mustWrite(t, repo, "edited.go", "package edited\n\nfunc Hello() string {\n\treturn \"hello again\"\n}\n")
	waitJSTrueDiag(t, p.ctx, refreshBtnVisibleJS, 15*time.Second, "the refresh nudge", diag)
	ctx, cancel = within(p, 10*time.Second)
	err = chromedp.Run(ctx, chromedp.Click(`.refresh-prompt button[name="refreshDiff"]`, chromedp.ByQuery))
	cancel()
	if err != nil {
		t.Fatalf("refresh: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, `(document.querySelector('`+row+` .viewed-changed') || {}).textContent === 'changed since you viewed it'`, 5*time.Second,
		"the drawer says edited.go changed since you viewed it", diag)
	if n := evalInt(t, p, `document.querySelectorAll('`+row+`.is-viewed').length`); n != 0 {
		t.Errorf("a changed file must not read viewed%s", diag())
	}
}
