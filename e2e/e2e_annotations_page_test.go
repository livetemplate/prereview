//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

// TestE2E_AnnotationsPage: the Annotations page lists a comment, a question, the
// quiz and a suggestion that arrives while it is open, under its own filter;
// turning a kind off there leaves the file's own filter alone; a row jumps to
// its card on the file.
func TestE2E_AnnotationsPage(t *testing.T) {
	dir := setupFixtureRepo(t)
	seedReviewStore(t, dir, csv.Row{ID: "c1", File: "edited.go", FromLine: 4, ToLine: 4, Side: "new",
		Body: "COMMENT-ONE check the greeting", Kind: "line", CreatedAt: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)})
	p := bootChromeAgainstRepo(t, dir, 1400, 900, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")
	submitQuiz(t, p, quizJSON)
	runAgentVerb(t, p, "ask", oneTapSet)
	waitJSDiag(t, p, `!!document.querySelector('.quiz-card') && !!document.querySelector('.question-card')`, "quiz and question cards on the file", diag)

	ctx, cancel := within(p, 10*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Click(`header.bar button[name="toggleCommentList"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`section.all-comments`, chromedp.ByQuery)); err != nil {
		t.Fatalf("open the Annotations page: %v%s", err, diag())
	}
	if got := textOf(t, p, `.all-comments .ac-head strong`); got != "Annotations" {
		t.Errorf("page title = %q, want Annotations", got)
	}
	// The heading names the page, so the filter button doesn't repeat it.
	if got := textOf(t, p, `.all-comments .ann-filter-trigger`); got != "Filter: Open" {
		t.Errorf("page filter button = %q, want Filter: Open", got)
	}
	waitJSDiag(t, p, `!!document.querySelector('.all-comments .ac-item[data-key="c1"]') && !!document.querySelector('.all-comments .ac-row[data-key="q:q1"]') && document.querySelectorAll('.all-comments .ac-row[data-key^="z:"]').length > 0`,
		"the comment, the question and the quiz are listed", diag)

	// A suggestion sent while the page is open arrives as a live update.
	submitSuggestions(t, p.binary, p.repo, `[{"id":"sg1","file":"edited.go","from_line":4,"to_line":4,"original":"\treturn \"hello world\"","proposed":"\treturn \"hi\"","note":"SUGGESTION-ONE shorter"}]`)
	waitJSDiag(t, p, `((document.querySelector('.all-comments .ac-row[data-key="s:sg1"] .ac-body')||{}).textContent||'') === 'SUGGESTION-ONE shorter'`,
		"the new suggestion's row, live", diag)
	if n := evalInt(t, p, `document.querySelectorAll('.all-comments .ac-item[data-key="c1"]').length`); n != 1 {
		t.Errorf("after the live update the comment card must still be there once, got %d%s", n, diag())
	}

	// The page's filter: Quiz off drops the quiz rows only.
	p.clickFilterRow("toggleAnnKind", "quiz")
	waitJSDiag(t, p, `document.querySelectorAll('.all-comments .ac-row[data-key^="z:"]').length === 0`, "quiz rows gone", diag)
	if n := evalInt(t, p, `document.querySelectorAll('.all-comments .ac-row[data-key="s:sg1"], .all-comments .ac-row[data-key="q:q1"], .all-comments .ac-item[data-key="c1"]').length`); n != 3 {
		t.Errorf("Quiz off must keep the comment, question and suggestion, %d left%s", n, diag())
	}

	// A row opens its card on the file, where the file's own filter still shows
	// the quiz.
	ctx2, cancel2 := within(p, 10*time.Second)
	defer cancel2()
	if err := chromedp.Run(ctx2, chromedp.Click(`.all-comments .ac-row[data-key="q:q1"] button[name="jumpToAnnotation"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("jump from the question row: %v%s", err, diag())
	}
	waitJSDiag(t, p, `!document.querySelector('section.all-comments') && !!document.querySelector('.quiz-card')`,
		"the page closes onto the file, quiz cards still shown there", diag)

	// None empties the page and says why; the filter stays to undo it.
	p.viewAction("toggleCommentList")
	waitJSDiag(t, p, `!!document.querySelector('section.all-comments .ann-filter')`, "the page reopens", diag)
	p.clickFilterRow("setAnnShow", "none")
	waitJSDiag(t, p, `!document.querySelector('.all-comments .ac-list') && /The filter hides every annotation/.test(document.querySelector('.all-comments .ac-empty-msg').textContent) && !!document.querySelector('.all-comments .ann-filter')`,
		"None lists nothing and keeps the filter", diag)
}
