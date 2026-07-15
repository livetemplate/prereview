//go:build browser

// End-to-end coverage for issue #98 Phase 2: the reviewer's decision on an LLM
// suggestion — accept / reject, plus undo. The
// verdict is recorded in the server-owned .prereview/suggestion-decisions.jsonl,
// shows as a badge, survives a reload, and auto-drops when the suggestion is
// revised (same id, new proposed text → fingerprint mismatch). Nothing is applied
// to the files — that's the hand-off (Phase 3).
//
// Run with: go test -tags=browser -run TestE2E_SuggestionDecisions ./e2e/...

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_SuggestionDecisions(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("app.go")

	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"code1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi there\"","note":"tighten"}
	]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion button[name="acceptSuggestion"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("suggestion action row never appeared: %v\nstderr: %s", err, p.stderr.String())
	}

	click := func(sel string) {
		if err := chromedp.Run(p.ctx,
			chromedp.Evaluate(`(document.querySelector('`+sel+`')||{click(){}}).click()`, nil),
			chromedp.Sleep(300*time.Millisecond),
		); err != nil {
			t.Fatalf("click %s: %v\nstderr: %s", sel, err, p.stderr.String())
		}
	}
	present := func(sel string) bool {
		var ok bool
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`!!document.querySelector('`+sel+`')`, &ok))
		return ok
	}

	// Accept → the card collapses behind its badge (#165: decided suggestions tuck away).
	// PEEK line 4 to reveal it and confirm the verdict badge; the action row is now Undo.
	click(`button[name="acceptSuggestion"]`)
	p.peekRow(4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion.is-decided.sg-accept .sg-verdict-badge.sg-accept`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accepted badge never appeared: %v\nstderr: %s", err, p.stderr.String())
	}
	if present(`button[name="acceptSuggestion"]`) {
		t.Error("accept/reject buttons should be gone once decided")
	}
	if !present(`button[name="clearSuggestionDecision"]`) {
		t.Error("Undo button should be present once decided")
	}

	// Durable: reload → the badge is re-derived from the decisions file on Mount.
	reload := func() {
		if err := chromedp.Run(p.ctx,
			chromedp.Navigate(p.url),
			chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
			chromedp.Sleep(1*time.Second),
		); err != nil {
			t.Fatalf("reload: %v\nstderr: %s", err, p.stderr.String())
		}
		p.clickFile("app.go")
	}
	reload()
	if !present(`.sg-verdict-badge.sg-accept`) {
		t.Fatalf("accepted badge did not survive reload\nstderr: %s", p.stderr.String())
	}

	// Undo → back to the action row (Accept/Reject).
	click(`button[name="clearSuggestionDecision"]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`button[name="acceptSuggestion"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("action row should return after undo: %v\nstderr: %s", err, p.stderr.String())
	}

	// Fingerprint drop: re-accept the suggestion, then the LLM revises it (same id,
	// new proposed text). The stale "accepted" verdict must NOT ride the new proposal
	// — the card returns to undecided (action row) with the new text.
	//
	// No peekRow here: the earlier peek persists (ToggledRows is server state), and
	// re-accepting returns the row to its collapsed default, which REACTIVATES that
	// stale toggle — so the row is already revealed. A second peek would toggle it
	// back OFF and re-collapse the card (see RowToggled). Wait for the badge directly.
	click(`button[name="acceptSuggestion"]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.sg-verdict-badge.sg-accept`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("re-accepted badge never appeared: %v\nstderr: %s", err, p.stderr.String())
	}
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"code1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return fmt.Sprintf(\"hi %s\", name)","note":"revised per request"}
	]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion button[name="acceptSuggestion"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("revised suggestion should drop its stale decision and show the action row again: %v\nstderr: %s", err, p.stderr.String())
	}
	if present(`.sg-verdict-badge`) {
		t.Error("a revised suggestion must not keep its stale verdict badge")
	}
}
