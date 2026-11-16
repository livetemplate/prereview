//go:build browser

// End-to-end for #206: a fork (grouped alternatives the agent offered unasked) that
// nobody picks leaves the comment that asked for it unaddressed, so the Queue panel and
// the End-session dialog count the forks still awaiting the reviewer's pick. Hiding the
// cards doesn't clear the count (hiding is view-only); accepting one alternative does.
//
// Run with: go test -tags=browser -run TestE2E_ForkAwaitingPick ./e2e/...

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_ForkAwaitingPick(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 900, "--agent")
	diag := captureDiag(t, p)
	// Each step gets a deadline so a selector that never matches fails with diagnostics
	// instead of hanging the suite.
	run := func(actions ...chromedp.Action) error {
		ctx, cancel := within(p, 20*time.Second)
		defer cancel()
		return chromedp.Run(ctx, actions...)
	}
	p.waitReady()
	p.clickFile("app.go")

	// Two forks of two alternatives each, both undecided.
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"c1-a","file":"app.go","from_line":4,"original":"\treturn \"hello world\"","proposed":"\treturn \"hi\"","note":"Casual or formal?"},
	  {"id":"c1-b","file":"app.go","from_line":4,"original":"\treturn \"hello world\"","proposed":"\treturn \"Good day\"","note":"Casual or formal?"},
	  {"id":"c2-a","file":"app.go","from_line":3,"original":"func Greet() string {","proposed":"func Hello() string {","note":"Which name?"},
	  {"id":"c2-b","file":"app.go","from_line":3,"original":"func Greet() string {","proposed":"func Welcome() string {","note":"Which name?"}
	]`)

	count := func(want, why string) {
		t.Helper()
		// Clicking a card is a click-away for the Queue dropdown, so (re)open it first.
		if err := run(
			chromedp.Evaluate(`document.querySelector('.queue-dropdown').classList.contains('open') || document.querySelector('.queue-trigger').click()`, nil),
			chromedp.WaitVisible(`.queue-awaiting-pick`, chromedp.ByQuery),
			chromedp.Poll(`document.querySelector('.queue-awaiting-pick').textContent.trim().startsWith(`+"`"+want+" fork"+"`"+`) && document.querySelector('.queue-awaiting-pick').textContent.includes('awaiting your pick')`, nil),
		); err != nil {
			t.Fatalf("%s: want \"%s fork(s) awaiting your pick\" in the Queue panel: %v%s", why, want, err, diag())
		}
	}

	if err := run(chromedp.WaitVisible(`.inline-suggestion[data-key="sg-c2-b"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the four alternatives should render: %v%s", err, diag())
	}
	count("2", "two undecided forks arrive")

	// Hide every card of fork c2: the reviewer can no longer see it, but it still owes a pick.
	if err := run(
		chromedp.Evaluate(`document.querySelector('.inline-suggestion[data-key="sg-c2-a"] button[name="hideSuggestionGroup"]').click()`, nil),
		chromedp.WaitNotPresent(`.inline-suggestion[data-key="sg-c2-a"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("hide fork c2: %v%s", err, diag())
	}
	count("2", "hiding a fork's cards is view-only")

	// Accept one alternative of c1: picked, so only c2 remains.
	if err := run(
		chromedp.Evaluate(`document.querySelector('.inline-suggestion[data-key="sg-c1-a"] button[name="acceptSuggestion"]').click()`, nil),
	); err != nil {
		t.Fatalf("accept c1-a: %v%s", err, diag())
	}
	count("1", "accepting one alternative picks that fork")

	// End session warns about the fork still awaiting a pick (singular wording).
	var warn string
	if err := run(
		chromedp.WaitVisible(`.queue-end-btn`, chromedp.ByQuery), // count() left the panel open
		chromedp.Click(`.queue-end-btn`, chromedp.ByQuery),
		chromedp.WaitVisible(`#confirm-end-session .end-warn-pick`, chromedp.ByQuery),
		chromedp.Text(`#confirm-end-session .end-warn-pick`, &warn, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("End session must warn about an unpicked fork: %v%s", err, diag())
	}
	if !strings.Contains(warn, "1 fork awaiting your pick") || !strings.Contains(warn, "that comment unaddressed") {
		t.Errorf("end-session warning = %q, want the singular fork wording", warn)
	}
}
