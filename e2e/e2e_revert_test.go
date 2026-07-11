//go:build browser

// End-to-end for #159 M4.2 — the desync fix. The whole M4 milestone existed because
// "undo" on an applied suggestion cleared the decision but left the agent's file edit
// in place. Now undo becomes a REVERT the agent performs: reviewer clicks Revert →
// the snapshot carries verdict=revert → the agent restores the original text and acks
// `prereview reverted` → the file is back to original AND the suggestion is no longer
// applied. This test plays the agent (it owns the file writes) and asserts both ends.
//
// Run: go test -tags=browser -run TestE2E_RevertRestoresFile ./e2e/...

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_RevertRestoresFile(t *testing.T) {
	repo := setupSuggestionRepo(t)
	appGo := filepath.Join(repo, "app.go")
	const original = "package app\n\nfunc Greet() string {\n\treturn \"hello world\"\n}\n"
	const applied = "package app\n\nfunc Greet() string {\n\treturn \"hi\"\n}\n"

	p := bootChromeAgainstRepo(t, repo, 1200, 800, "--agent")
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	readFile := func() string {
		b, err := os.ReadFile(appGo)
		if err != nil {
			t.Fatalf("read app.go: %v", err)
		}
		return string(b)
	}
	agent := func(verb, id string) {
		if out, err := exec.Command(p.binary, verb, "--out", p.repo, id).CombinedOutput(); err != nil {
			t.Fatalf("prereview %s %s: %v\n%s", verb, id, err, out)
		}
	}
	p.waitReady()
	p.clickFile("app.go")

	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""}
	]`)

	// Reviewer accepts; the agent applies the edit to disk and acks it.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-s1"] .sg-old`, chromedp.ByQuery),
		chromedp.Click(`button[name='acceptSuggestion']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.sg-verdict-badge.sg-accept`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accept: %v%s", err, diag())
	}
	if err := os.WriteFile(appGo, []byte(applied), 0o644); err != nil {
		t.Fatalf("agent apply write: %v", err)
	}
	agent("applied", "s1")
	if got := readFile(); got != applied {
		t.Fatalf("after apply, file should hold the proposed text; got:\n%s", got)
	}

	// The box collapsed to a ✦ badge. Expand it and click Revert.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.line-margin .applied-badge`, chromedp.ByQuery),
		chromedp.Click(`.applied-badge`, chromedp.ByQuery),
		chromedp.WaitVisible(`button[name='requestRevert']`, chromedp.ByQuery),
		chromedp.Click(`button[name='requestRevert']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.sg-reverting`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("request revert: %v%s", err, diag())
	}

	// The agent picks up verdict=revert (that the snapshot carries it is unit-tested in
	// TestRevertLifecycle), restores the original text, and acks.
	if err := os.WriteFile(appGo, []byte(original), 0o644); err != nil {
		t.Fatalf("agent revert write: %v", err)
	}
	agent("reverted", "s1")

	// THE FIX: the file is back to original, and the suggestion is no longer applied —
	// it drops back to an undecided box (Accept offered again), no ✦ badge.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`button[name='acceptSuggestion']`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.applied-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("after revert the suggestion should return to undecided (no ✦ badge): %v%s", err, diag())
	}
	if got := readFile(); got != original {
		t.Fatalf("after revert, file must be restored to the ORIGINAL; got:\n%s", got)
	}
}
