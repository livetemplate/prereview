//go:build browser

package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

// explainJSON cites the open file, a second changed file, and a file the review
// doesn't have, so the page shows a jump to another file and one ⚠.
const explainJSON = `{
  "summary": "Changes the greeting and adds a fresh package.",
  "steps": [
    {"title": "The greeting", "body": "Hello now says hello world.",
     "anchors": [{"file": "edited.go", "from_line": 4}]},
    {"title": "A new package", "anchors": [{"file": "fresh.go", "from_line": 1, "to_line": 3}]},
    {"title": "A file that isn't here", "anchors": [{"file": "nope.go", "from_line": 9}]}
  ]
}`

func submitExplain(t *testing.T, p *runningPrereview, payload string) {
	t.Helper()
	runAgentVerb(t, p, "explain", payload)
}

// TestE2E_UnderstandWalkthrough (#115): the reviewer asks for a walkthrough from
// the Understand page; the request is queued as an explain request; the agent's
// `prereview explain` appears live with no reload; every cited line is checked
// (one isn't in the review and reads ⚠); and a step's link opens ANOTHER file at
// the cited line.
func TestE2E_UnderstandWalkthrough(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")
	p.openUnderstandTab("walkthrough")

	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`window.__noReload = 1`, nil),
		chromedp.Click(`button[name='requestExplain']`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("tap Explain this change: %v%s", err, diag())
	}
	waitJSTrue(t, p.ctx, `!!document.querySelector('.understand .request-pending')`, 5*time.Second, "the request reads pending")
	if n := evalInt(t, p, `document.querySelectorAll("button[name='requestExplain']").length`); n != 0 {
		t.Errorf("while the request is pending there must be no second Explain button, found %d%s", n, diag())
	}
	rows := p.readCSV()
	fileCol, bodyCol, requestCol := slices.Index(rows[0], csv.ColFile), slices.Index(rows[0], csv.ColBody), slices.Index(rows[0], csv.ColRequest)
	last := rows[len(rows)-1]
	if len(last) <= requestCol || last[requestCol] != csv.RequestExplain || last[fileCol] != "edited.go" {
		t.Fatalf("Explain this change must queue one explain request on the open file, got %q%s", last, diag())
	}
	if !strings.Contains(last[bodyCol], "prereview explain") || !strings.Contains(last[bodyCol], "2 changed file(s)") {
		t.Errorf("the request body must name the command and the whole-change scope:\n%s", last[bodyCol])
	}

	submitExplain(t, p, explainJSON)
	waitJSTrue(t, p.ctx, `document.querySelectorAll('.walk-step').length === 3`, 10*time.Second, "the walkthrough's steps appear live")
	if got := evalInt(t, p, `window.__noReload || 0`); got != 1 {
		t.Fatalf("the walkthrough must arrive by a live update, not a reload%s", diag())
	}
	if n := evalInt(t, p, `document.querySelectorAll('.walk-anchors .quiz-ungrounded').length`); n != 1 {
		t.Errorf("exactly the nope.go anchor must read ⚠, got %d%s", n, diag())
	}
	if n := evalInt(t, p, `document.querySelectorAll("button[name='jumpToAnchor']").length`); n != 2 {
		t.Errorf("the two grounded anchors must be links, got %d%s", n, diag())
	}
	if n := evalInt(t, p, `document.querySelectorAll("button[name='requestExplain']").length`); n != 1 {
		t.Errorf("once the walkthrough arrives, Explain again is offered, got %d buttons%s", n, diag())
	}

	// A step's link opens the file it cites — here not the open one.
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`document.querySelector('form:has(input[name="file"][value="fresh.go"]) button[name="jumpToAnchor"]').click()`, nil)); err != nil {
		t.Fatalf("click the fresh.go anchor: %v%s", err, diag())
	}
	waitJSTrue(t, p.ctx, `!document.querySelector('.understand') && !!document.querySelector('.code .line.is-cursor')`, 5*time.Second, "the page closes and the cursor lands")
	if got := evalStr(t, p, `document.querySelector('.code .line.is-cursor').getAttribute('data-key')`); got != "L0-1" {
		t.Errorf("the cursor must sit on fresh.go's first line (L0-1), got %q%s", got, diag())
	}
	if got := evalStr(t, p, `document.querySelector('.code .line.is-cursor').closest('[data-file], article').textContent.includes('fresh') ? 'ok' : 'no'`); got != "ok" {
		t.Errorf("the open file must be fresh.go%s", diag())
	}
}

// TestE2E_UnderstandWalkthroughBar (#115 A3): Start the walkthrough opens the
// first step's file at its line, with the step bar on top and the step's marker
// on the line; › walks into the next file; a step that cites nothing says so and
// stays put; the "N open" stepper is out of the way while walking.
func TestE2E_UnderstandWalkthroughBar(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("fresh.go")
	submitExplain(t, p, explainJSON)
	p.openUnderstandTab("walkthrough")
	waitJSTrue(t, p.ctx, `document.querySelectorAll('.walk-step').length === 3`, 10*time.Second, "the steps are listed")

	if err := chromedp.Run(p.ctx, chromedp.Click(`.walk-start button[name='startWalkthrough']`, chromedp.ByQuery)); err != nil {
		t.Fatalf("Start the walkthrough: %v%s", err, diag())
	}
	waitJSTrue(t, p.ctx, `!!document.querySelector('.walk-bar') && !!document.querySelector('.code .line.is-cursor')`, 5*time.Second, "the bar shows and the cursor lands")
	bar := func() string {
		return evalStr(t, p, `document.querySelector('.walk-bar').textContent.replace(/\s+/g, ' ')`)
	}
	if got := bar(); !strings.Contains(got, "Step 1 of 3") || !strings.Contains(got, "The greeting") || !strings.Contains(got, "edited.go L4") {
		t.Errorf("bar at step 1 = %q%s", got, diag())
	}
	if got := evalStr(t, p, `document.querySelector('.code .line.is-cursor').getAttribute('data-key')`); got != "L0-4" {
		t.Errorf("cursor at step 1 = %q, want edited.go's new line 4 (L0-4)%s", got, diag())
	}
	if got := evalStr(t, p, `[...document.querySelectorAll('.code .step-mark')].map(e => e.textContent).join('|')`); got != "Step 1: The greeting" {
		t.Errorf("edited.go's step marker = %q%s", got, diag())
	}
	// The controls share one row, so ‹ and › line up with Steps and ×.
	if got := evalStr(t, p, `(() => { const ys = [...document.querySelectorAll('.walk-bar-nav button')].map(b => { const r = b.getBoundingClientRect(); return r.top + r.height / 2; }); return Math.max(...ys) - Math.min(...ys) <= 2 ? 'one row' : ys.join(','); })()`); got != "one row" {
		t.Errorf("the step bar's buttons must line up in one row, centres at %s%s", got, diag())
	}
	if n := evalInt(t, p, `document.querySelectorAll('.ann-nav:not(.walk-bar)').length`); n != 0 {
		t.Errorf("the open-items stepper must give way to the step bar, found %d%s", n, diag())
	}

	navStep(t, p, "walkNext", diag)
	waitJSTrue(t, p.ctx, `(document.querySelector('.walk-bar .ann-nav-label')||{}).textContent === 'Step 2 of 3'`, 5*time.Second, "› reaches step 2")
	waitJSTrue(t, p.ctx, `(document.querySelector('.code .line.is-cursor')||{getAttribute:()=>''}).getAttribute('data-key') === 'L0-1'`, 5*time.Second, "step 2 opens fresh.go at line 1")
	if got := evalStr(t, p, `(document.querySelector('.code .step-mark')||{}).textContent || ''`); got != "Step 2: A new package" {
		t.Errorf("fresh.go's step marker = %q%s", got, diag())
	}

	navStep(t, p, "walkNext", diag)
	waitJSTrue(t, p.ctx, `(document.querySelector('.walk-bar .ann-nav-label')||{}).textContent === 'Step 3 of 3'`, 5*time.Second, "› reaches step 3")
	if got := bar(); !strings.Contains(got, "cites nothing in this review") {
		t.Errorf("a step citing only nope.go must say it cites nothing here, got %q%s", got, diag())
	}
	if n := evalInt(t, p, `document.querySelectorAll('.walk-bar button[name="walkNext"][disabled]').length`); n != 1 {
		t.Errorf("› must be disabled on the last stop%s", diag())
	}
	if err := chromedp.Run(p.ctx, chromedp.Click(`button[name='endWalkthrough']`, chromedp.ByQuery)); err != nil {
		t.Fatalf("×: %v%s", err, diag())
	}
	waitJSTrue(t, p.ctx, `!document.querySelector('.walk-bar')`, 5*time.Second, "× puts the bar away")
}
