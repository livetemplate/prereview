//go:build browser

// End-to-end for #231: Diff view folds long unchanged runs, and a card hangs on
// its line's row, so an annotation on a folded line used to have no visible card
// and the fold row gave no hint. A row carrying a card is now kept out of the
// folds, like a change.
//
// Run: go test -tags=browser -run 'TestE2E_CommentOnFoldedLine|TestE2E_OldSideCommentSurvivesSuggestion' ./e2e/...

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// cardShownJS is truthy once a visible comment card in the code view contains text.
func cardShownJS(text string) string {
	return `[...document.querySelectorAll('.code .inline-comment')].some(c => c.textContent.includes(` + "`" + text + "`" + `) && c.offsetParent !== null)`
}

// A comment made in File view on an unchanged line far from the change must keep
// its card when the reviewer switches back to Diff view, and the rest still folds.
func TestE2E_CommentOnFoldedLine(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoBigDiff(t), 1200, 800) // big.go: line 20 changed of 40
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("big.go")

	ctx, cancel := within(p, 15*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`form[aria-label="File view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
		chromedp.WaitVisible(`.code.file-view`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("switch to File view: %v%s", err, diag())
	}
	addLineComment(t, p, 35, 35, "far from the change")

	if err := chromedp.Run(ctx,
		chromedp.Click(`form[aria-label="File view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.code.file-view`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("switch back to Diff view: %v%s", err, diag())
	}
	waitJSTrue(t, p.ctx, cardShownJS("far from the change"), 10*time.Second,
		"Diff view: the comment on folded line 35 has a visible card")
	if n := countSel(t, p, `.code .fold-row`); n < 1 {
		t.Errorf("Diff view must still fold the unrelated unchanged runs; got %d fold rows%s", n, diag())
	}
	if n := countSel(t, p, `.code .line`); n >= 40 {
		t.Errorf("Diff view must not reveal the whole file; got %d lines%s", n, diag())
	}
}

// A comment on a deleted (old-side) line must survive the agent suggesting an
// edit on the same file: a suggestion used to switch Diff view to the whole
// working-tree file (#98), which drops every del row and the card with it.
func TestE2E_OldSideCommentSurvivesSuggestion(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoBigDiff(t), 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("big.go")
	addLineComment(t, p, 20, 0, "why was this removed")

	submitSuggestions(t, p.binary, p.repo, `[{"id":"far1","file":"big.go","from_line":35,"to_line":35,"original":"line 35","proposed":"line thirty-five","note":"spell it"}]`)
	ctx, cancel := within(p, 15*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.code .inline-suggestion`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the suggestion on line 35 never rendered: %v%s", err, diag())
	}
	waitJSTrue(t, p.ctx, cardShownJS("why was this removed"), 10*time.Second,
		"the old-side comment's card stays visible once the file has a suggestion")
	if n := countSel(t, p, `.code .line.del`); n < 1 {
		t.Errorf("Diff view must keep its del rows with a suggestion on the file; got %d%s", n, diag())
	}
	if n := countSel(t, p, `.code .fold-row`); n < 1 {
		t.Errorf("Diff view must still fold with a suggestion on the file; got %d fold rows%s", n, diag())
	}
}
