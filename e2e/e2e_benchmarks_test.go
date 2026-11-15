//go:build browser

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
)

// benchRequest is the open benchmark judge request (the newest), from the store.
func benchRequest(t *testing.T, p *runningPrereview, after int) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows, _ := csv.Read(filepath.Join(p.store, "comments.csv"))
		var found []string
		for _, r := range rows {
			if library.IsBenchRequest(r.Body) {
				found = append(found, r.Body)
			}
		}
		if len(found) > after {
			return found[len(found)-1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no benchmark request #%d queued (have %d)", after+1, len(found))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

var benchCmd = regexp.MustCompile(`prereview judge --bench (\S+) --run (\S+) --case (\S+) --judge (\S+) --content-hash (\S+) --rubric-hash (\S+)`)

// answerBench plays the agent: every judge command in the request, run as written.
func answerBench(t *testing.T, p *runningPrereview, request, verdict string) {
	t.Helper()
	ms := benchCmd.FindAllStringSubmatch(request, -1)
	if len(ms) == 0 {
		t.Fatalf("the request names no judge command:\n%s", request)
	}
	for _, m := range ms {
		cmd := exec.Command(p.binary, "judge", "--bench", m[1], "--run", m[2], "--case", m[3], "--judge", m[4],
			"--content-hash", m[5], "--rubric-hash", m[6], "--verdict", verdict, "--critique", "Leads with the change.", "--model", "test-model")
		cmd.Env = prefsIsolatedEnv(p.repo)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("prereview judge --bench: %v: %s", err, out)
		}
	}
}

// TestE2E_Benchmark: a benchmark made from the open file runs its checkers at
// once and asks the agent for its judge in one request; the agent's answers
// (the real CLI, commands copied from the request) complete the score; editing
// the rubric and running again marks the earlier run not comparable. Phone.
func TestE2E_Benchmark(t *testing.T) {
	const w, h = 390, 844
	repo := setupFixtureRepoChecks(t, "notes.md")
	writeRule(t, repo, "reader-first.md", readerFirstJudge)
	p := bootChromeAgainstRepo(t, repo, w, h, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(w, h)
	ctx, cancel := within(p, 60*time.Second)
	defer cancel()

	card := `.lib-card[data-key="bench-notes"]`
	if err := chromedp.Run(ctx,
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.Click(`.lib-tabs form:has(input[value="benchmarks"]) button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.lib-bench-new input[name="name"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.lib-bench-new input[name="name"]`, "Notes", chromedp.ByQuery),
		chromedp.Click(`.lib-bench-new button[name="createBenchmark"]`, chromedp.ByQuery),
		chromedp.WaitVisible(card, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("create a benchmark: %v%s", err, diag())
	}
	if got := textOf(t, p, card+` header`); !strings.HasPrefix(got, "1 case · 3 checkers · 1 judge") {
		t.Errorf("benchmark header = %q%s", got, diag())
	}
	if _, err := os.Stat(filepath.Join(libraryDirFor(repo), "benchmarks", "notes", "cases", "notes-md.md")); err != nil {
		t.Fatalf("the open file wasn't snapshotted: %v", err)
	}

	runOnce := func(n int, verdict string) {
		t.Helper()
		if err := chromedp.Run(ctx,
			chromedp.Click(card+` button[name="runBenchmark"]`, chromedp.ByQuery),
			chromedp.WaitVisible(card+` td.bm-wait`, chromedp.ByQuery), // the judge waits for the agent
			chromedp.WaitVisible(card+` td.bm-no`, chromedp.ByQuery),   // the fixture's "very" fails a checker at once
		); err != nil {
			t.Fatalf("run %d: %v%s", n, err, diag())
		}
		answerBench(t, p, benchRequest(t, p, n-1), verdict)
		if err := chromedp.Run(ctx, chromedp.WaitNotPresent(card+` td.bm-wait`, chromedp.ByQuery)); err != nil {
			t.Fatalf("run %d: the agent's verdict didn't land: %v%s", n, err, diag())
		}
	}
	runOnce(1, "pass")
	if got := textOf(t, p, card+` .bm-score`); !strings.HasPrefix(got, "0.") || !strings.Contains(got, "1 run") {
		t.Errorf("score after one run = %q%s", got, diag())
	}

	// Edit the rubric: the next run is a different judge.
	writeRule(t, repo, "reader-first.md", strings.Replace(readerFirstJudge, "Does the text lead", "Does the very first paragraph lead", 1))
	time.Sleep(1500 * time.Millisecond) // the checks watcher's tick reloads the rule
	runOnce(2, "fail")
	if err := chromedp.Run(ctx, chromedp.Click(card+` .bm-runs > summary`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if n := countSel(t, p, card+` .bm-runs tr.bm-incomparable`); n != 1 {
		t.Errorf("the run before the rubric edit isn't marked not comparable (%d)%s", n, diag())
	}
	if got := textOf(t, p, card+` .bm-runs tbody tr:first-child`); !strings.Contains(got, "test-model") {
		t.Errorf("the run doesn't pin the judge model: %q%s", got, diag())
	}
	var sw, cw int
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.documentElement.scrollWidth`, &sw),
		chromedp.Evaluate(`document.documentElement.clientWidth`, &cw),
	); err != nil || sw > cw {
		t.Errorf("the Benchmarks tab scrolls sideways at %dpx: %d > %d%s", w, sw, cw, diag())
	}
}

// TestE2E_JudgeCalibration: with 10 of your answers for the judge's current
// rubric, 8 of them agreeing, the judge is calibrated — its fail counts in the
// file's Checks n/m and turns the button red; one more Disagree drops it to 7 of
// the last 10, back to advisory, out of the count.
func TestE2E_JudgeCalibration(t *testing.T) {
	repo := setupFixtureRepoChecks(t, "notes.md")
	writeRule(t, repo, "reader-first.md", readerFirstJudge)
	rubric := checks.ParseJudge("reader-first", checks.SourceUser, "", []byte(readerFirstJudge)).RubricHash
	var seed strings.Builder
	for i, agree := range []bool{true, true, true, true, true, true, true, true, false, false} { // oldest first
		l := map[bool]string{true: "agree", false: "disagree"}[agree]
		fmt.Fprintf(&seed, `{"judge":"reader-first","rubric_hash":%q,"content_hash":"c%d","verdict":"fail","label":%q,"store":"/past","file":"old%d.md","at":"t%d","when":"2026-11-01T10:%02d:00Z"}`+"\n", rubric, i, l, i, i, i)
	}
	mustWrite(t, libraryDirFor(repo), library.CalibrationFile, seed.String())

	p := bootChromeAgainstRepo(t, repo, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	ctx, cancel := within(p, 40*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.ck-strip button[name="runJudges"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.ck-strip .ck-waiting`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("run judges: %v%s", err, diag())
	}
	playJudge(t, p, repo, onlyComment(t, p).Body, "--file", "notes.md", "--judge", "reader-first", "--verdict", "fail",
		"--critique", "Buries what the reader must do.", "--model", "test-model")
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`.ck-strip .ck-strip-pick button.is-judge`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the verdict didn't render: %v%s", err, diag())
	}
	if got := textOf(t, p, `.checks-trigger .checks-count`); got != "2/4" {
		t.Errorf("a calibrated judge's fail isn't counted: %q, want 2/4%s", got, diag())
	}
	if got := textOf(t, p, `.ck-strip .ck-strip-pick button.is-judge .ck-judge-tag`); got != "judge" {
		t.Errorf("a calibrated judge is tagged %q on the bar%s", got, diag())
	}

	clickStripPick(t, p, "Reader first", diag)
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.jv-card`, chromedp.ByQuery),
		chromedp.Click(`.jv-card form:has(input[name="label"][value="disagree"]) button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.jv-card form:has(input[name="label"][value="disagree"]) button.is-on`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("disagree: %v%s", err, diag())
	}
	deadline := time.Now().Add(10 * time.Second)
	for textOf(t, p, `.checks-trigger .checks-count`) != "2/3" {
		if time.Now().After(deadline) {
			t.Fatalf("one more disagreement didn't drop the judge out of the count: %q%s", textOf(t, p, `.checks-trigger .checks-count`), diag())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := textOf(t, p, `.ck-strip .ck-pill.ck-advisory`); got != "judge · advisory" {
		t.Errorf("the judge card's pill = %q, want judge · advisory%s", got, diag())
	}
}
