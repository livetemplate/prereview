//go:build browser

// End-to-end for #193: one sticky ‹ N / M open › bar steps through every open item
// (unresolved comment, undecided suggestion, unanswered quiz question) in document
// order across files, at phone size. Every item arrives after the page has loaded,
// so each render is a live update.
//
// Run with: go test -tags=browser -run 'TestE2E_AnnotationNav|TestE2E_QuizStepper' ./e2e/...

package e2e

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// navLabel is the bar's count text ("" when the bar isn't there).
func navLabel(t *testing.T, p *runningPrereview) string {
	t.Helper()
	return strings.TrimSpace(evalStr(t, p, `(document.querySelector('.ann-nav .ann-nav-label')||{}).textContent||""`))
}

// waitNavLabel waits until the bar reads want.
func waitNavLabel(t *testing.T, p *runningPrereview, want, why string, diag func() string) {
	t.Helper()
	ctx, cancel := within(p, 10*time.Second)
	defer cancel()
	js := `((document.querySelector('.ann-nav .ann-nav-label')||{}).textContent||"").trim() === ` + strconv.Quote(want)
	if err := chromedp.Run(ctx, chromedp.Poll(js, nil, chromedp.WithPollingInterval(50*time.Millisecond))); err != nil {
		t.Fatalf("%s: bar reads %q, want %q%s", why, navLabel(t, p), want, diag())
	}
}

// navStep taps ‹ or › on the bar.
func navStep(t *testing.T, p *runningPrereview, action string, diag func() string) {
	t.Helper()
	ctx, cancel := within(p, 10*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Click(`.ann-nav button[name="`+action+`"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("tap %s: %v%s", action, err, diag())
	}
}

// expectInView waits for sel to be laid out inside the viewport: the step's
// scroll landed and nothing collapsed it.
func expectInView(t *testing.T, p *runningPrereview, sel, why string, diag func() string) {
	t.Helper()
	ctx, cancel := within(p, 10*time.Second)
	defer cancel()
	js := `(()=>{const e=document.querySelector(` + strconv.Quote(sel) + `); if(!e||e.offsetParent===null) return false;
		const r=e.getBoundingClientRect(); return r.bottom>0 && r.top<innerHeight})()`
	if err := chromedp.Run(ctx, chromedp.Poll(js, nil, chromedp.WithPollingInterval(50*time.Millisecond))); err != nil {
		t.Fatalf("%s: %s is not on screen%s", why, sel, diag())
	}
}

func TestE2E_AnnotationNav(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 390, 844, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(390, 844)
	p.clickFile("app.go")
	if n := evalInt(t, p, `document.querySelectorAll('.ann-nav').length`); n != 0 {
		t.Fatalf("no open items yet, so no bar; got %d%s", n, diag())
	}

	// app.go: a comment on line 3, a suggestion on line 4. folded.go: a quiz
	// question on line 3 and a suggestion on line 11 (inside a fold).
	addLineComment(t, p, 0, 3, "rename Greet")
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"near","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""},
	  {"id":"far","file":"folded.go","from_line":11,"to_line":11,"original":"func keepMe() {}","proposed":"func keepMe() { _ = 1 }"}
	]`)
	submitQuiz(t, p, `{"id":"zn","file":"folded.go","questions":[
	  {"id":"qn","kind":"line","probe":"consequence","prompt":"QN","options":["x","y"],"answer":0,"why":"w","from_line":3,"to_line":3,"side":"new"}]}`)
	waitNavLabel(t, p, "4 open", "the bar counts the whole review", diag)

	navStep(t, p, "navNext", diag)
	waitNavLabel(t, p, "1 / 4 open", "first step", diag)
	expectInView(t, p, `.inline-comment`, "the comment", diag)
	navStep(t, p, "navNext", diag)
	waitNavLabel(t, p, "2 / 4 open", "second step", diag)
	expectInView(t, p, `.inline-suggestion[data-key="sg-near"]`, "the suggestion on line 4", diag)

	// Deciding the current item drops it; next continues from here, not item 1.
	if err := chromedp.Run(p.ctx, chromedp.Click(`.inline-suggestion[data-key="sg-near"] button[name="rejectSuggestion"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("reject: %v%s", err, diag())
	}
	waitNavLabel(t, p, "3 open", "the decided suggestion leaves the count", diag)
	navStep(t, p, "navNext", diag)
	waitNavLabel(t, p, "2 / 3 open", "next after deciding crosses into folded.go", diag)
	expectInView(t, p, `.quiz-card[data-key="quiz-qn"] .quiz-card-head`, "the folded.go question", diag)

	// n is the keyboard twin of ›.
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent("n")); err != nil {
		t.Fatalf("press n: %v", err)
	}
	waitNavLabel(t, p, "3 / 3 open", "n steps like ›", diag)
	expectInView(t, p, `.inline-suggestion[data-key="sg-far"]`, "the folded.go suggestion, inside a fold", diag)
	navStep(t, p, "navNext", diag)
	waitNavLabel(t, p, "1 / 3 open", "› wraps to the first item", diag)

	// The Annotations filter is per file: Comments off on app.go hides its
	// comment card and drops it as a stop; folded.go's items stay stops.
	pickAnnFilter(t, p, "toggleAnnKind", "comment", diag)
	waitNavLabel(t, p, "2 open", "app.go's comment is no longer a stop", diag)
	if n := evalInt(t, p, `document.querySelectorAll('.inline-comment').length`); n != 0 {
		t.Errorf("Comments off should hide app.go's comment card, got %d%s", n, diag())
	}
	navStep(t, p, "navPrev", diag)
	waitNavLabel(t, p, "2 / 2 open", "‹ wraps to folded.go's suggestion", diag)
	expectInView(t, p, `.inline-suggestion[data-key="sg-far"]`, "folded.go's suggestion, its file filter untouched", diag)

	// None on folded.go drops its stops; the bar stays at 0 (#212).
	pickAnnFilter(t, p, "setAnnShow", "none", diag)
	waitNavLabel(t, p, "0 open", "None leaves nothing to step to", diag)

	// × collapses the bar to its filter chip, which brings it back.
	ctx, cancel := within(p, 10*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.ann-nav button[name="dismissNav"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.ann-nav.is-collapsed .ann-filter-trigger`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("× should collapse the bar to its filter: %v%s", err, diag())
	}
	if got := evalStr(t, p, `document.querySelector('.ann-nav.is-collapsed .ann-filter-trigger').textContent.trim()`); got != "Annotations: None" {
		t.Errorf("chip label = %q, want %q%s", got, "Annotations: None", diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(`.ann-nav.is-collapsed button[name="expandNav"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.ann-nav:not(.is-collapsed) .ann-nav-label`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the chip should bring the bar back: %v%s", err, diag())
	}
}

// pickAnnFilter opens the Annotations filter and clicks the row whose form
// carries value for the named action (setAnnShow/show or toggleAnnKind/kind).
func pickAnnFilter(t *testing.T, p *runningPrereview, action, value string, diag func() string) {
	t.Helper()
	ctx, cancel := within(p, 10*time.Second)
	defer cancel()
	js := `[...document.querySelectorAll('.ann-filter button[name="` + action + `"]')].find(b=>b.form.querySelector('input[value="` + value + `"]')).click()`
	if err := chromedp.Run(ctx,
		chromedp.Click(`.ann-filter > button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.ann-filter.open button[name="`+action+`"]`, chromedp.ByQuery),
		chromedp.Evaluate(js, nil),
	); err != nil {
		t.Fatalf("filter %s=%s: %v%s", action, value, err, diag())
	}
}

// The quiz half of the bar: it replaced the per-question dots, so it must cover
// what they did — appear without a menu, count answers, reach the overview, and
// bring a collapsed question back into view.
func TestE2E_QuizStepper(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 390, 844, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(390, 844)
	p.clickFile("edited.go")
	submitQuiz(t, p, quizJSON)
	waitForQuizEntry(t, p)
	waitNavLabel(t, p, "4 open", "a new quiz's four questions", diag)
	if n := evalInt(t, p, `document.querySelectorAll('.ann-nav button[name="toggleUnderstand"]').length`); n != 1 {
		t.Errorf("a file with a quiz offers the overview from the bar, got %d%s", n, diag())
	}

	if err := chromedp.Run(p.ctx, chromedp.Click(`.quiz-card[data-key='quiz-q1'] .quiz-options li:nth-child(2) button[name='answerQuestion']`, chromedp.ByQuery)); err != nil {
		t.Fatalf("answer q1: %v%s", err, diag())
	}
	waitNavLabel(t, p, "3 open", "an answered question is no longer open", diag)

	// Collapse the file head, then step to its first question (q3, ungrounded, so it
	// renders at the head): the step must open the group, not land on a hidden card.
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.file-head-marks .line-marks').click()`, nil)); err != nil {
		t.Fatalf("collapse the head: %v", err)
	}
	if err := chromedp.Run(p.ctx, chromedp.Poll(`(()=>{const c=document.querySelector('.quiz-card[data-key="quiz-q3"]'); return !c||c.offsetParent===null})()`, nil, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatalf("the head question should be collapsed: %v%s", err, diag())
	}
	navStep(t, p, "navNext", diag)
	waitNavLabel(t, p, "1 / 3 open", "first step", diag)
	expectInView(t, p, `.quiz-card[data-key="quiz-q3"] .quiz-card-head`, "stepping to a collapsed question opens it", diag)
	navStep(t, p, "navNext", diag)
	navStep(t, p, "navNext", diag)
	waitNavLabel(t, p, "3 / 3 open", "third step", diag)
	expectInView(t, p, `.quiz-card[data-key="quiz-q2"] .quiz-card-head`, "q2, the line-1 question, after the two head ones", diag)
}
