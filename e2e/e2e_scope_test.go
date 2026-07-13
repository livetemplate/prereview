//go:build browser

// End-to-end for #171. Two things, both seen through a real browser:
//
//  1. A single-file review's .prereview/ store lives in the file's PARENT directory, so
//     it is shared with every other file reviewed from there. Reviewing b.md must NOT
//     surface a.md's comments or suggestion cards — the reported bug.
//  2. An accepted-but-unapplied edit must be impossible to miss: an amber count in the
//     Queue, and a warning on the End-session confirm. prereview never writes user files;
//     if the agent's turn has ended, nothing else ever will.
//
// Run: go test -tags=browser -run TestE2E_SingleFileScope ./e2e/...
//      go test -tags=browser -run TestE2E_AwaitingApply ./e2e/...

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

// twoDocDir is the reported situation: one directory, two documents, one shared store —
// and a.md carries a previous review's leftovers (a comment and a suggestion).
func twoDocDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []struct{ name, body string }{
		{"a.md", "# Doc A\n\nthe first draft, teh typo here\n"},
		{"b.md", "# Doc B\n\nthe second draft, wiht its own typo\n"},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pdir := filepath.Join(dir, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Left behind by the earlier review of a.md, plus one comment on b.md so we can prove
	// the b.md work still shows (the fix must scope, not just hide everything).
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"ca1,a.md,3,3,new,stale note on the OLD doc,2026-07-01T12:00:00Z,false,,,line,,\n" +
		"cb1,b.md,3,3,new,live note on the doc under review,2026-07-01T12:00:00Z,false,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestE2E_SingleFileScope(t *testing.T) {
	dir := twoDocDir(t)

	// Boot the review on ONE FILE. The store is in the parent dir, shared with a.md.
	p := bootChromeAgainstRepo(t, filepath.Join(dir, "b.md"), 1400, 1000, "--agent")
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}

	// a.md's suggestion from the earlier review — written straight into the shared store,
	// exactly as the previous session left it. (--out is the REPO = the parent directory.)
	submitSuggestions(t, p.binary, dir, `[
	  {"id":"sa1","file":"a.md","from_line":3,"to_line":3,"original":"teh","proposed":"the"}
	]`)

	p.waitReady()

	// The page must not carry a single trace of a.md.
	var body string
	if err := chromedp.Run(p.ctx, chromedp.Text(`body`, &body, chromedp.ByQuery)); err != nil {
		t.Fatalf("read body: %v%s", err, diag())
	}
	if strings.Contains(body, "stale note on the OLD doc") {
		t.Errorf("a.md's comment is rendered while reviewing b.md — the store is shared, "+
			"but the REVIEW is not.%s", diag())
	}
	if !strings.Contains(body, "live note on the doc under review") {
		t.Errorf("b.md's own comment should still render — the fix must SCOPE, not hide.%s", diag())
	}

	// The queue — the surface the bug was reported through.
	var queueFiles []string
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.queue-trigger`, chromedp.ByQuery),
		chromedp.Evaluate(`[...document.querySelectorAll('.queue-row')].map(e=>e.textContent)`, &queueFiles),
	); err != nil {
		t.Fatalf("open queue: %v%s", err, diag())
	}
	for _, row := range queueFiles {
		if strings.Contains(row, "a.md") {
			t.Errorf("queue row from the previous file's review: %q — this is the reported bug%s",
				row, diag())
		}
	}

	// The agent's own view of the queue must agree with the reviewer's, or the agent goes
	// and edits a document nobody is reviewing.
	out, err := exec.Command(p.binary, "comments", "--out", dir, "--json").Output()
	if err != nil {
		t.Fatalf("prereview comments: %v", err)
	}
	var listed []struct {
		ID   string `json:"id"`
		File string `json:"file"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("parse comments json: %v\n%s", err, out)
	}
	for _, c := range listed {
		if c.File != "b.md" {
			t.Errorf("`prereview comments` listed %s (%s) — the agent would act on a file the "+
				"reviewer isn't reviewing", c.ID, c.File)
		}
	}
	if len(listed) != 1 {
		t.Errorf("`prereview comments` = %d comments, want 1 (only b.md's)\n%s", len(listed), out)
	}

	// And the load-bearing safety property: scoping the VIEW must not have deleted the
	// other file's rows from the shared store.
	csv, err := os.ReadFile(filepath.Join(dir, ".prereview", "comments.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(csv), "ca1") {
		t.Fatal("DATA LOSS: a.md's comment is gone from comments.csv — scope filters the reads, " +
			"it must never reach the CSV rewrite")
	}
}

// An accepted edit the agent never applies is the state that silently leaves the document
// inconsistent. It must be loud: an amber count in the Queue and a warning on End session.
func TestE2E_AwaitingApply(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 900, "--agent")
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("app.go")

	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""}
	]`)

	// Accept it. prereview does NOT write the file — the agent does. Until it does, the
	// review is owed a file write, and that has to be visible.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion .sg-old`, chromedp.ByQuery),
		chromedp.Click(`button[name='acceptSuggestion']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.q-awaiting`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("an accepted-but-unapplied edit must surface an awaiting-apply count: %v%s", err, diag())
	}

	// The End-session confirm must say so — this is the last moment the reviewer can
	// notice that accepting never actually changed the document. End session lives INSIDE
	// the Queue dropdown (the session hub), so open that first.
	var warn string
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.queue-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.queue-awaiting`, chromedp.ByQuery),
		chromedp.Click(`.queue-end-btn`, chromedp.ByQuery),
		chromedp.WaitVisible(`#confirm-end-session .end-warn`, chromedp.ByQuery),
		chromedp.Text(`#confirm-end-session .end-warn`, &warn, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("End session must warn about unapplied accepts: %v%s", err, diag())
	}
	if !strings.Contains(warn, "not yet applied") {
		t.Errorf("end-session warning = %q, want it to say the edit is not yet applied", warn)
	}

	// The agent applies it and acks → the debt is settled, the warning goes away.
	if out, err := exec.Command(p.binary, "applied", "--out", p.repo, "s1").CombinedOutput(); err != nil {
		t.Fatalf("prereview applied: %v\n%s", err, out)
	}
	if err := chromedp.Run(p.ctx, chromedp.WaitNotPresent(`.q-awaiting`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the awaiting-apply count should clear once the agent acks the apply: %v%s", err, diag())
	}
}
