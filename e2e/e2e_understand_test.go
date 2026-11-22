//go:build browser

package e2e

import (
	"slices"
	"strconv"
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
	waitNavLabel(t, p, "Step 2 of 3", "› reaches step 2", diag)
	waitJSTrue(t, p.ctx, `(document.querySelector('.code .line.is-cursor')||{getAttribute:()=>''}).getAttribute('data-key') === 'L0-1'`, 5*time.Second, "step 2 opens fresh.go at line 1")
	if got := evalStr(t, p, `(document.querySelector('.code .step-mark')||{}).textContent || ''`); got != "Step 2: A new package" {
		t.Errorf("fresh.go's step marker = %q%s", got, diag())
	}

	navStep(t, p, "walkNext", diag)
	waitNavLabel(t, p, "Step 3 of 3", "› reaches step 3", diag)
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

// TestE2E_UnderstandNotExplained (#115 A4): a walkthrough that leaves a change
// out gets a derived last step listing it, the page says how many, and walking
// to the end lands on the change, marked in the diff.
func TestE2E_UnderstandNotExplained(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")
	// Only edited.go is explained; fresh.go (wholly added) is left out.
	submitExplain(t, p, `{"steps": [{"title": "The greeting", "anchors": [{"file": "edited.go", "from_line": 4}]}]}`)
	p.openUnderstandTab("walkthrough")
	waitJSTrue(t, p.ctx, `document.querySelectorAll('.walk-step').length === 2`, 10*time.Second, "the agent's step plus the derived one")
	if got := evalStr(t, p, `document.querySelector('.walk-unexplained').textContent.trim()`); !strings.Contains(got, "1 change no step explains") {
		t.Errorf("the page must count the changes no step explains, got %q%s", got, diag())
	}
	last := evalStr(t, p, `(() => { const s = [...document.querySelectorAll('.walk-step')].pop(); return s.className + ' | ' + s.textContent.replace(/\s+/g, ' '); })()`)
	if !strings.Contains(last, "walk-step-unexplained") || !strings.Contains(last, "Not explained") || !strings.Contains(last, "fresh.go L1-L3") {
		t.Errorf("the last step must be the derived Not explained step citing fresh.go L1-L3, got %q%s", last, diag())
	}

	if err := chromedp.Run(p.ctx, chromedp.Click(`.walk-start button[name='startWalkthrough']`, chromedp.ByQuery)); err != nil {
		t.Fatalf("Start the walkthrough: %v%s", err, diag())
	}
	waitNavLabel(t, p, "Step 1 of 2", "the walk starts", diag)
	navStep(t, p, "walkNext", diag)
	waitNavLabel(t, p, "Step 2 of 2", "› reaches the derived step", diag)
	waitJSTrue(t, p.ctx, `(document.querySelector('.code .line.is-cursor')||{getAttribute:()=>''}).getAttribute('data-key') === 'L0-1'`, 5*time.Second, "it opens fresh.go at the left-out change")
	if got := evalStr(t, p, `(document.querySelector('.code .step-mark')||{}).textContent || ''`); got != "Step 2: Not explained" {
		t.Errorf("the left-out change must be marked in the diff, got %q%s", got, diag())
	}
}

// TestE2E_UnderstandImpact (#115 A′2): a step's "Affects" link opens a file the
// review doesn't list (history.go is committed and unchanged) at the cited line;
// an impact anchor past the file's end reads ⚠; concern headings and risk chips
// render.
func TestE2E_UnderstandImpact(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")
	p.openUnderstandTab("walkthrough")

	submitExplain(t, p, `{
  "concerns": [{"id": "greet", "title": "Greeting"}],
  "steps": [
    {"title": "The greeting", "concern": "greet", "anchors": [{"file": "edited.go", "from_line": 4}],
     "risks": [{"kind": "public-api", "note": "Hello's result changed"}],
     "impact": [{"file": "history.go", "from_line": 3, "contains": "Old", "note": "an old caller"},
                {"file": "history.go", "from_line": 9}]}
  ]
}`)
	waitJSTrueDiag(t, p.ctx, `document.querySelectorAll('.walk-impact li').length === 2`, 10*time.Second, "the impact links appear live", diag)
	if got := evalStr(t, p, `document.querySelector('.walk-concern').textContent`); got != "Greeting · 1 step" {
		t.Errorf("concern heading = %q, want %q%s", got, "Greeting · 1 step", diag())
	}
	if got := evalStr(t, p, `document.querySelector('.walk-risks .risk-chip').textContent`); got != "public-api" {
		t.Errorf("risk chip = %q, want public-api%s", got, diag())
	}
	if n := evalInt(t, p, `document.querySelectorAll('.walk-impact .quiz-ungrounded').length`); n != 1 {
		t.Errorf("the line past history.go's end must read ⚠, got %d%s", n, diag())
	}

	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`document.querySelector('.walk-impact form:has(input[name="file"][value="history.go"]) button[name="jumpToAnchor"]').click()`, nil)); err != nil {
		t.Fatalf("click the history.go impact link: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, `!document.querySelector('.understand') && !!document.querySelector('.code .line.is-cursor')`, 5*time.Second, "the page closes and the cursor lands", diag)
	if got := evalStr(t, p, `document.querySelector('.code .line.is-cursor').getAttribute('data-key')`); got != "L3-3" {
		t.Errorf("the cursor must sit on history.go's line 3 (L3-3), got %q%s", got, diag())
	}
	if got := evalStr(t, p, `document.querySelector('.code .line.is-cursor .content').textContent`); !strings.Contains(got, "Old") {
		t.Errorf("the cursor line must be history.go's func Old, got %q%s", got, diag())
	}
}

// TestE2E_UnderstandDiagram (#115 A′4): a walkthrough's diagram arrives by a live
// update, its tab appears, mermaid draws it, its cited line is a link, and a
// revised walkthrough (the same id re-appended) redraws it with no reload.
func TestE2E_UnderstandDiagram(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")
	p.openUnderstandTab("walkthrough")
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`window.__noReload = 1`, nil)); err != nil {
		t.Fatal(err)
	}

	payload := func(label string) string {
		return `{"id": "e-diag", "steps": [{"title": "The greeting", "anchors": [{"file": "edited.go", "from_line": 4}]}],
  "diagrams": [{"id": "flow", "title": "Greeting flow", "mermaid": "graph TD\n  Caller --> ` + label + `", "anchors": [{"file": "edited.go", "from_line": 4}]}]}`
	}
	submitExplain(t, p, payload("Hello"))
	waitJSTrueDiag(t, p.ctx, `!!document.querySelector("button[name='setUnderstandTab'][data-tab='diagrams']")`, 10*time.Second, "the Diagrams tab appears live", diag)
	if err := chromedp.Run(p.ctx, chromedp.Click(`button[name="setUnderstandTab"][data-tab="diagrams"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("open the Diagrams tab: %v%s", err, diag())
	}
	svgHas := func(label string) string {
		return `(() => { const s = document.querySelector('.diagram .md-mermaid svg'); return !!s && s.textContent.includes(` + strconv.Quote(label) + `); })()`
	}
	waitJSTrueDiag(t, p.ctx, svgHas("Hello"), 25*time.Second, "mermaid draws the diagram", diag)
	if n := evalInt(t, p, `document.querySelectorAll(".diagram button[name='jumpToAnchor']").length`); n != 1 {
		t.Errorf("the diagram's cited line must be one link, got %d%s", n, diag())
	}

	submitExplain(t, p, payload("Goodbye"))
	waitJSTrueDiag(t, p.ctx, svgHas("Goodbye"), 15*time.Second, "the revised diagram is redrawn", diag)
	if got := evalInt(t, p, `window.__noReload || 0`); got != 1 {
		t.Fatalf("the diagram must update by a live update, not a reload%s", diag())
	}
}

// TestE2E_WalkCaretOnItsLine: after a walkthrough jump, the keyboard caret sits
// on the cursor's line, not on the step marker above it. A row with a marker is
// taller than content-visibility's size estimate, so measuring before it was laid
// out put the caret a marker's height too high; marker rows are now laid out.
func TestE2E_WalkCaretOnItsLine(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 390, 844, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(390, 844)
	p.clickFile("edited.go")
	p.openUnderstandTab("walkthrough")
	submitExplain(t, p, `{"steps": [{"title": "The greeting", "anchors": [{"file": "edited.go", "from_line": 4}]}, {"title": "Before it", "anchors": [{"file": "edited.go", "from_line": 3}]}]}`)
	waitJSTrueDiag(t, p.ctx, `document.querySelectorAll('.walk-step').length >= 2`, 10*time.Second, "the steps arrive", diag)
	if err := chromedp.Run(p.ctx, chromedp.Click(`.walk-start button`, chromedp.ByQuery)); err != nil {
		t.Fatalf("start the walkthrough: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, `!!document.querySelector('.walk-bar') && !!document.querySelector('[data-lvt-text-caret]')`, 5*time.Second, "the walk starts with a caret", diag)
	time.Sleep(500 * time.Millisecond) // a late layout must not move the line away from the caret
	off := evalStr(t, p, `(() => { const c = document.querySelector('[data-lvt-text-caret]').getBoundingClientRect(), l = document.querySelector('.code .line.is-cursor .content').getBoundingClientRect(); return (l.top - c.top).toFixed(1); })()`)
	if v, err := strconv.ParseFloat(off, 64); err != nil || v > 4 || v < -4 {
		t.Errorf("the caret is %spx above its line's text, want within 4px%s", off, diag())
	}
}
