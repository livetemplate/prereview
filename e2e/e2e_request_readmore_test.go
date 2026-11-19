//go:build browser

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_RequestReadMore: a request prereview queues for the agent (Run judges)
// shows one line on its card, with the prompt written for the agent behind Read
// more. An open prompt stays open when the card re-renders live (the agent
// replies on it), and the Queue row and All comments show the line alone.
func TestE2E_RequestReadMore(t *testing.T) {
	const summary = "Asked your agent to run this file's judges"
	repo := setupFixtureRepoChecks(t, "notes.md")
	writeRule(t, repo, "reader-first.md", readerFirstJudge)
	p := bootChromeAgainstRepo(t, repo, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	ctx, cancel := within(p, 30*time.Second)
	defer cancel()

	card := `.file-comments .inline-comment`
	prompt := card + ` details.req-prompt > .body`
	if err := chromedp.Run(ctx,
		chromedp.Click(`.ck-strip button[name="runJudges"]`, chromedp.ByQuery),
		chromedp.WaitVisible(card+` details.req-prompt > summary`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Run judges, then the request card: %v%s", err, diag())
	}
	request := onlyComment(t, p)
	if got := textOf(t, p, card+` .req-summary`); got != summary {
		t.Errorf("card summary = %q, want %q%s", got, summary, diag())
	}
	if visible(t, p, prompt) {
		t.Errorf("the prompt shows before Read more%s", diag())
	}
	if got := evalStr(t, p, `getComputedStyle(document.querySelector('`+card+` .req-more'), '::after').content`); got != `"Read more"` {
		t.Errorf("closed label = %s, want \"Read more\"%s", got, diag())
	}

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.__noReload = 1`, nil),
		chromedp.Click(card+` details.req-prompt > summary`, chromedp.ByQuery),
		chromedp.WaitVisible(prompt, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Read more: %v%s", err, diag())
	}
	if got := textOf(t, p, prompt); !strings.Contains(got, "--judge reader-first") {
		t.Errorf("opened prompt = %q, want the agent's instructions%s", got, diag())
	}
	if got := evalStr(t, p, `getComputedStyle(document.querySelector('`+card+` .req-more'), '::after').content`); got != `"Show less"` {
		t.Errorf("open label = %s, want \"Show less\"%s", got, diag())
	}

	// The agent replies on the request: the card re-renders live and the prompt
	// the reviewer opened stays open.
	if _, stderr, code := runCLI(t, 10*time.Second, p.binary, "reply", "--out", p.store, request.ID, "--body", "On it."); code != 0 {
		t.Fatalf("prereview reply: %s", stderr)
	}
	if err := chromedp.Run(ctx, chromedp.WaitVisible(card+` .thread .thread-agent`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the reply didn't render live: %v%s", err, diag())
	}
	if !visible(t, p, prompt) {
		t.Errorf("a live update closed the prompt the reviewer opened%s", diag())
	}
	if evalStr(t, p, `String(window.__noReload)`) != "1" {
		t.Errorf("the page reloaded%s", diag())
	}

	// The Queue row and All comments: the line alone, never the prompt.
	if got := textOf(t, p, `.queue-row .queue-body`); got != summary {
		t.Errorf("queue row = %q, want %q%s", got, summary, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.KeyEvent("a"),
		chromedp.WaitVisible(`.all-comments .ac-body`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("All comments: %v%s", err, diag())
	}
	if got := textOf(t, p, `.all-comments .ac-body`); got != summary {
		t.Errorf("All comments body = %q, want %q%s", got, summary, diag())
	}
	noHorizontalScroll(t, p, diag)
}

// visible reports whether sel matches an element the browser shows.
// checkVisibility, not offsetParent: Chromium hides a closed <details>'s content
// with content-visibility, which keeps its layout box.
func visible(t *testing.T, p *runningPrereview, sel string) bool {
	t.Helper()
	return evalStr(t, p, `String(!!document.querySelector('`+sel+`')?.checkVisibility())`) == "true"
}
