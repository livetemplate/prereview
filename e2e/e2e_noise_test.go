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
