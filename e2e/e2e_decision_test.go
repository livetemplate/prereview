//go:build browser

// End-to-end coverage for issue #98 Phase 2: the reviewer's decision on an LLM
// suggestion — accept / reject / request-revision (with a note), plus undo. The
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

	// Undo → back to the action row.
	click(`button[name="clearSuggestionDecision"]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`button[name="requestRevision"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("action row should return after undo: %v\nstderr: %s", err, p.stderr.String())
	}

	// Request revision → inline note form → type a note → send.
	click(`button[name="requestRevision"]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.sg-revise-form textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.sg-revise-form textarea`, "please keep it formal", chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("revision note form: %v\nstderr: %s", err, p.stderr.String())
	}
	click(`button[name="submitRevision"]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.sg-verdict-badge.sg-revise`, chromedp.ByQuery),
		chromedp.WaitVisible(`.sg-revise-note`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("revision-requested badge/note never appeared: %v\nstderr: %s", err, p.stderr.String())
	}
	readNote := func() string {
		var note string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.sg-revise-note')?.textContent||"").trim()`, &note))
		return note
	}
	if got := readNote(); got != "please keep it formal" {
		t.Errorf("revision note text = %q, want %q", got, "please keep it formal")
	}

	// Edit the note in place: the Edit-note button re-opens the form pre-filled with
	// the existing note; the reviewer can amend and re-send.
	click(`button[name="requestRevision"]`)
	var prefill string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.sg-revise-form textarea`, chromedp.ByQuery),
		chromedp.Value(`.sg-revise-form textarea`, &prefill, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("edit-note form: %v\nstderr: %s", err, p.stderr.String())
	}
	if prefill != "please keep it formal" {
		t.Errorf("edit form should pre-fill the existing note, got %q", prefill)
	}
	// Replace the textarea value (the form POST serializes the current DOM value, so
	// no input-event/debounce race), then send.
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`document.querySelector('.sg-revise-form textarea').value = "on second thought, keep it casual"`, nil)); err != nil {
		t.Fatalf("amend note: %v", err)
	}
	click(`button[name="submitRevision"]`)
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.sg-revise-note`, chromedp.ByQuery)); err != nil {
		t.Fatalf("amended note missing: %v\nstderr: %s", err, p.stderr.String())
	}
	if got := readNote(); got != "on second thought, keep it casual" {
		t.Errorf("amended note = %q, want the edited text", got)
	}

	// Fingerprint drop: the LLM revises the suggestion (same id, new proposed text).
	// The stale "revision requested" verdict must NOT ride the new proposal — the
	// card returns to undecided (action row) with the new text.
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
