//go:build browser

// End-to-end for checks M1: deterministic checkers over the open file. The file
// header shows "passed/total"; the panel lists each check; selecting one marks its
// findings inline with a card that queues the finding as an ordinary comment;
// disabling a check drops it from the count; editing a rule file updates the open
// panel live; and a noisy rule only unfolds rows once it is selected.
//
// Run: go test -tags=browser -run 'TestE2E_Checks|TestE2E_CheckRule' ./e2e/...

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// setupFixtureRepoChecks is a 60-line prose file (name decides Markdown vs plain
// text) whose line 1 changed and whose only prose problem is "very" on line 40,
// far from the change, so Diff view folds it until a check is selected.
func setupFixtureRepoChecks(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	body := func(title string) string {
		var b strings.Builder
		b.WriteString("# " + title + "\n")
		for i := 2; i <= 60; i++ {
			if i == 40 {
				b.WriteString("This is very good.\n")
				continue
			}
			fmt.Fprintf(&b, "Plain line %d.\n", i)
		}
		return b.String()
	}
	mustWrite(t, dir, name, body("Notes"))
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, name, body("Notes, edited"))
	return dir
}

func writeRule(t *testing.T, repo, name, body string) {
	t.Helper()
	dir := checksDirFor(repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func textOf(t *testing.T, p *runningPrereview, sel string) string {
	t.Helper()
	var s string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector(`+"`"+sel+"`"+`)||{}).textContent||''`, &s)); err != nil {
		t.Fatalf("text of %s: %v", sel, err)
	}
	return strings.TrimSpace(s)
}

// checkRow selects a check's panel row by ID.
func checkRow(id string) string { return fmt.Sprintf(`.checks-list li[data-key="check-%s"]`, id) }

func TestE2E_ChecksPanel(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			repo := setupFixtureRepoChecks(t, "notes.md")
			p := bootChromeAgainstRepo(t, repo, vp.w, vp.h)
			diag := captureDiag(t, p)
			p.waitReadyAt(vp.w, vp.h)
			ctx, cancel := within(p, 20*time.Second)
			defer cancel()

			// Three built-ins apply to Markdown; only filler-intensifiers fails.
			if err := chromedp.Run(ctx, chromedp.WaitVisible(`.checks-trigger`, chromedp.ByQuery)); err != nil {
				t.Fatalf("no checks button in the file header: %v%s", err, diag())
			}
			if got := textOf(t, p, `.checks-trigger .checks-count`); got != "2/3" {
				t.Errorf("header count = %q, want 2/3%s", got, diag())
			}
			if n := countSel(t, p, `.checks-trigger.is-failing`); n != 1 {
				t.Errorf("a failing check must mark the button failing%s", diag())
			}

			if err := chromedp.Run(ctx,
				chromedp.Click(`.checks-trigger`, chromedp.ByQuery),
				chromedp.WaitVisible(`.checks-panel`, chromedp.ByQuery),
				chromedp.WaitVisible(checkRow("filler-intensifiers")+`.is-fail`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("open the panel: %v%s", err, diag())
			}
			if n := countSel(t, p, `.checks-list li.is-pass`); n != 2 {
				t.Errorf("want 2 passing rows, got %d%s", n, diag())
			}
			// Nothing is drawn until a check is selected.
			if n := countSel(t, p, `mark.check-finding`); n != 0 {
				t.Errorf("an unselected check drew %d marks%s", n, diag())
			}

			// Select it: the rendered Markdown shows its source (a transient reveal), the
			// span is marked and carded on line 40.
			if err := chromedp.Run(ctx,
				chromedp.Click(checkRow("filler-intensifiers")+` .check-name`, chromedp.ByQuery),
				chromedp.WaitVisible(`.code mark.check-finding`, chromedp.ByQuery),
				chromedp.WaitVisible(`.code .finding-card`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("select the check: %v%s", err, diag())
			}
			if got := textOf(t, p, `.code mark.check-finding`); got != "very" {
				t.Errorf("marked span = %q, want very%s", got, diag())
			}
			if got := textOf(t, p, `.code .line-row:has(.finding-card) .line`); !strings.Contains(got, "This is very good.") {
				t.Errorf("finding card is on the wrong row: %q%s", got, diag())
			}
			noHorizontalScroll(t, p, diag)
			// The reveal must not rewrite the reviewer's saved Preview/Raw choice.
			if prefs, _ := os.ReadFile(filepath.Join(filepath.Dir(repo), "prereview-ui-prefs.json")); strings.Contains(string(prefs), `"raw_markdown":true`) {
				t.Errorf("selecting a check flipped the durable raw_markdown pref: %s", prefs)
			}

			// Queue it: an ordinary text comment anchored to the span.
			if err := chromedp.Run(ctx,
				chromedp.Click(`.code .finding-card button[name="queueFinding"]`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("queue the finding: %v%s", err, diag())
			}
			waitJSTrue(t, p.ctx, `[...document.querySelectorAll('.code .inline-comment')].some(c => c.textContent.includes('adds emphasis'))`,
				10*time.Second, "the queued finding shows as a comment card")
			cm := onlyComment(t, p)
			if cm.Kind != "text" || cm.FromLine != 40 || cm.ToLine != 40 || cm.FromCol != 8 || cm.ToCol != 12 || cm.Side != "new" || cm.File != "notes.md" {
				t.Errorf("queued comment %+v%s", cm, diag())
			}
			if n := countSel(t, p, `.code mark.comment-span.check-finding`); n != 1 {
				t.Errorf("the commented span must carry both marks; got %d%s", n, diag())
			}

			// Disable it: it leaves the count and its marks go.
			if err := chromedp.Run(ctx,
				chromedp.Click(checkRow("filler-intensifiers")+` button[name="toggleCheckEnabled"]`, chromedp.ByQuery),
				chromedp.WaitVisible(checkRow("filler-intensifiers")+`.is-disabled`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("disable the check: %v%s", err, diag())
			}
			if got := textOf(t, p, `.checks-trigger .checks-count`); got != "2/2" {
				t.Errorf("after disable count = %q, want 2/2%s", got, diag())
			}
			if n := countSel(t, p, `mark.check-finding`); n != 0 {
				t.Errorf("a disabled check still draws %d marks%s", n, diag())
			}
		})
	}
}

// onlyComment reads the review's single comment through the agent's own CLI.
func onlyComment(t *testing.T, p *runningPrereview) streamComment {
	t.Helper()
	out, err := exec.Command(p.binary, "comments", "--json", "--out", p.store).Output()
	if err != nil {
		t.Fatalf("prereview comments: %v", err)
	}
	var cs []streamComment
	if err := json.Unmarshal(out, &cs); err != nil || len(cs) != 1 {
		t.Fatalf("comments --json (%v): %s", err, out)
	}
	return cs[0]
}

type streamComment struct {
	Kind     string `json:"kind"`
	File     string `json:"file"`
	FromLine int    `json:"from_line"`
	ToLine   int    `json:"to_line"`
	FromCol  int    `json:"from_col"`
	ToCol    int    `json:"to_col"`
	Side     string `json:"side"`
	Body     string `json:"body"`
}

func noHorizontalScroll(t *testing.T, p *runningPrereview, diag func() string) {
	t.Helper()
	var over int
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.scrollingElement.scrollWidth - window.innerWidth`, &over)); err != nil {
		t.Fatal(err)
	}
	if over > 0 {
		t.Errorf("page scrolls horizontally by %dpx%s", over, diag())
	}
}

// Editing a rule file on disk updates the open panel with no reload; a broken rule
// shows its parse error on its row; a new file adds a row.
func TestE2E_CheckRuleEditedLive(t *testing.T) {
	repo := setupFixtureRepoChecks(t, "notes.md")
	writeRule(t, repo, "house.yml", "extends: existence\nname: House\nmessage: \"No '%s'.\"\ntokens: [Notes]\n")
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.checks-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(checkRow("house")+`.is-fail`, chromedp.ByQuery),
		chromedp.Evaluate(`window.__noReload = 1`, nil),
	); err != nil {
		t.Fatalf("open panel with the user rule failing: %v%s", err, diag())
	}

	time.Sleep(20 * time.Millisecond) // a distinct mtime
	writeRule(t, repo, "house.yml", "extends: existence\nname: House\ntokens: [nothing-matches-this]\n")
	if err := chromedp.Run(ctx, chromedp.WaitVisible(checkRow("house")+`.is-pass`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the edited rule didn't re-run live: %v%s", err, diag())
	}

	writeRule(t, repo, "house.yml", "extends: [")
	if err := chromedp.Run(ctx, chromedp.WaitVisible(checkRow("house")+`.is-error .check-error`, chromedp.ByQuery)); err != nil {
		t.Fatalf("a broken rule must show its parse error: %v%s", err, diag())
	}
	if got := textOf(t, p, checkRow("house")+` .check-error`); !strings.Contains(got, "invalid YAML") {
		t.Errorf("error text %q%s", got, diag())
	}

	writeRule(t, repo, "second.yml", "extends: existence\nname: Second\ntokens: [Plain]\n")
	if err := chromedp.Run(ctx, chromedp.WaitVisible(checkRow("second")+`.is-fail`, chromedp.ByQuery)); err != nil {
		t.Fatalf("a new rule file didn't appear: %v%s", err, diag())
	}
	var still int
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__noReload || 0`, &still)); err != nil || still != 1 {
		t.Errorf("the page reloaded (marker %d, %v)%s", still, err, diag())
	}
	// The user rule's row offers its file (no editor set: the path to copy).
	if got := countSel(t, p, checkRow("second")+` input.check-path`); got != 1 {
		t.Errorf("a user rule row must show its path%s", diag())
	}
}

// A rule hitting every line must not unfold the file until it is selected. Plain
// text, so Diff view folds (a rendered Markdown file is revealed whole instead).
func TestE2E_ChecksFindingsBounded(t *testing.T) {
	repo := setupFixtureRepoChecks(t, "notes.txt")
	writeRule(t, repo, "noisy.yml", "extends: existence\nname: Noisy\ntokens: [Plain]\n")
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.checks-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(checkRow("noisy")+`.is-fail`, chromedp.ByQuery),
		chromedp.Click(checkRow("filler-intensifiers")+` .check-name`, chromedp.ByQuery),
		chromedp.WaitVisible(`.code .finding-card`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("setup: %v%s", err, diag())
	}
	if n := countSel(t, p, `.code .fold-row`); n < 1 {
		t.Fatalf("with a one-finding check selected the file must still fold; got %d%s", n, diag())
	}
	if n := countSel(t, p, `.code .finding-card`); n != 1 {
		t.Errorf("only the selected check's findings are carded; got %d%s", n, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(checkRow("noisy")+` .check-name`, chromedp.ByQuery),
		chromedp.WaitVisible(checkRow("noisy")+`.is-selected`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("select noisy: %v%s", err, diag())
	}
	waitJSTrue(t, p.ctx, `document.querySelectorAll('.code .finding-card').length === 58`, 10*time.Second,
		"selecting the noisy check cards all 58 of its findings")
	if n := countSel(t, p, `.code .fold-row`); n != 0 {
		t.Errorf("the selected noisy check's rows should all be unfolded; %d fold rows left%s", n, diag())
	}
}

// A repo's Vale config never runs until the reviewer trusts the repo (a config
// can make vale start other tools). The panel offers the trust button; after it,
// vale's findings show as rows.
func TestE2E_ChecksValeTrust(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "vale-ran")
	fake := "#!/bin/sh\n: > '" + marker + "'\nprintf '%s' '{\"notes.md\":[{\"Check\":\"Demo.Weasel\",\"Message\":\"Avoid very.\",\"Severity\":\"warning\",\"Match\":\"very\",\"Line\":40,\"Span\":[9,12]}]}'\n"
	if err := os.WriteFile(filepath.Join(bin, "vale"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := setupFixtureRepoChecks(t, "notes.md")
	mustWrite(t, repo, ".vale.ini", "StylesPath = styles\n")
	runCmd(t, repo, "git", "add", ".vale.ini")
	runCmd(t, repo, "git", "commit", "-q", "-m", "vale config") // committed, so notes.md stays the changed file
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.checks-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.checks-notice button[name="trustVale"]`, chromedp.ByQuery),
	); err != nil {
		var panel string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.checks-panel')||document.querySelector('.file-head')||{}).outerHTML||'none'`, &panel))
		t.Fatalf("the untrusted Vale config must offer trust: %v\npanel: %.1500s%s", err, panel, diag())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("vale ran before the repo was trusted%s", diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(`.checks-notice button[name="trustVale"]`, chromedp.ByQuery),
		chromedp.WaitVisible(checkRow("vale:Demo.Weasel")+`.is-fail`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.checks-notice`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("trusting didn't run vale: %v%s", err, diag())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("vale never ran after trust%s", diag())
	}
}
