//go:build browser

// End-to-end for #165: annotations are state-coloured and never vanish. A RESOLVED
// comment and an ACCEPTED suggestion collapse to a GREEN count badge (card hidden but
// present); an OPEN comment / unaccepted suggestion show their card + a YELLOW badge.
// Clicking a badge peeks the collapsed done cards. Yellow wins on a mixed line.
//
// Run: go test -tags=browser -run TestE2E_AnnotationBadges ./e2e/...

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_AnnotationBadges(t *testing.T) {
	repo := setupFixtureRepo(t)
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Line 3: an open + a resolved comment (mixed → yellow). Line 5 (the "}"): resolved
	// only (green). edited.go is 5 lines, so both anchor.
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,3,3,new,OPEN comment,2026-06-30T12:00:00Z,false,,,line,,\n" +
		"c2,edited.go,3,3,new,RESOLVED comment,2026-06-30T12:00:00Z,true,,,line,,\n" +
		"c3,edited.go,5,5,new,RESOLVED only,2026-06-30T12:00:00Z,true,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	p := bootChromeAgainstRepo(t, repo, 1400, 900, "--agent")
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("edited.go")
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s1","file":"edited.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""}
	]`)
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.inline-suggestion[data-key="sg-s1"] .sg-old`, chromedp.ByQuery)); err != nil {
		t.Fatalf("suggestion never rendered: %v%s", err, diag())
	}

	// The resolved-only line 7 must have a GREEN comment badge (the vanish bug is fixed),
	// with its card collapsed (present but not visible). One unified badge per line.
	var line5Green bool
	var resolvedShown, openCommentShown int
	_ = chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.line-mark.is-done`, chromedp.ByQuery),
		chromedp.Evaluate(`[...document.querySelectorAll('.line-mark.is-done')].some(e=>e.textContent.trim()==='1')`, &line5Green),
		chromedp.Evaluate(`[...document.querySelectorAll('.inline-comment.is-resolved')].filter(e=>e.offsetParent).length`, &resolvedShown),
		chromedp.Evaluate(`[...document.querySelectorAll('.inline-comment:not(.is-resolved)')].filter(e=>e.offsetParent).length`, &openCommentShown),
	)
	if !line5Green {
		t.Errorf("a resolved-only line must show a GREEN badge (count 1), not vanish%s", diag())
	}
	if resolvedShown != 0 {
		t.Errorf("resolved comment cards should be collapsed by default; %d visible%s", resolvedShown, diag())
	}
	if openCommentShown < 1 {
		t.Errorf("the open comment card should stay visible%s", diag())
	}

	// Line 3 is MIXED (open + resolved comment) → ONE yellow badge counting 2 (open wins).
	var line3Yellow2 bool
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`[...document.querySelectorAll('.line-mark.is-open')].some(e=>e.textContent.trim()==='2')`, &line3Yellow2))
	if !line3Yellow2 {
		t.Errorf("the mixed line should have ONE yellow badge counting 2%s", diag())
	}

	// The open suggestion → yellow badge + card visible.
	var sugYellow, sugCardShown bool
	_ = chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('.line-mark.is-open')`, &sugYellow),
		chromedp.Evaluate(`!!document.querySelector('.inline-suggestion[data-key="sg-s1"]').offsetParent`, &sugCardShown),
	)
	if !sugYellow || !sugCardShown {
		t.Errorf("open suggestion → yellow badge (%v) + visible card (%v)%s", sugYellow, sugCardShown, diag())
	}

	// PEEK the resolved-only line's green badge → its collapsed card reveals. Done BEFORE
	// the accept below, since the accept's re-render strips the client-only peek class.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`[...document.querySelectorAll('.line-row')].find(r => r.querySelector('.line-mark.is-done') && [...r.querySelectorAll('.inline-comment.is-resolved')].some(c=>/RESOLVED only/.test(c.textContent))).querySelector('.line-marks').click()`, nil),
		chromedp.Sleep(300*1e6),
	); err != nil {
		t.Fatalf("peek click: %v%s", err, diag())
	}
	var peekedShown int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`[...document.querySelectorAll('.line-row.row-toggled .inline-comment.is-resolved')].filter(e=>e.offsetParent).length`, &peekedShown))
	if peekedShown < 1 {
		t.Errorf("clicking the green badge should peek the collapsed resolved card%s", diag())
	}

	// Accept the suggestion → its card COLLAPSES behind an ACCENTUATED-yellow badge
	// (is-accepted): decided but not yet applied (#165).
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-suggestion[data-key="sg-s1"] button[name='acceptSuggestion']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.line-row:has(.inline-suggestion[data-key="sg-s1"]) .line-mark.is-accepted`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accept should collapse to an accentuated-yellow badge: %v%s", err, diag())
	}
	var acceptedShown int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`[...document.querySelectorAll('.inline-suggestion[data-key="sg-s1"]')].filter(e=>e.offsetParent).length`, &acceptedShown))
	if acceptedShown != 0 {
		t.Errorf("an accepted suggestion should collapse (card hidden); %d visible%s", acceptedShown, diag())
	}

	// The agent APPLIES it (acks `applied`) → the badge goes GREEN (is-done); still collapsed.
	if out, err := exec.Command(p.binary, "applied", "--out", p.repo, "s1").CombinedOutput(); err != nil {
		t.Fatalf("prereview applied s1: %v\n%s", err, out)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.line-row:has(.inline-suggestion[data-key="sg-s1"]) .line-mark.is-done`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("apply should turn the badge green: %v%s", err, diag())
	}
}
