//go:build browser

// End-to-end for #209: comments left on a remote copy of a reviewed doc arrive
// as native comments. The test plays the agent: it runs `prereview import`
// while a tab is open, and the comments land live, anchored on their quotes,
// badged with where they came from, and stay on disk through the tab's saves.
//
// Run: go test -tags=browser -run 'TestE2E_ImportLive' ./e2e/...

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

const importDocMD = "# Plan\n\nWe ship **on Friday** after review.\n\nThe rollout starts small.\n\nIt grows each week.\n"

// setupImportRepo is a repo whose one changed file is plan.md.
func setupImportRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "plan.md", "# Plan\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "plan.md", importDocMD)
	return dir
}

// runImportCmd runs `prereview import` the way the agent does.
func runImportCmd(t *testing.T, p *runningPrereview, payload string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "import.json")
	if err := os.WriteFile(f, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(p.binary, "import", "--out", p.store, "--file", f).CombinedOutput()
	if err != nil {
		t.Fatalf("prereview import: %v: %s", err, out)
	}
	return string(out)
}

type importedJSON struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Source *struct {
		Name, Author, Quote string
	} `json:"source"`
}

func TestE2E_ImportLive(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			repo := setupImportRepo(t)
			p := bootChromeAgainstRepo(t, repo, vp.w, vp.h, "--agent")
			diag := captureDiag(t, p)
			p.waitReadyAt(vp.w, vp.h)
			ctx, cancel := within(p, 30*time.Second)
			defer cancel()
			if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__noReload = 1`, nil)); err != nil {
				t.Fatal(err)
			}

			// Three remote comments: one quoting the source, one quoting the
			// rendered text (** gone), one whose quote isn't in the file.
			out := runImportCmd(t, p, `[
				{"remote_id":"r1","source":"Google Docs","author":"Alice","url":"https://docs.example/d#r1","file":"plan.md","quote":"The rollout starts small.","body":"Too vague?"},
				{"remote_id":"r2","source":"Google Docs","author":"Bob","file":"plan.md","quote":"ship on Friday after","body":"Which Friday?"},
				{"remote_id":"r3","source":"Notion","author":"Cy","file":"plan.md","quote":"a sentence that was cut","body":"Where did this go?"}
			]`)
			if !strings.Contains(out, "queued 3 comment(s)") || !strings.Contains(out, "adds them now") {
				t.Errorf("import said %q", out)
			}
			waitJSTrue(t, p.ctx, `document.querySelectorAll('.source-badge').length === 3`, 10*time.Second,
				"the three imported comments show live, each with its source")
			var reloaded bool
			if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__noReload !== 1`, &reloaded)); err != nil || reloaded {
				t.Errorf("the page reloaded (%v)%s", err, diag())
			}
			if got := textOf(t, p, `.source-badge a`); got != "Google Docs" {
				t.Errorf("linked badge %q%s", got, diag())
			}
			if n := countSel(t, p, `.ic-snippet`); n < 1 {
				t.Errorf("no quote shown for the file comment%s", diag())
			}
			waitJSTrue(t, p.ctx, `[...document.querySelectorAll('.ic-snippet')].some(e => e.textContent.includes('a sentence that was cut'))`,
				5*time.Second, "the file comment shows the quote it was on")

			// What the agent sees: the snapshot shape, with source.
			raw, err := exec.Command(p.binary, "comments", "--json", "--out", p.store).Output()
			if err != nil {
				t.Fatalf("prereview comments: %v", err)
			}
			var cs []importedJSON
			if err := json.Unmarshal(raw, &cs); err != nil || len(cs) != 3 {
				t.Fatalf("comments --json (%v): %s", err, raw)
			}
			byQuote := map[string]importedJSON{}
			for _, c := range cs {
				if c.Source == nil {
					t.Fatalf("an imported comment without source: %s", raw)
				}
				byQuote[c.Source.Quote] = c
			}
			if c := byQuote["The rollout starts small."]; c.Kind != "text" || c.Text != "The rollout starts small." {
				t.Errorf("source-quoted comment %+v", c)
			}
			if c := byQuote["ship on Friday after"]; c.Kind != "text" || c.Text != "ship on Friday after" {
				t.Errorf("rendered-quoted comment %+v", c)
			}
			fileCm := byQuote["a sentence that was cut"]
			if fileCm.Kind != "file" {
				t.Errorf("unmatched quote %+v, want a file comment", fileCm)
			}

			// The agent marks one done: its card shows it, live.
			if out, err := exec.Command(p.binary, "done", "--out", p.store, fileCm.ID).CombinedOutput(); err != nil {
				t.Fatalf("prereview done: %v: %s", err, out)
			}
			waitJSTrue(t, p.ctx, `document.querySelectorAll('.processed-badge').length >= 1`, 10*time.Second,
				"the done mark shows on the imported comment")

			// A reviewer save in this tab keeps all three on disk; importing the
			// same ids again adds nothing.
			if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('.inline-comment button[name="toggleResolved"]').click()`, nil)); err != nil {
				t.Fatal(err)
			}
			// (a resolved comment leaves the view, so read the save from disk)
			resolvedOnDisk := func() (n int) {
				rows, _ := csv.Read(filepath.Join(p.store, "comments.csv"))
				for _, r := range rows {
					if r.Resolved {
						n++
					}
				}
				return n
			}
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(100 * time.Millisecond) {
				if resolvedOnDisk() == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the resolve wasn't saved%s", diag())
				}
			}
			runImportCmd(t, p, `{"remote_id":"r1","source":"Google Docs","file":"plan.md","quote":"The rollout starts small.","body":"Too vague?"}`)
			time.Sleep(1500 * time.Millisecond) // two watcher ticks
			rows, err := csv.Read(filepath.Join(p.store, "comments.csv"))
			if err != nil || len(rows) != 3 {
				t.Errorf("comments.csv has %d rows (%v), want 3%s", len(rows), err, diag())
			}
			noHorizontalScroll(t, p, diag)
		})
	}
}

// TestE2E_ImportAtLaunch: an import made while no review runs says so, and the
// next launch adds it.
func TestE2E_ImportAtLaunch(t *testing.T) {
	repo := setupImportRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, ".prereview"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "import.json")
	payload := `{"remote_id":"r1","source":"Google Docs","author":"Alice","file":"plan.md","quote":"It grows each week.","body":"How fast?"}`
	if err := os.WriteFile(f, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(prereviewBinary(t), "import", "--out", repo, "--file", f).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "added when it next starts") {
		t.Fatalf("import with no review running: %v: %s", err, out)
	}
	p := bootChromeAgainstRepo(t, repo, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	waitJSTrue(t, p.ctx, `document.querySelectorAll('.source-badge').length === 1`, 10*time.Second,
		"the comment imported before launch shows")
	if got := textOf(t, p, `.source-badge`); got != "Google Docs · Alice" {
		t.Errorf("badge %q%s", got, diag())
	}
}
