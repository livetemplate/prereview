//go:build browser

// End-to-end coverage for issue #98 Phase 1: LLM-submitted suggested edits. The
// coding agent runs `prereview suggest` to append a proposed edit to
// .prereview/suggestions.jsonl; the running review server (skill mode) watches the
// file and pushes an inline suggestion box to every open tab — no reload. The box
// is visually distinct from a comment (sparkle label + before/after mini-diff),
// renders in both the code view and the rendered-Markdown block view, is
// toggleable, durable across a reload, and goes `outdated` when its target text is
// edited away.
//
// Run with: go test -tags=browser -run TestE2E_Suggestions ./e2e/...

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// setupSuggestionRepo builds a git repo with an untracked code file and an
// untracked Markdown doc (both show in the default changed-file drawer as added),
// so the suggestion surface can be exercised on the code view and the rendered
// Markdown block view.
func setupSuggestionRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "seed.txt", "seed\n")
	// folded.go is committed with a change near the top and many unchanged lines
	// below, so diff view folds the tail. A suggestion on a folded line must still
	// render (the LLM emits arbitrary line numbers) — the fold-reveal path (#98).
	mustWrite(t, dir, "folded.go", "package folded\n\nfunc A() {}\n\nfunc keepLine12() {}\n\nfunc B() {}\n\nfunc C() {}\n\nfunc keepMe() {}\n\nfunc D() {}\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	mustWrite(t, dir, "app.go", "package app\n\nfunc Greet() string {\n\treturn \"hello world\"\n}\n")
	mustWrite(t, dir, "notes.md", "# Notes\n\nThe API might returns an error.\n\nDone.\n")
	// A change on line 1 only; every other line is unchanged and far from it, so
	// diff view collapses the tail into a fold.
	mustWrite(t, dir, "folded.go", "package foldedX\n\nfunc A() {}\n\nfunc keepLine12() {}\n\nfunc B() {}\n\nfunc C() {}\n\nfunc keepMe() {}\n\nfunc D() {}\n")
	return dir
}

// submitSuggestions writes a JSON payload to a temp file and runs the `suggest`
// subcommand against the repo, as the agent would.
func submitSuggestions(t *testing.T, binary, repo, payload string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "sugg.json")
	if err := os.WriteFile(f, []byte(payload), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if out, err := exec.Command(binary, "suggest", "--out", repo, "--file", f).CombinedOutput(); err != nil {
		t.Fatalf("prereview suggest: %v\n%s", err, out)
	}
}

func TestE2E_Suggestions(t *testing.T) {
	// --skill so the server runs WatchLLMStatus — the live suggestion-push path.
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--skill")
	p.waitReady()
	p.clickFile("app.go")

	// The agent proposes an edit on app.go line 4 and one on notes.md line 3.
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"code1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi there\"","note":"tighten the greeting"},
	  {"id":"doc1","file":"notes.md","from_line":3,"to_line":3,"original":"The API might returns an error.","proposed":"The API may return an error.","note":"grammar"},
	  {"id":"fold1","file":"folded.go","from_line":11,"to_line":11,"original":"func keepMe() {}","proposed":"func keepMe() { /* kept */ }","note":"on a folded line"}
	]`)

	// Box appears LIVE (watcher fan-out, no reload) on the code view, visually
	// distinct: a sparkle label + a before/after mini-diff.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion .sg-label`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion .sg-old`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion .sg-new`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("suggestion box never appeared live on code view: %v\nstderr: %s", err, p.stderr.String())
	}
	// Not styled as a comment.
	var isComment bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`!!document.querySelector('.inline-suggestion.inline-comment')`, &isComment)); err != nil {
		t.Fatalf("eval distinct: %v", err)
	}
	if isComment {
		t.Error("suggestion box must not carry the .inline-comment class (distinct surface)")
	}
	// Before/after content is present.
	assertText := func(sel, want string) {
		var got string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`(document.querySelector('`+sel+`')?.textContent||"").trim()`, &got)); err != nil {
			t.Fatalf("read %s: %v", sel, got)
		}
		if got != want {
			t.Errorf("%s: want %q, got %q", sel, want, got)
		}
	}
	assertText(`.inline-suggestion .sg-old`, `return "hello world"`)
	assertText(`.inline-suggestion .sg-new`, `return "hi there"`)

	countBoxes := func() int {
		var n int
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`document.querySelectorAll('.inline-suggestion').length`, &n))
		return n
	}
	if countBoxes() != 1 {
		t.Fatalf("code view: want exactly 1 suggestion box, got %d", countBoxes())
	}

	// Toggle OFF via the view menu → the box disappears; toggle ON → returns.
	toggle := func() {
		if err := chromedp.Run(p.ctx,
			chromedp.Evaluate(`document.querySelector('button[name="toggleSuggestions"]').click()`, nil),
			chromedp.Sleep(250*time.Millisecond),
		); err != nil {
			t.Fatalf("toggle suggestions: %v\nstderr: %s", err, p.stderr.String())
		}
	}
	toggle()
	if countBoxes() != 0 {
		t.Fatalf("after hide toggle: want 0 boxes, got %d", countBoxes())
	}
	toggle()
	if countBoxes() != 1 {
		t.Fatalf("after show toggle: want 1 box, got %d", countBoxes())
	}

	// Rendered-Markdown block view: the doc suggestion renders inside .md-view.
	p.clickFile("notes.md")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-view .inline-suggestion`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("suggestion box missing in rendered-Markdown view: %v\nstderr: %s", err, p.stderr.String())
	}

	// Folded line: folded.go has a change on line 1 only, so diff view collapses
	// its tail — but a suggestion on the (folded) line 11 must still render, because
	// the LLM emits arbitrary line numbers. The fold-reveal path shows the full file
	// whenever it carries visible suggestions.
	p.clickFile("folded.go")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("suggestion on a folded line never rendered: %v\nstderr: %s", err, p.stderr.String())
	}
	var onKeptLine bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`!!document.querySelector('div[data-key="row-L11-11"] .inline-suggestion')`, &onKeptLine)); err != nil {
		t.Fatalf("eval folded suggestion row: %v", err)
	}
	if !onKeptLine {
		t.Error("folded-line suggestion should render on the revealed line 11 row")
	}

	// Durable: a full reload re-derives suggestions from suggestions.jsonl on Mount.
	p.clickFile("app.go")
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
		chromedp.Sleep(1*time.Second),
	); err != nil {
		t.Fatalf("reload: %v\nstderr: %s", err, p.stderr.String())
	}
	p.clickFile("app.go")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("after reload, suggestion box missing (not durable): %v\nstderr: %s", err, p.stderr.String())
	}

	// Outdated drift: edit app.go so the suggestion's original text is gone; on the
	// next Mount the box renders flagged (never silently misplaced).
	mustWrite(t, p.repo, "app.go", "package app\n\nfunc Greet() string {\n\treturn \"COMPLETELY DIFFERENT\"\n}\n")
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
		chromedp.Sleep(1*time.Second),
	); err != nil {
		t.Fatalf("reload after edit: %v\nstderr: %s", err, p.stderr.String())
	}
	p.clickFile("app.go")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion.is-outdated`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("edited-away suggestion should render outdated: %v\nstderr: %s", err, p.stderr.String())
	}
}
