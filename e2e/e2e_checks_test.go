//go:build browser

// End-to-end for checks M1/M1b: deterministic checkers. The file header shows
// "passed/total" and opens the Checks page (the Library frame); a bar above the
// code lists the file's failing checks, and picking one marks ITS findings inline
// with cards that queue them as ordinary comments, the bar becoming a finding
// stepper. The page lists every check across every changed file, searchable and
// filtered by status / source / category / file; editing a rule file updates it
// live; and a noisy rule only unfolds rows once it is picked.
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

// pageRow selects a check's row on the Checks page by ID.
func pageRow(id string) string { return fmt.Sprintf(`.ckp-row[data-key="ckp-row-%s"]`, id) }

// clickStripPick taps the bar's button for the named failing check.
func clickStripPick(t *testing.T, p *runningPrereview, name string, diag func() string) {
	t.Helper()
	ok := false
	js := fmt.Sprintf(`(() => { const b = [...document.querySelectorAll('.ck-strip.is-idle .ck-strip-pick button')].find(b => b.textContent.trim().startsWith(%q)); if (!b) return false; b.click(); return true })()`, name)
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &ok)); err != nil || !ok {
		var bar string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.ck-strip')||{}).outerHTML||'no bar'`, &bar))
		t.Fatalf("no bar button for %q (%v): %s%s", name, err, bar, diag())
	}
}

func TestE2E_ChecksPage(t *testing.T) {
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

			// The bar lists the failing check; nothing is drawn until it's picked.
			if err := chromedp.Run(ctx, chromedp.WaitVisible(`.ck-strip.is-idle`, chromedp.ByQuery)); err != nil {
				t.Fatalf("no failing-checks bar: %v%s", err, diag())
			}
			if got := textOf(t, p, `.ck-strip.is-idle .ck-strip-pick`); got != "Filler intensifiers 1" {
				t.Errorf("bar lists %q%s", got, diag())
			}
			if n := countSel(t, p, `mark.check-finding`); n != 0 {
				t.Errorf("an unpicked check drew %d marks%s", n, diag())
			}

			// Pick it: the rendered Markdown shows its source (derived, never the
			// saved pref), the span is marked and carded on line 40, and the bar is
			// now the stepper.
			clickStripPick(t, p, "Filler intensifiers", diag)
			if err := chromedp.Run(ctx,
				chromedp.WaitVisible(`.code mark.check-finding`, chromedp.ByQuery),
				chromedp.WaitVisible(`.code .finding-card`, chromedp.ByQuery),
				chromedp.WaitVisible(`.ck-strip:not(.is-idle) .ck-strip-pos`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("pick the check: %v%s", err, diag())
			}
			if got := textOf(t, p, `.ck-strip .ck-pos-long`); got != "finding 1 of 1 in this file" {
				t.Errorf("stepper says %q%s", got, diag())
			}
			// The stepper is one row at every width (it wrapped on a phone).
			var rows int
			if err := chromedp.Run(ctx, chromedp.Evaluate(`new Set([...document.querySelector('.ck-strip').children].filter(e => e.offsetParent).map(e => Math.round(e.getBoundingClientRect().top / 8))).size`, &rows)); err != nil || rows != 1 {
				t.Errorf("the stepper bar spans %d rows (%v)%s", rows, err, diag())
			}
			if got := textOf(t, p, `.code mark.check-finding`); got != "very" {
				t.Errorf("marked span = %q, want very%s", got, diag())
			}
			if got := textOf(t, p, `.code .line-row:has(.finding-card) .line`); !strings.Contains(got, "This is very good.") {
				t.Errorf("finding card is on the wrong row: %q%s", got, diag())
			}
			noHorizontalScroll(t, p, diag)
			if prefs, _ := os.ReadFile(filepath.Join(filepath.Dir(repo), "prereview-ui-prefs.json")); strings.Contains(string(prefs), `"raw_markdown":true`) {
				t.Errorf("picking a check flipped the durable raw_markdown pref: %s", prefs)
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
			if n := countSel(t, p, `.code .finding-card`); n != 0 {
				t.Errorf("the queued finding still shows its own card (with Queue as comment) under its comment: %d%s", n, diag())
			}

			// × returns the bar to the list and clears the marks.
			if err := chromedp.Run(ctx,
				chromedp.Click(`.ck-strip-close button`, chromedp.ByQuery),
				chromedp.WaitVisible(`.ck-strip.is-idle`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("× on the bar: %v%s", err, diag())
			}
			if n := countSel(t, p, `mark.check-finding`); n != 0 {
				t.Errorf("after × %d marks remain%s", n, diag())
			}

			// The header button opens the Checks page filtered to this file.
			if err := chromedp.Run(ctx,
				chromedp.Click(`.checks-trigger`, chromedp.ByQuery),
				chromedp.WaitVisible(`.library`, chromedp.ByQuery),
				chromedp.WaitVisible(pageRow("filler-intensifiers")+`.is-fail`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("open the Checks page: %v%s", err, diag())
			}
			if got := textOf(t, p, `.ckp-file-chip`); !strings.Contains(got, "notes.md") {
				t.Errorf("file chip %q%s", got, diag())
			}
			if n := countSel(t, p, `.library-trigger.is-on`); n != 1 {
				t.Errorf("the toolbar Library button must show it's open%s", diag())
			}
			// Passing categories fold into one line; the failing one is listed.
			if got := textOf(t, p, `.ckp-more`); !strings.Contains(got, "Clarity (1)") || !strings.Contains(got, "Readability (1)") || !strings.Contains(got, "all pass") {
				t.Errorf("folded passing groups %q%s", got, diag())
			}
			noHorizontalScroll(t, p, diag)

			// Expand the row: findings grouped by file, the rule's actions.
			if err := chromedp.Run(ctx,
				chromedp.Click(pageRow("filler-intensifiers")+` .ckp-row-head`, chromedp.ByQuery),
				chromedp.WaitVisible(pageRow("filler-intensifiers")+` .ckp-file`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("expand the row: %v%s", err, diag())
			}
			if got := textOf(t, p, pageRow("filler-intensifiers")+` .ckp-file header code`); got != "notes.md" {
				t.Errorf("findings grouped under %q%s", got, diag())
			}
			if got := textOf(t, p, pageRow("filler-intensifiers")+` .ckp-at`); got != "L40:8-12" {
				t.Errorf("finding location %q%s", got, diag())
			}
			if n := countSel(t, p, pageRow("filler-intensifiers")+` button[name="copyCheckToUserFolder"]`); n != 1 {
				t.Errorf("a built-in offers Edit a copy%s", diag())
			}
			noHorizontalScroll(t, p, diag)

			// Turn it off: the row says off; back in the file the count drops.
			if err := chromedp.Run(ctx,
				chromedp.Click(pageRow("filler-intensifiers")+` button[name="toggleCheckEnabled"]`, chromedp.ByQuery),
				chromedp.WaitVisible(pageRow("filler-intensifiers")+`.is-off`, chromedp.ByQuery),
				chromedp.Click(`.lib-back button`, chromedp.ByQuery),
				chromedp.WaitNotPresent(`.library`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("turn the check off: %v%s", err, diag())
			}
			if got := textOf(t, p, `.checks-trigger .checks-count`); got != "2/2" {
				t.Errorf("after turning it off count = %q, want 2/2%s", got, diag())
			}
			if n := countSel(t, p, `.ck-strip`); n != 0 {
				t.Errorf("no failing checks left, so no bar%s", diag())
			}
		})
	}
}

// setupFixtureRepoTwoDocs is two changed Markdown files: notes.md with one "very"
// (line 40) and other.md with "very" on lines 3 and 30, plus "clearly" (a weasel
// word) on line 5.
func setupFixtureRepoTwoDocs(t *testing.T) string {
	t.Helper()
	repo := setupFixtureRepoChecks(t, "notes.md")
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		switch i {
		case 3, 30:
			b.WriteString("Another very small point.\n")
		case 5:
			b.WriteString("This is clearly right.\n")
		default:
			fmt.Fprintf(&b, "Other line %d.\n", i)
		}
	}
	mustWrite(t, repo, "other.md", b.String())
	return repo
}

// Search, status chips, the category sidebar and the source picker narrow the
// page across every changed file.
func TestE2E_ChecksSearchFilter(t *testing.T) {
	repo := setupFixtureRepoTwoDocs(t)
	writeRule(t, repo, "house.yml", "extends: existence\nname: House\ncategory: Style\ntokens: [nothing-matches-this]\n")
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.Click(checksTab, chromedp.ByQuery),
		chromedp.WaitVisible(pageRow("filler-intensifiers")+`.is-fail`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open the Library from the toolbar: %v%s", err, diag())
	}
	if n := countSel(t, p, `.ckp-file-chip`); n != 0 {
		t.Errorf("the toolbar opens every file (no file chip)%s", diag())
	}
	if got := textOf(t, p, pageRow("filler-intensifiers")+` .ckp-sum`); got != "3 in 2 files" {
		t.Errorf("review-wide summary %q%s", got, diag())
	}
	if got := textOf(t, p, pageRow("weasel-words")+` .ckp-sum`); got != "1 in 1 file" {
		t.Errorf("weasel summary %q%s", got, diag())
	}

	// Search a flagged word: only the check that flagged it remains.
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`input.ckp-search`, "clearly", chromedp.ByQuery),
		chromedp.WaitNotPresent(pageRow("filler-intensifiers"), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("search: %v%s", err, diag())
	}
	if n := countSel(t, p, `.ckp-row`); n != 1 || countSel(t, p, pageRow("weasel-words")) != 1 {
		t.Errorf("search 'clearly' lists %d rows; want only Weasel words%s", n, diag())
	}
	if got := textOf(t, p, `.ckp-hits`); !strings.HasPrefix(got, "1 check match") {
		t.Errorf("hits line %q%s", got, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => { const i = document.querySelector('input.ckp-search'); i.value = ''; i.dispatchEvent(new Event('input', {bubbles: true})) })()`, nil),
		chromedp.WaitVisible(pageRow("filler-intensifiers"), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("clear search: %v%s", err, diag())
	}

	// Status chip: Passing lists the passing checks (every passing group shown).
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`[...document.querySelectorAll('.ckp-chips .lib-chip')].find(b => b.textContent.startsWith('Passing')).click()`, nil),
		chromedp.WaitVisible(pageRow("house")+`.is-pass`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Passing chip: %v%s", err, diag())
	}
	if n := countSel(t, p, `.ckp-row.is-fail`); n != 0 {
		t.Errorf("Passing chip still lists %d failing rows%s", n, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`[...document.querySelectorAll('.ckp-chips .lib-chip')].find(b => b.textContent.startsWith('All')).click()`, nil),
		chromedp.WaitVisible(pageRow("filler-intensifiers"), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("All chip: %v%s", err, diag())
	}

	// Category sidebar: the rule's own category: is a group.
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`[...document.querySelectorAll('.ckp-cat')].find(b => b.textContent.startsWith('Style')).click()`, nil),
		chromedp.WaitVisible(`.ckp-group[data-key="ckp-group-Style"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(pageRow("filler-intensifiers"), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Style category: %v%s", err, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`[...document.querySelectorAll('.ckp-cat')].find(b => b.textContent.startsWith('All')).click()`, nil),
		chromedp.WaitVisible(pageRow("filler-intensifiers"), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("All categories: %v%s", err, diag())
	}

	// Source picker: Yours lists only your rule.
	if err := chromedp.Run(ctx,
		chromedp.SetValue(`.ckp-source-form select`, "user", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.ckp-source-form select').dispatchEvent(new Event('change', {bubbles: true}))`, nil),
		chromedp.WaitNotPresent(pageRow("filler-intensifiers"), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("source picker: %v%s", err, diag())
	}
	if n := countSel(t, p, `.ckp-row`); n != 1 || countSel(t, p, pageRow("house")) != 1 {
		t.Errorf("source=yours lists %d rows%s", n, diag())
	}
}

// Show in file opens the finding's file with that check picked; the bar steps
// through its findings in this file, wrapping; × clears.
func TestE2E_ChecksShowInFile(t *testing.T) {
	repo := setupFixtureRepoTwoDocs(t)
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.Click(checksTab, chromedp.ByQuery),
		chromedp.WaitVisible(pageRow("filler-intensifiers"), chromedp.ByQuery),
		chromedp.Click(pageRow("filler-intensifiers")+` .ckp-row-head`, chromedp.ByQuery),
		chromedp.WaitVisible(pageRow("filler-intensifiers")+` .ckp-file[data-key="ckp-file-other.md"]`, chromedp.ByQuery),
		chromedp.Click(pageRow("filler-intensifiers")+` .ckp-file[data-key="ckp-file-other.md"] .ckp-show button`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.library`, chromedp.ByQuery),
		chromedp.WaitVisible(`.ck-strip:not(.is-idle)`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Show in file: %v%s", err, diag())
	}
	if got := textOf(t, p, `header.file-head .file-path, header.file-head h2, header.file-head`); !strings.Contains(got, "other.md") {
		t.Errorf("the file shown isn't other.md: %q%s", got, diag())
	}
	if got := textOf(t, p, `.ck-strip .ck-pos-long`); got != "finding 1 of 2 in this file" {
		t.Errorf("stepper %q%s", got, diag())
	}
	if n := countSel(t, p, `.code mark.check-finding`); n != 2 {
		t.Errorf("want both findings marked, got %d%s", n, diag())
	}
	cursorLine := func() string {
		return textOf(t, p, `.code .line.is-cursor .content`)
	}
	waitJSTrue(t, p.ctx, `!!document.querySelector('.code .line.is-cursor')`, 5*time.Second, "the cursor is on the finding")
	if got := cursorLine(); !strings.Contains(got, "Another very small point.") {
		t.Errorf("cursor on %q%s", got, diag())
	}
	first := textOf(t, p, `.code .line.is-cursor .gutter, .code .line.is-cursor`)
	if err := chromedp.Run(ctx,
		chromedp.Click(`.ck-strip-nav form:last-child button`, chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	waitJSTrue(t, p.ctx, `document.querySelector('.ck-strip .ck-pos-long').textContent.trim() === 'finding 2 of 2 in this file'`, 5*time.Second, "› steps to the second finding")
	if got := textOf(t, p, `.code .line.is-cursor .gutter, .code .line.is-cursor`); got == first {
		t.Errorf("› didn't move the cursor row (%q)%s", got, diag())
	}
	if err := chromedp.Run(ctx, chromedp.Click(`.ck-strip-nav form:last-child button`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	waitJSTrue(t, p.ctx, `document.querySelector('.ck-strip .ck-pos-long').textContent.trim() === 'finding 1 of 2 in this file'`, 5*time.Second, "› from the last wraps to the first, in this file")
	if got := textOf(t, p, `header.file-head`); !strings.Contains(got, "other.md") {
		t.Errorf("stepping changed the file%s", diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(`.ck-strip-close button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.ck-strip.is-idle`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("×: %v%s", err, diag())
	}
	if n := countSel(t, p, `mark.check-finding`); n != 0 {
		t.Errorf("× left %d marks%s", n, diag())
	}
	// The bar now lists this file's two failing checks; picking one marks it.
	clickStripPick(t, p, "Weasel words", diag)
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`.code mark.check-finding`, chromedp.ByQuery)); err != nil {
		t.Fatalf("pick from the list: %v%s", err, diag())
	}
	if got := textOf(t, p, `.code mark.check-finding`); got != "clearly" {
		t.Errorf("weasel marks %q%s", got, diag())
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

// Editing a rule file on disk updates the open Checks page with no reload; a
// broken rule shows its parse error on its row; a new file adds a row.
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
		chromedp.WaitVisible(pageRow("house")+`.is-fail`, chromedp.ByQuery),
		chromedp.Click(`button[name="togglePassingGroups"]`, chromedp.ByQuery), // keep passing rows listed
		chromedp.WaitVisible(pageRow("weasel-words"), chromedp.ByQuery),
		chromedp.Evaluate(`window.__noReload = 1`, nil),
	); err != nil {
		t.Fatalf("open the page with the user rule failing: %v%s", err, diag())
	}

	time.Sleep(20 * time.Millisecond) // a distinct mtime
	writeRule(t, repo, "house.yml", "extends: existence\nname: House\ntokens: [nothing-matches-this]\n")
	if err := chromedp.Run(ctx, chromedp.WaitVisible(pageRow("house")+`.is-pass`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the edited rule didn't re-run live: %v%s", err, diag())
	}

	writeRule(t, repo, "house.yml", "extends: [")
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(pageRow("house")+`.is-error`, chromedp.ByQuery),
		chromedp.Click(pageRow("house")+` .ckp-row-head`, chromedp.ByQuery),
		chromedp.WaitVisible(pageRow("house")+` .ckp-err`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a broken rule must show its parse error: %v%s", err, diag())
	}
	if got := textOf(t, p, pageRow("house")+` .ckp-err`); !strings.Contains(got, "invalid YAML") {
		t.Errorf("error text %q%s", got, diag())
	}

	writeRule(t, repo, "second.yml", "extends: existence\nname: Second\ntokens: [Plain]\n")
	if err := chromedp.Run(ctx, chromedp.WaitVisible(pageRow("second")+`.is-fail`, chromedp.ByQuery)); err != nil {
		t.Fatalf("a new rule file didn't appear: %v%s", err, diag())
	}
	var still int
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__noReload || 0`, &still)); err != nil || still != 1 {
		t.Errorf("the page reloaded (marker %d, %v)%s", still, err, diag())
	}
	// The user rule's expanded row offers its file (no editor set: the path to copy).
	if err := chromedp.Run(ctx,
		chromedp.Click(pageRow("second")+` .ckp-row-head`, chromedp.ByQuery),
		chromedp.WaitVisible(pageRow("second")+` input.check-path`, chromedp.ByQuery),
	); err != nil {
		t.Errorf("a user rule row must show its path: %v%s", err, diag())
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
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`.ck-strip.is-idle`, chromedp.ByQuery)); err != nil {
		t.Fatalf("no bar: %v%s", err, diag())
	}
	clickStripPick(t, p, "Filler intensifiers", diag)
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`.code .finding-card`, chromedp.ByQuery)); err != nil {
		t.Fatalf("setup: %v%s", err, diag())
	}
	if n := countSel(t, p, `.code .fold-row`); n < 1 {
		t.Fatalf("with a one-finding check selected the file must still fold; got %d%s", n, diag())
	}
	if n := countSel(t, p, `.code .finding-card`); n != 1 {
		t.Errorf("only the selected check's findings are carded; got %d%s", n, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(`.ck-strip-close button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.ck-strip.is-idle`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("×: %v%s", err, diag())
	}
	clickStripPick(t, p, "Noisy", diag)
	waitJSTrue(t, p.ctx, `document.querySelectorAll('.code .finding-card').length === 58`, 10*time.Second,
		"selecting the noisy check cards all 58 of its findings")
	if n := countSel(t, p, `.code .fold-row`); n != 0 {
		t.Errorf("the selected noisy check's rows should all be unfolded; %d fold rows left%s", n, diag())
	}
}

// A repo's Vale config never runs until the reviewer trusts the repo (a config
// can make vale start other tools). The Checks page offers the trust button in the
// Vale category; after it, vale's findings show as rows.
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
		chromedp.WaitVisible(`.ckp-trust button[name="trustVale"]`, chromedp.ByQuery),
	); err != nil {
		var panel string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.library')||document.querySelector('.file-head')||{}).outerHTML||'none'`, &panel))
		t.Fatalf("the untrusted Vale config must offer trust: %v\npanel: %.1500s%s", err, panel, diag())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("vale ran before the repo was trusted%s", diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(`.ckp-trust button[name="trustVale"]`, chromedp.ByQuery),
		chromedp.WaitVisible(pageRow("vale:Demo.Weasel")+`.is-fail`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.ckp-trust`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("trusting didn't run vale: %v%s", err, diag())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("vale never ran after trust%s", diag())
	}
}

// checksTab is the Library's Checks tab: the toolbar opens the Library on
// Learnings (checks M3), a file's Checks button on Checks.
const checksTab = `.lib-tabs form:has(input[name="tab"][value="checks"]) button`

// TestE2E_FindingIgnoreHide: on a finding card, Hide folds the card for now (the
// underline and the bar's count stay, "1 hidden · show" brings it back); Ignore
// dismisses it — the check passes, Checks n/m goes up — and the Checks page's
// "1 ignored · show again" brings it back. Phone.
func TestE2E_FindingIgnoreHide(t *testing.T) {
	const w, h = 390, 844
	repo := setupFixtureRepoChecks(t, "notes.md") // "This is very good." on line 40
	p := bootChromeAgainstRepo(t, repo, w, h)
	diag := captureDiag(t, p)
	p.waitReadyAt(w, h)
	ctx, cancel := within(p, 40*time.Second)
	defer cancel()
	before := textOf(t, p, `.checks-trigger .checks-count`)

	clickStripPick(t, p, "Filler intensifiers", diag)
	card := `.code .finding-card`
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(card, chromedp.ByQuery),
		chromedp.Click(card+` button[name="hideFinding"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(card, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("hide: %v%s", err, diag())
	}
	if n := countSel(t, p, `.code mark.check-finding`); n != 1 {
		t.Errorf("hiding the card removed its underline (%d marks)%s", n, diag())
	}
	if got := textOf(t, p, `.ck-strip-hidden button`); got != "1 hidden · show" {
		t.Errorf("bar = %q%s", got, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(`.ck-strip-hidden button`, chromedp.ByQuery),
		chromedp.WaitVisible(card, chromedp.ByQuery),
		chromedp.Click(card+` button[name="dismissFinding"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.code mark.check-finding`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("show, then ignore: %v%s", err, diag())
	}
	after := textOf(t, p, `.checks-trigger .checks-count`)
	if after == before || !strings.HasPrefix(after, string(rune(before[0]+1))) {
		t.Errorf("ignoring the only finding should pass the check: %s → %s%s", before, after, diag())
	}

	row := pageRow("filler-intensifiers")
	if err := chromedp.Run(ctx,
		chromedp.Click(`.checks-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library`, chromedp.ByQuery),
		// It passes now; on an unfiltered page its group would fold.
		chromedp.Evaluate(`(document.querySelector('button[name="togglePassingGroups"]')||{click(){}}).click()`, nil),
		chromedp.Click(row+` .ckp-row-head`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .ckp-dismissed button`, chromedp.ByQuery),
	); err != nil {
		var lib string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('`+row+`')||document.querySelector('.library')||{outerHTML:'no library'}).outerHTML.slice(0, 4000)`, &lib))
		t.Fatalf("the Checks page doesn't offer the ignored finding back: %v\n%s%s", err, lib, diag())
	}
	if got := textOf(t, p, row+` .ckp-dismissed button`); got != "1 ignored · show again" {
		t.Errorf("page = %q%s", got, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(row+` .ckp-dismissed button`, chromedp.ByQuery),
		chromedp.WaitVisible(row+`.is-fail`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("show again: %v%s", err, diag())
	}
	noHorizontalScroll(t, p, diag)
}
