//go:build browser

// End-to-end for a PURE DELETION suggestion — one whose "proposed" is empty.
//
// `prereview suggest` has always accepted these (suggest_cmd.go validates only
// file + from_line), but the card rendered the after-pane unconditionally, so a
// deletion showed as an EMPTY GREEN BOX — indistinguishable from a bug. This
// matters because a plan-simplify pass is mostly cuts, so deletions are the
// common case for that workflow rather than an edge case.
//
// Asserts the deletion renders AS a deletion, and — the regression that actually
// bit — that no empty .sg-new element is emitted at all.
//
// Run with: go test -tags=browser -run TestE2E_DeletionSuggestion ./e2e/...

package e2e

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_DeletionSuggestion(t *testing.T) {
	p, _, _ := bootChromeStreamRepo(t, setupSuggestionRepo(t))
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("app.go")

	// "del" is a pure deletion (empty proposed); "repl" is an ordinary
	// replacement on the same file, so one page proves both paths at once.
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"del","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"","note":"dead line"},
	  {"id":"repl","file":"app.go","from_line":3,"to_line":3,"original":"func Greet() string {","proposed":"func Greet() (string) {","note":"parens"}
	]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-del"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-repl"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("suggestions never appeared: %v%s", err, diag())
	}

	q := func(sel string) string {
		var s string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`(()=>{const e=document.querySelector('`+sel+`');return e?e.outerHTML:''})()`, &s))
		return s
	}
	count := func(sel string) int {
		var n int
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`document.querySelectorAll('`+sel+`').length`, &n))
		return n
	}

	// --- the deletion card ---------------------------------------------------
	del := `.inline-suggestion[data-key="sg-del"] `

	// 1. THE regression: no after-pane element at all. Previously this existed
	//    and was empty, which is what painted the empty green box.
	if n := count(del + `.sg-new`); n != 0 {
		t.Errorf("deletion card still emits %d .sg-new element(s) — the empty green box is back\n%s",
			n, q(del+`.sg-diff`))
	}
	// 2. It is marked as a deletion, so CSS can strike the before-pane.
	if n := count(del + `.sg-diff.is-deletion`); n != 1 {
		t.Errorf("deletion card missing .sg-diff.is-deletion (got %d)%s", n, diag())
	}
	// 3. The before-pane still carries the text being removed.
	if got := q(del + `.sg-old`); !strings.Contains(got, "hello world") {
		t.Errorf("deletion card lost its before-pane: %q%s", got, diag())
	}
	// 4. A caption stands in for the missing green box, and is non-empty — an
	//    empty caption would reproduce the original bug in a new element.
	var note string
	if err := chromedp.Run(p.ctx, chromedp.Text(del+`.sg-del-note`, &note, chromedp.ByQuery)); err != nil {
		t.Fatalf("deletion caption (.sg-del-note) never rendered: %v%s", err, diag())
	}
	if strings.TrimSpace(note) == "" {
		t.Errorf("deletion caption rendered empty — same failure, new element%s", diag())
	}
	if !strings.Contains(strings.ToLower(note), "delete") {
		t.Errorf("deletion caption does not say what will happen: %q", note)
	}

	// 5. It is still actionable — accept/reject must work on a deletion.
	if n := count(del + `button[name="acceptSuggestion"]`); n != 1 {
		t.Errorf("deletion card has no accept button (got %d) — not decidable%s", n, diag())
	}

	// --- the ordinary replacement card is untouched --------------------------
	repl := `.inline-suggestion[data-key="sg-repl"] `
	if n := count(repl + `.sg-new`); n != 1 {
		t.Errorf("replacement card lost its after-pane (got %d .sg-new)%s", n, diag())
	}
	if n := count(repl + `.sg-diff.is-deletion`); n != 0 {
		t.Errorf("replacement card wrongly marked as a deletion%s", diag())
	}
	if n := count(repl + `.sg-del-note`); n != 0 {
		t.Errorf("replacement card wrongly rendered a deletion caption%s", diag())
	}
}
