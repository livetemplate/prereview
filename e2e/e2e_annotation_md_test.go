//go:build browser

// End-to-end for #165 in the rendered-MARKDOWN view: it has no gutter, so each block
// carries a state badge in its top-right — green when the block's annotations are all
// done (resolved / accepted), yellow when any are open. Done cards collapse; clicking
// the badge peeks them.
//
// Run: go test -tags=browser -run TestE2E_AnnotationBadgesMarkdown ./e2e/...

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_AnnotationBadgesMarkdown(t *testing.T) {
	repo := setupFixtureRepoMarkdown(t) // docs.md: prose block lines 3-5, list block 7-8
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// docs.md's prose paragraph renders PER LINE, so annotations on the same source line
	// share a block. A resolved comment + (accepted) suggestion both on line 4 → one
	// block, count 2, green. An open comment on line 3 → its own block, yellow.
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,docs.md,4,4,new,resolved note,2026-06-30T12:00:00Z,true,,,line,,\n" +
		"c2,docs.md,3,3,new,open note,2026-06-30T12:00:00Z,false,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	p := bootChromeAgainstRepo(t, repo, 1400, 1000, "--agent")
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("docs.md")
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.md-view .md-block-marks`, chromedp.ByQuery)); err != nil {
		t.Fatalf("md-view block badges never rendered: %v%s", err, diag())
	}
	// The line-3 block (open comment c2) carries a YELLOW badge.
	var openBlockYellow int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelectorAll('.md-block-marks .line-mark.is-open').length`, &openBlockYellow))
	if openBlockYellow < 1 {
		t.Errorf("the open-comment block should have a YELLOW badge%s", diag())
	}

	// Submit a suggestion into c1's block (line 4). It's UNDECIDED → visible inline. Accept
	// it → the card collapses and, since the block's other annotation (c1) is resolved, the
	// block badge goes ACCENTUATED yellow (accepted-pending).
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s1","file":"docs.md","from_line":4,"to_line":4,"original":"Second clause EDITED here.","proposed":"Second clause revised here."}
	]`)
	sgBlock := `.md-block:has(.inline-suggestion[data-key="sg-s1"])`
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(sgBlock+` .inline-suggestion[data-key="sg-s1"] button[name='acceptSuggestion']`, chromedp.ByQuery),
		chromedp.Click(sgBlock+` button[name='acceptSuggestion']`, chromedp.ByQuery),
		chromedp.WaitVisible(sgBlock+` .md-block-marks .line-mark.is-accepted`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accept in md-view should turn the block badge accentuated-yellow: %v%s", err, diag())
	}

	// The agent APPLIES it → the block badge goes GREEN, counting 2 (resolved comment +
	// applied suggestion). Done cards stay collapsed.
	if out, err := exec.Command(p.binary, "applied", "--out", p.repo, "s1").CombinedOutput(); err != nil {
		t.Fatalf("prereview applied s1: %v\n%s", err, out)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(sgBlock+` .md-block-marks .line-mark.is-done`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("apply in md-view should turn the block badge green: %v%s", err, diag())
	}

	// The all-done block badge counts 2 (resolved comment + applied suggestion); done cards
	// stay collapsed.
	var greenCount string
	var doneVisible int
	_ = chromedp.Run(p.ctx,
		chromedp.Evaluate(`(document.querySelector('`+sgBlock+` .md-block-marks .line-mark')?.textContent||"").trim()`, &greenCount),
		chromedp.Evaluate(`[...document.querySelectorAll('.md-view .inline-comment.is-resolved, .md-view .inline-suggestion.sg-accept')].filter(e=>e.offsetParent).length`, &doneVisible),
	)
	if greenCount != "2" {
		t.Errorf("the all-done block badge should count 2 (resolved comment + applied suggestion); got %q%s", greenCount, diag())
	}
	if doneVisible != 0 {
		t.Errorf("done annotations should be collapsed in the md-view; %d visible%s", doneVisible, diag())
	}

	// Peek the green block → its collapsed done cards reveal.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('`+sgBlock+` .md-block-marks').click()`, nil),
		chromedp.Sleep(300*1e6),
	); err != nil {
		t.Fatalf("peek click: %v%s", err, diag())
	}
	var peeked int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`[...document.querySelectorAll('`+sgBlock+`.row-toggled .inline-suggestion.sg-accept, `+sgBlock+`.row-toggled .inline-comment.is-resolved')].filter(e=>e.offsetParent).length`, &peeked))
	if peeked < 1 {
		t.Errorf("peeking the green block should reveal its collapsed done cards%s", diag())
	}
}
