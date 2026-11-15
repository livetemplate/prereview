//go:build browser

// End-to-end for judges (checks M2): a judge is a Markdown rubric in the user's
// checks folder; "Run judges" in the file bar queues one request to the agent;
// the agent answers with `prereview judge` (the test plays the agent); verdicts
// render live, join the bar, carry a critique card with Show in file and
// Agree/Disagree, count in the header's one n/m, and go stale when the file
// changes.
//
// Run: go test -tags=browser -run 'TestE2E_Judges' ./e2e/...

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

const readerFirstJudge = "---\nkind: judge\nname: Reader first\ncategory: Reader-first\n---\n" +
	"Does the text lead with what the reader must do?\n"

// playJudge records a verdict the way the agent does: the real CLI, with the
// content and rubric hashes copied from the queued request's command and the
// same checks folder the server reads.
func playJudge(t *testing.T, p *runningPrereview, repo, request string, args ...string) {
	t.Helper()
	m := regexp.MustCompile(`--content-hash (\S+) --rubric-hash (\S+)`).FindStringSubmatch(request)
	if m == nil {
		t.Fatalf("the request has no hashes to copy:\n%s", request)
	}
	args = append([]string{"judge", "--out", p.store, "--content-hash", m[1], "--rubric-hash", m[2]}, args...)
	cmd := exec.Command(p.binary, args...)
	cmd.Env = prefsIsolatedEnv(repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prereview judge: %v: %s", err, out)
	}
}

func TestE2E_Judges(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			repo := setupFixtureRepoChecks(t, "notes.md")
			writeRule(t, repo, "reader-first.md", readerFirstJudge)
			p := bootChromeAgainstRepo(t, repo, vp.w, vp.h, "--agent")
			diag := captureDiag(t, p)
			p.waitReadyAt(vp.w, vp.h)
			ctx, cancel := within(p, 30*time.Second)
			defer cancel()

			// Not run yet: offered on the bar, not counted in the header.
			if err := chromedp.Run(ctx, chromedp.WaitVisible(`.ck-strip.is-idle button[name="runJudges"]`, chromedp.ByQuery)); err != nil {
				t.Fatalf("no Run judges on the bar: %v%s", err, diag())
			}
			if got := textOf(t, p, `.ck-strip button[name="runJudges"]`); got != "Run judges (1)" {
				t.Errorf("run button %q%s", got, diag())
			}
			if got := textOf(t, p, `.checks-trigger .checks-count`); got != "2/3" {
				t.Errorf("a judge with no verdict isn't counted: %q%s", got, diag())
			}

			// Run judges: one queued file-level request, and the bar waits.
			if err := chromedp.Run(ctx,
				chromedp.Click(`.ck-strip button[name="runJudges"]`, chromedp.ByQuery),
				chromedp.WaitVisible(`.ck-strip .ck-waiting`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("Run judges: %v%s", err, diag())
			}
			if got := textOf(t, p, `.ck-strip .ck-waiting`); got != "Asked your agent · 1 judge" {
				t.Errorf("waiting %q%s", got, diag())
			}
			request := onlyComment(t, p)
			if request.Kind != "file" || !strings.Contains(request.Body, "--judge reader-first") {
				t.Errorf("queued request %+v%s", request, diag())
			}

			// The agent answers (fail, quoting line 40): live, no reload.
			if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__noReload = 1`, nil)); err != nil {
				t.Fatal(err)
			}
			playJudge(t, p, repo, request.Body, "--file", "notes.md", "--judge", "reader-first", "--verdict", "fail",
				"--critique", "Buries what the reader must do.", "--quote", "This is very good.", "--model", "test-model")
			if err := chromedp.Run(ctx, chromedp.WaitVisible(`.ck-strip .ck-strip-pick button.is-judge`, chromedp.ByQuery)); err != nil {
				t.Fatalf("the verdict didn't render live: %v%s", err, diag())
			}
			if n := countSel(t, p, `.ck-strip .ck-waiting`); n != 0 {
				t.Errorf("still waiting after the answer%s", diag())
			}
			// Not calibrated yet (M5): listed on the bar as advisory, not counted.
			if got := textOf(t, p, `.checks-trigger .checks-count`); got != "2/3" {
				t.Errorf("an advisory judge's verdict counted: %q, want 2/3%s", got, diag())
			}
			if got := textOf(t, p, `.ck-strip .ck-strip-pick button.is-judge .ck-judge-tag`); got != "judge · advisory" {
				t.Errorf("bar tag = %q, want judge · advisory%s", got, diag())
			}

			// Pick it: the critique card, one-row bar, Show in file lands on the quote.
			clickStripPick(t, p, "Reader first", diag)
			if err := chromedp.Run(ctx, chromedp.WaitVisible(`.jv-card`, chromedp.ByQuery)); err != nil {
				t.Fatalf("no critique card: %v%s", err, diag())
			}
			if got := textOf(t, p, `.jv-card .jv-critique`); got != "Buries what the reader must do." {
				t.Errorf("critique %q%s", got, diag())
			}
			var rows int
			if err := chromedp.Run(ctx, chromedp.Evaluate(`new Set([...document.querySelector('.ck-strip').children].filter(e => e.offsetParent).map(e => Math.round(e.getBoundingClientRect().top / 8))).size`, &rows)); err != nil || rows != 1 {
				t.Errorf("the judge bar spans %d rows (%v)%s", rows, err, diag())
			}
			if err := chromedp.Run(ctx,
				chromedp.Click(`.jv-card button[name="jumpToFinding"]`, chromedp.ByQuery),
			); err != nil {
				t.Fatal(err)
			}
			waitJSTrue(t, p.ctx, `(() => { const l = document.querySelector('.code .line.is-cursor .content'); return !!l && l.textContent.includes('This is very good.') })()`,
				5*time.Second, "Show in file moves the cursor to the quoted line")
			noHorizontalScroll(t, p, diag)

			// Back at the top: Show in file scrolled to the quote, and the card sits
			// above the file (under the sticky bars when scrolled into view from below).
			if err := chromedp.Run(ctx,
				chromedp.Evaluate(`(() => { const v = document.querySelector('main.viewer') || document.scrollingElement; v.scrollTop = 0; window.scrollTo(0, 0); })()`, nil),
				chromedp.Sleep(150*time.Millisecond),
			); err != nil {
				t.Fatal(err)
			}
			// Queue as comment (the quote was found: a text comment on it): the
			// card says Queued instead of offering it again.
			queueJudgmentOnce(t, p, diag)
			judgeCardFits(t, p, vp.name, map[string]string{"phone": "wrap", "desktop": "one line"}[vp.name], diag)

			// Agree persists across a reload, and so does Queued.
			if err := chromedp.Run(ctx,
				chromedp.Click(`.jv-card button[name="labelJudgment"][aria-pressed="false"]`, chromedp.ByQuery),
				chromedp.WaitVisible(`.jv-card button[name="labelJudgment"][aria-pressed="true"]`, chromedp.ByQuery),
				chromedp.Reload(),
			); err != nil {
				t.Fatalf("Agree: %v%s", err, diag())
			}
			p.waitReadyAt(vp.w, vp.h) // the server-rendered card exists before the socket; clicks need the socket
			if err := chromedp.Run(ctx, chromedp.WaitVisible(`.jv-card button[name="labelJudgment"][aria-pressed="true"]`, chromedp.ByQuery)); err != nil {
				t.Fatalf("Agree didn't persist: %v%s", err, diag())
			}
			if got := textOf(t, p, `.jv-card button[aria-pressed="true"]`); got != "Agree" {
				t.Errorf("pressed answer %q%s", got, diag())
			}
			assertQueued(t, p, diag) // survives the reload too

			// The Checks page: the Judges chip filters to the judge; its row lists
			// the verdict with your answer.
			for _, st := range []struct {
				name string
				act  chromedp.Action
			}{
				{"open the Library", chromedp.Click(`.library-trigger`, chromedp.ByQuery)},
				{"the Checks tab", chromedp.Click(checksTab, chromedp.ByQuery)},
				{"kind chips", chromedp.WaitVisible(`button[name="setChecksKind"]`, chromedp.ByQuery)},
				{"Judges chip", chromedp.Evaluate(`[...document.querySelectorAll('button[name="setChecksKind"]')].find(b => b.textContent.startsWith('Judges')).click()`, nil)},
				{"checkers leave", chromedp.WaitNotPresent(pageRow("filler-intensifiers"), chromedp.ByQuery)},
				{"expand the judge", chromedp.Click(pageRow("reader-first")+` .ckp-row-head`, chromedp.ByQuery)},
				{"its verdicts", chromedp.WaitVisible(pageRow("reader-first")+` .ckp-file.jv`, chromedp.ByQuery)},
			} {
				sctx, scancel := within(p, 8*time.Second)
				err := chromedp.Run(sctx, st.act)
				scancel()
				if err != nil {
					t.Fatalf("judges on the page: %s: %v%s", st.name, err, diag())
				}
			}
			if dir := os.Getenv("PREREVIEW_JUDGE_SHOTS"); dir != "" { // the verdict row shares the card's action groups
				var buf []byte
				if err := chromedp.Run(ctx, chromedp.Screenshot(pageRow("reader-first")+` .ckp-file.jv`, &buf, chromedp.ByQuery)); err == nil {
					_ = os.WriteFile(filepath.Join(dir, strings.NewReplacer("/", "-").Replace(t.Name())+"-page.png"), buf, 0o644)
				}
			}
			if got := textOf(t, p, pageRow("reader-first")+` .ckp-rule`); !strings.Contains(got, "agrees with you 1 of 1") {
				t.Errorf("agreement %q%s", got, diag())
			}

			// Edit the file: after a refresh the verdict is out of date.
			mustWrite(t, repo, "notes.md", "# Notes, edited again\nThis is very good.\n")
			if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
				t.Fatal(err)
			}
			p.waitReadyAt(vp.w, vp.h) // a click before the socket connects is lost
			_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => { const b = document.querySelector('.lib-back button'); if (b) b.click(); })()`, nil))
			_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => { const b = document.querySelector('.ck-strip-close button'); if (b) b.click(); })()`, nil))
			waitJSTrue(t, p.ctx, `(() => { const b = document.querySelector('.ck-strip button[name="runJudges"]'); return !!b && b.textContent.trim() === 'Re-run judges (1 stale)' })()`,
				10*time.Second, "an edited file makes the verdict stale (Re-run judges (1 stale))")
			if _, err := os.Stat(filepath.Join(p.store, "judge-labels.jsonl")); err != nil {
				t.Errorf("labels file: %v", err)
			}
		})
	}
}

// queueJudgmentOnce clicks the open judge card's Queue as comment and waits for
// the card to say Queued in its place, with the critique queued once, marked
// with the judge's ID.
func queueJudgmentOnce(t *testing.T, p *runningPrereview, diag func() string) {
	t.Helper()
	ctx, cancel := within(p, 8*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Click(`.jv-card button[name="queueJudgment"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("Queue as comment: %v%s", err, diag())
	}
	assertQueued(t, p, diag)
	rows := p.readCSV()
	col, marked := slices.Index(rows[0], csv.ColCheck), 0
	for _, r := range rows[1:] {
		if r[col] == "reader-first" {
			marked++
		}
	}
	if marked != 1 {
		t.Errorf("%d comments marked reader-first, want 1: %v%s", marked, rows, diag())
	}
}

// assertQueued waits for the open judge card to say Queued in place of Queue as
// comment.
func assertQueued(t *testing.T, p *runningPrereview, diag func() string) {
	t.Helper()
	ctx, cancel := within(p, 8*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`.jv-card .ck-pill.ck-open`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the judge card doesn't say Queued: %v%s", err, diag())
	}
	if got := textOf(t, p, `.jv-card .ck-pill.ck-open`); got != "Queued" {
		t.Errorf("card pill %q, want Queued%s", got, diag())
	}
	if n := countSel(t, p, `.jv-card button[name="queueJudgment"]`); n != 0 {
		t.Errorf("Queue as comment is still offered%s", diag())
	}
}

// judgeCardFits checks the judge card's action row: the act group (Queue or
// Queued, Show in file) and the calibration group (Agree? …, model) each stay
// on one line inside the card. layout "wrap" wants the calibration group on its
// own line (a phone), "one line" both groups side by side (desktop). PREREVIEW_JUDGE_SHOTS saves a screenshot of the card as <name>.png.
func judgeCardFits(t *testing.T, p *runningPrereview, name, layout string, diag func() string) {
	t.Helper()
	var m struct {
		Card, Agree        struct{ Left, Right, Top, Bottom float64 }
		FirstTop, Height   float64
		AgreeOneLine, Fits bool
	}
	js := `(() => {
		const r = e => { const b = e.getBoundingClientRect(); return {Left: b.left, Right: b.right, Top: b.top, Bottom: b.bottom} };
		const card = document.querySelector('.jv-card'), agree = card.querySelector('.jv-agree');
		const kids = [...card.querySelectorAll('.jv-actions > *')];
		const btns = [...agree.querySelectorAll('button')];
		return {Card: r(card), Agree: r(agree), FirstTop: kids[0].getBoundingClientRect().top,
			Height: btns[0].getBoundingClientRect().height,
			AgreeOneLine: btns.every(b => Math.abs(b.getBoundingClientRect().top - btns[0].getBoundingClientRect().top) < 2),
			Fits: kids.every(k => { const b = k.getBoundingClientRect(); return b.left >= card.getBoundingClientRect().left - 1 && b.right <= card.getBoundingClientRect().right + 1 })};
	})()`
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &m)); err != nil {
		t.Fatalf("measure the judge card: %v%s", err, diag())
	}
	if !m.Fits || !m.AgreeOneLine {
		t.Errorf("the judge card's actions overflow or split mid-group: %+v%s", m, diag())
	}
	if wrapped := m.Agree.Top > m.FirstTop+m.Height/2; wrapped != (layout == "wrap") {
		t.Errorf("the calibration group should be %q, wrapped=%v: %+v%s", layout, wrapped, m, diag())
	}
	if dir := os.Getenv("PREREVIEW_JUDGE_SHOTS"); dir != "" {
		var buf []byte
		if err := chromedp.Run(p.ctx, // back to the top: a file-level Queue scrolled to the new comment inside main.viewer
			chromedp.Evaluate(`(() => { const v = document.querySelector('main.viewer') || document.scrollingElement; v.scrollTop = 0; window.scrollTo(0, 0); })()`, nil),
			chromedp.Sleep(150*time.Millisecond),
			chromedp.Screenshot(`.jv-card`, &buf, chromedp.ByQuery)); err == nil {
			_ = os.WriteFile(filepath.Join(dir, strings.NewReplacer("/", "-").Replace(t.Name())+".png"), buf, 0o644)
		}
	}
}

// A critique whose quote wasn't found (or had none) queues as a file-level
// comment; its card shows Queued too, across a reload.
func TestE2E_JudgeQueuedFileLevel(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			repo := setupFixtureRepoChecks(t, "notes.md")
			writeRule(t, repo, "reader-first.md", readerFirstJudge)
			p := bootChromeAgainstRepo(t, repo, vp.w, vp.h, "--agent")
			diag := captureDiag(t, p)
			p.waitReadyAt(vp.w, vp.h)
			ctx, cancel := within(p, 30*time.Second)
			defer cancel()

			if err := chromedp.Run(ctx,
				chromedp.Click(`.ck-strip button[name="runJudges"]`, chromedp.ByQuery),
				chromedp.WaitVisible(`.ck-strip .ck-waiting`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("Run judges: %v%s", err, diag())
			}
			request := onlyComment(t, p)
			playJudge(t, p, repo, request.Body, "--file", "notes.md", "--judge", "reader-first", "--verdict", "fail",
				"--critique", "Never says what to do.", "--model", "test-model")
			if err := chromedp.Run(ctx, chromedp.WaitVisible(`.ck-strip .ck-strip-pick button.is-judge`, chromedp.ByQuery)); err != nil {
				t.Fatalf("the verdict didn't render live: %v%s", err, diag())
			}
			clickStripPick(t, p, "Reader first", diag)
			if err := chromedp.Run(ctx, chromedp.WaitVisible(`.jv-card`, chromedp.ByQuery)); err != nil {
				t.Fatalf("no critique card: %v%s", err, diag())
			}
			if n := countSel(t, p, `.jv-card button[name="jumpToFinding"]`); n != 0 {
				t.Fatalf("a critique with no quote offers Show in file%s", diag())
			}
			queueJudgmentOnce(t, p, diag)
			judgeCardFits(t, p, vp.name, map[string]string{"phone": "wrap", "desktop": "one line"}[vp.name], diag)

			if err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
				t.Fatal(err)
			}
			p.waitReadyAt(vp.w, vp.h)
			assertQueued(t, p, diag) // survives the reload
		})
	}
}
