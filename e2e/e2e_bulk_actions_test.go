//go:build browser

// End-to-end for the bulk actions: Queue all / Ignore all on an expanded check
// or judge row of Library → Checks, and Resolve all (with Undo) on All
// comments.
//
// Run: go test -tags=browser -run 'TestE2E_BulkFindingActions|TestE2E_ResolveAllUndo' ./e2e/...

package e2e

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

// diskComments is the review's comments.csv.
func diskComments(t *testing.T, p *runningPrereview) []csv.Row {
	t.Helper()
	rows, err := csv.Read(filepath.Join(p.store, "comments.csv"))
	if err != nil {
		t.Fatalf("read comments.csv: %v", err)
	}
	return rows
}

// waitComments waits until comments.csv holds n rows.
func waitComments(t *testing.T, p *runningPrereview, n int, diag func() string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows, _ := csv.Read(filepath.Join(p.store, "comments.csv"))
		if len(rows) == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("comments.csv has %d rows, want %d%s", len(rows), n, diag())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestE2E_BulkFindingActions(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			repo := setupFixtureRepoChecks(t, "notes.md") // "very" once, on line 40
			// A second changed file with three more "very"s.
			mustWrite(t, repo, "tail.md", "# More\n")
			runCmd(t, repo, "git", "add", "tail.md") // notes.md stays changed
			runCmd(t, repo, "git", "commit", "-q", "-m", "tail")
			mustWrite(t, repo, "tail.md", "# More\nIt is very nice.\nIt is very plain.\nA very long line.\n")
			writeRule(t, repo, "reader-first.md", readerFirstJudge)
			p := bootChromeAgainstRepo(t, repo, vp.w, vp.h, "--agent")
			diag := captureDiag(t, p)
			p.waitReadyAt(vp.w, vp.h)
			ctx, cancel := within(p, 60*time.Second)
			defer cancel()

			// The judge fails the open file (the request names it), quoting "very".
			if err := chromedp.Run(ctx, runJudges(),
				chromedp.WaitVisible(judgesWaiting, chromedp.ByQuery)); err != nil {
				t.Fatalf("Run judges: %v%s", err, diag())
			}
			request := onlyComment(t, p)
			m := regexp.MustCompile("Judge `([^`]+)`").FindStringSubmatch(request.Body)
			if m == nil {
				t.Fatalf("no file in the request:\n%s", request.Body)
			}
			quote := map[string]string{"notes.md": "This is very good.", "tail.md": "It is very nice."}[m[1]]
			playJudge(t, p, repo, request.Body, "--file", m[1], "--judge", "reader-first", "--verdict", "fail",
				"--critique", "Buries what the reader must do.", "--quote", quote, "--model", "test-model")
			if err := chromedp.Run(ctx, openCheckStrip(), chromedp.WaitVisible(`.ck-strip .ck-strip-pick button.is-judge`, chromedp.ByQuery)); err != nil {
				var bar string
				_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.ck-strip')||{outerHTML:'no bar'}).outerHTML`, &bar))
				t.Fatalf("the verdict didn't render: %v\nrequest: %s\n%s%s", err, request.Body, bar, diag())
			}
			queued := 1 // the Run judges request

			// Library → Checks → the checker's row: Queue all 4 (both files).
			filler := pageRow("filler-intensifiers")
			if err := chromedp.Run(ctx,
				chromedp.Click(`.library-trigger`, chromedp.ByQuery),
				chromedp.Click(checksTab, chromedp.ByQuery),
				chromedp.Click(filler+` .ckp-row-head`, chromedp.ByQuery),
				chromedp.WaitVisible(filler+` .ckp-bulk`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("expand the checker row: %v%s", err, diag())
			}
			if got := textOf(t, p, filler+` button[name="queueAllFindings"]`); got != "Queue all 4" {
				t.Errorf("Queue all reads %q, want Queue all 4%s", got, diag())
			}
			if got := textOf(t, p, filler+` button[name="dismissAllFindings"]`); got != "Ignore all 4" {
				t.Errorf("Ignore all reads %q, want Ignore all 4%s", got, diag())
			}
			bulkFits(t, p, filler+` .ckp-bulk`, diag)
			if err := chromedp.Run(ctx,
				chromedp.Click(filler+` button[name="queueAllFindings"]`, chromedp.ByQuery),
				chromedp.WaitNotPresent(filler+` button[name="queueAllFindings"]`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("Queue all: %v%s", err, diag())
			}
			queued += 4
			waitComments(t, p, queued, diag)
			if n := countSel(t, p, filler+` .ckp-q.ck-pill`); n != 4 {
				t.Errorf("%d findings read Queued, want 4%s", n, diag())
			}

			// Ignore all: the row passes, and each file offers Show again.
			if err := chromedp.Run(ctx,
				chromedp.Click(filler+` button[name="dismissAllFindings"]`, chromedp.ByQuery),
				chromedp.WaitVisible(filler+`.is-pass`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("Ignore all: %v%s", err, diag())
			}
			if n := countSel(t, p, filler+` button[name="restoreDismissed"]`); n != 2 {
				t.Errorf("%d files offer Show again, want 2%s", n, diag())
			}
			// Ignore all is now pressed, as Ignored 4: pressing it brings them back.
			toggled := filler + ` .ckp-bulk button[name="restoreAllDismissed"][aria-pressed="true"]`
			if got := textOf(t, p, toggled); got != "Ignored 4" {
				t.Errorf("pressed toggle reads %q, want Ignored 4%s", got, diag())
			}
			if err := chromedp.Run(ctx,
				chromedp.Click(toggled, chromedp.ByQuery),
				chromedp.WaitVisible(filler+`.is-fail`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("the row doesn't fail again after Ignored 4: %v%s", err, diag())
			}

			// The judge's row: Queue all 1, Ignore all 1.
			judge := pageRow("reader-first")
			if err := chromedp.Run(ctx,
				chromedp.Click(judge+` .ckp-row-head`, chromedp.ByQuery),
				chromedp.WaitVisible(judge+` .ckp-bulk`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("expand the judge row: %v%s", err, diag())
			}
			if got := textOf(t, p, judge+` button[name="queueAllFindings"]`); got != "Queue all 1" {
				t.Errorf("judge Queue all reads %q%s", got, diag())
			}
			if err := chromedp.Run(ctx,
				chromedp.Click(judge+` button[name="queueAllFindings"]`, chromedp.ByQuery),
				chromedp.WaitNotPresent(judge+` button[name="queueAllFindings"]`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("judge Queue all: %v%s", err, diag())
			}
			queued++
			waitComments(t, p, queued, diag)
			if last := diskComments(t, p)[queued-1]; last.Check != "reader-first" || !strings.Contains(last.Body, "(judge: Reader first)") {
				t.Errorf("queued judge comment %+v%s", last, diag())
			}

			// Ignore all: the verdict reads ignored, the row stops failing, and
			// Show again brings it back.
			if err := chromedp.Run(ctx,
				chromedp.Click(judge+` button[name="dismissAllFindings"]`, chromedp.ByQuery),
				chromedp.WaitVisible(judge+` .jv .ck-ignored`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("judge Ignore all: %v%s", err, diag())
			}
			if got := textOf(t, p, judge+` .ckp-sum`); got != "1 ignored" {
				t.Errorf("judge summary %q, want 1 ignored%s", got, diag())
			}
			if n := countSel(t, p, judge+`.is-fail`); n != 0 {
				t.Errorf("the judge row still fails%s", diag())
			}
			// The verdict's own toggle reads Ignored; pressing it brings it back.
			verdictBtn := judge + ` .jv button[name="toggleIgnoreVerdict"]`
			if got := textOf(t, p, verdictBtn+`[aria-pressed="true"]`); got != "Ignored" {
				t.Errorf("verdict toggle reads %q, want Ignored%s", got, diag())
			}
			if err := chromedp.Run(ctx,
				chromedp.Click(verdictBtn, chromedp.ByQuery),
				chromedp.WaitVisible(judge+` .jv .ck-fail`, chromedp.ByQuery),
				chromedp.WaitVisible(verdictBtn+`[aria-pressed="false"]`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("the verdict's Ignored toggle: %v%s", err, diag())
			}
			// Ignore it from its own button; the row's toggle then reads Ignored 1.
			if err := chromedp.Run(ctx,
				chromedp.Click(verdictBtn, chromedp.ByQuery),
				chromedp.WaitVisible(judge+` .jv .ck-ignored`, chromedp.ByQuery),
				chromedp.Click(judge+` .ckp-bulk button[name="restoreAllDismissed"]`, chromedp.ByQuery),
				chromedp.WaitVisible(judge+` .jv .ck-fail`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("the verdict's Ignore, then the row's Ignored 1: %v%s", err, diag())
			}

			// The page-wide pair acts on every check listed: the checker's 4 and
			// the judge's 1, all queued already, so only Ignore all shows.
			page := `.ckp-bulk-page`
			if got := textOf(t, p, page+` button[name="dismissAllFindings"]`); got != "Ignore all 5" {
				t.Errorf("page-wide Ignore all reads %q, want Ignore all 5%s", got, diag())
			}
			if n := countSel(t, p, page+` button[name="queueAllFindings"]`); n != 0 {
				t.Errorf("page-wide Queue all offered with everything queued%s", diag())
			}
			bulkFits(t, p, page, diag)
			pagePressed := page + ` button[name="restoreAllDismissed"][aria-pressed="true"]`
			if err := chromedp.Run(ctx,
				chromedp.Click(page+` button[name="dismissAllFindings"]`, chromedp.ByQuery),
				chromedp.WaitVisible(pagePressed, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("page-wide Ignore all: %v%s", err, diag())
			}
			if n := countSel(t, p, `.ckp-row.is-fail`); n != 0 {
				t.Errorf("%d rows still fail after page-wide Ignore all%s", n, diag())
			}
			// It stays, pressed, though every group now passes: pressing it
			// brings all five back.
			if got := textOf(t, p, pagePressed); got != "Ignored 5" {
				t.Errorf("page toggle reads %q, want Ignored 5%s", got, diag())
			}
			if err := chromedp.Run(ctx,
				chromedp.Click(pagePressed, chromedp.ByQuery),
				chromedp.WaitVisible(page+` button[name="dismissAllFindings"]`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("page-wide Ignored 5: %v%s", err, diag())
			}
			if got := textOf(t, p, page+` button[name="dismissAllFindings"]`); got != "Ignore all 5" {
				t.Errorf("after the page toggle: %q, want Ignore all 5%s", got, diag())
			}
			noHorizontalScroll(t, p, diag)
		})
	}
}

// bulkFits: the bulk buttons under scope sit on one line inside the viewport.
func bulkFits(t *testing.T, p *runningPrereview, scope string, diag func() string) {
	t.Helper()
	var r struct {
		Rows  int     `json:"rows"`
		Right float64 `json:"right"`
		VW    float64 `json:"vw"`
	}
	js := fmt.Sprintf(`(() => { const b = [...document.querySelectorAll(%q)].map(e => e.getBoundingClientRect());
		return {rows: new Set(b.map(x => Math.round(x.top))).size, right: Math.max(...b.map(x => x.right)), vw: document.documentElement.clientWidth} })()`,
		scope+` button`)
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &r)); err != nil {
		t.Fatal(err)
	}
	if r.Rows != 1 || r.Right > r.VW {
		t.Errorf("bulk buttons: %d rows, right edge %.0f of %.0f%s", r.Rows, r.Right, r.VW, diag())
	}
}

func TestE2E_ResolveAllUndo(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			repo := setupFixtureRepo(t)
			now := time.Now().UTC()
			var rows []csv.Row
			for i := range 3 {
				rows = append(rows, csv.Row{ID: fmt.Sprintf("c%d", i+1), File: "edited.go", FromLine: 1, ToLine: 1, Side: "new",
					Body: fmt.Sprintf("note %d", i+1), CreatedAt: now, Kind: "line"})
			}
			rows = append(rows, csv.Row{ID: "c4", File: "edited.go", FromLine: 1, ToLine: 1, Side: "new",
				Body: "already done", CreatedAt: now, Kind: "line", Resolved: true})
			seedReviewStore(t, repo, rows...)
			p := bootChromeAgainstRepo(t, repo, vp.w, vp.h)
			diag := captureDiag(t, p)
			p.waitReadyAt(vp.w, vp.h)
			ctx, cancel := within(p, 30*time.Second)
			defer cancel()

			resolved := func() int {
				n := 0
				for _, r := range diskComments(t, p) {
					if r.Resolved {
						n++
					}
				}
				return n
			}
			if err := chromedp.Run(ctx,
				chromedp.Evaluate(`document.querySelector('button[name="toggleCommentList"]').click()`, nil),
				chromedp.WaitVisible(`.all-comments .ac-resolve-all button`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("open All comments: %v%s", err, diag())
			}
			if got := textOf(t, p, `.ac-resolve-all button`); got != "Resolve all 3" {
				t.Errorf("button reads %q, want Resolve all 3%s", got, diag())
			}
			// It sits on the header's line, beside Back.
			var sameRow bool
			if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => { const a = document.querySelector('.ac-resolve-all button').getBoundingClientRect(), b = document.querySelector('.ac-head button[name="toggleCommentList"]').getBoundingClientRect(); return a.top < b.bottom && b.top < a.bottom && a.right <= b.left })()`, &sameRow)); err != nil || !sameRow {
				t.Errorf("Resolve all isn't beside Back (%v)%s", err, diag())
			}

			if err := chromedp.Run(ctx,
				chromedp.Click(`.ac-resolve-all button`, chromedp.ByQuery),
				chromedp.WaitVisible(`.undo-toast button[name="undoResolveAll"]`, chromedp.ByQuery),
				chromedp.WaitNotPresent(`.ac-resolve-all`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("Resolve all: %v%s", err, diag())
			}
			if got := textOf(t, p, `.undo-toast span`); got != "Resolved 3 comments" {
				t.Errorf("notice %q%s", got, diag())
			}
			if n := resolved(); n != 4 {
				t.Errorf("%d resolved on disk, want 4%s", n, diag())
			}

			// Undo reopens the three, not the one resolved before.
			if err := chromedp.Run(ctx,
				chromedp.Click(`.undo-toast button[name="undoResolveAll"]`, chromedp.ByQuery),
				chromedp.WaitNotPresent(`.undo-toast`, chromedp.ByQuery),
				chromedp.WaitVisible(`.ac-resolve-all button`, chromedp.ByQuery),
			); err != nil {
				t.Fatalf("Undo: %v%s", err, diag())
			}
			if n := resolved(); n != 1 {
				t.Errorf("%d resolved on disk after Undo, want 1%s", n, diag())
			}
			noHorizontalScroll(t, p, diag)
		})
	}
}
