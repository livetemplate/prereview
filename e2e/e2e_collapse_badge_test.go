//go:build browser

// End-to-end for #159 M4.3b: once the agent APPLIES an accepted suggestion, its
// inline box collapses out of the diff flow to a small ✦ badge in the right margin
// (declutter). Clicking the ✦ re-expands the applied box inline (a peek); the box's
// "Collapse ✦" button collapses it back. Pure view toggle — the edit stays applied.
//
// Run: go test -tags=browser -run TestE2E_AppliedCollapsesToBadge ./e2e/...

package e2e

import (
	"os/exec"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_AppliedCollapsesToBadge(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("app.go")

	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\"","note":"shorter"}
	]`)

	// Accept → the box is still a full card (accepted, pending apply), no ✦ badge.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-s1"] .sg-old`, chromedp.ByQuery),
		chromedp.Click(`button[name='acceptSuggestion']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.sg-verdict-badge.sg-accept`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accept suggestion: %v%s", err, diag())
	}
	var badgeBeforeApply bool
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`!!document.querySelector('.applied-badge')`, &badgeBeforeApply))
	if badgeBeforeApply {
		t.Error("an accepted-but-not-applied suggestion must NOT collapse to a ✦ badge yet")
	}

	// The agent applies + acks → the box collapses to a ✦ badge LIVE (via the watcher).
	if out, err := exec.Command(p.binary, "applied", "--out", p.repo, "s1").CombinedOutput(); err != nil {
		t.Fatalf("prereview applied: %v\n%s", err, out)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.line-margin .applied-badge`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.inline-suggestion[data-key="sg-s1"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("applied suggestion should collapse to a ✦ badge (box gone): %v%s", err, diag())
	}

	// Click the ✦ badge → the applied box re-expands inline; the badge STAYS (marked
	// is-expanded) — it's the sole toggle, there is no collapse button on the box.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.applied-badge`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-s1"] .sg-applied-status`, chromedp.ByQuery),
		chromedp.WaitVisible(`.line-margin .applied-badge.is-expanded`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("clicking the ✦ badge should re-expand the applied box (badge stays): %v%s", err, diag())
	}
	// The expanded applied box shows NO collapse button, NO Undo (revert is M4.2), and
	// NO alarming outdated warning.
	var collapseBtn, undo, outdated bool
	_ = chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('.sg-collapse-btn')`, &collapseBtn),
		chromedp.Evaluate(`!!document.querySelector('button[name="clearSuggestionDecision"]')`, &undo),
		chromedp.Evaluate(`!!document.querySelector('.inline-suggestion[data-key="sg-s1"] .anchor-orig')`, &outdated),
	)
	if collapseBtn {
		t.Error("the applied box must NOT have a collapse button — the ✦ badge is the toggle")
	}
	if undo {
		t.Error("an applied suggestion must not show Undo (revert lands in M4.2)")
	}
	if outdated {
		t.Error("an applied suggestion must not show the alarming 'original text is gone' warning")
	}

	// Click the ✦ badge again → it collapses back to the badge (box gone).
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.applied-badge`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.inline-suggestion[data-key="sg-s1"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.line-margin .applied-badge:not(.is-expanded)`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("clicking the ✦ badge again should re-collapse the box: %v%s", err, diag())
	}
}
