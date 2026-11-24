//go:build browser

// End-to-end for a {{with}} inside a {{range}} (livetemplate v0.25.6). Range items share
// one statics array; livetemplate used to give a {{with}} no slot when its value was
// empty and an unwrapped one when set, so after a live update an item whose with
// differed from the first item's had its values shifted one slot right. On the Checks
// page that put a judge's description in its summary cell and squeezed its name to a
// letter per line. A full render was always right, so both tests reach the page by a
// live update.
//
// Run with: go test -tags=browser -run 'TestE2E_WithInRange' ./e2e/...

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_WithInRangeChecksRows(t *testing.T) {
	repo := setupFixtureRepoChecks(t, "notes.md")
	// One group (category), a checker with no description first, then a judge whose
	// description is its rubric's title.
	writeRule(t, repo, "house.yml", "extends: existence\nname: House\ncategory: Reader-first\nmessage: \"No '%s'.\"\ntokens: [Notes]\n")
	writeRule(t, repo, "reader-first.md", readerFirstJudge)
	p := bootChromeAgainstRepo(t, repo, 390, 844, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(390, 844)
	ctx, cancel := within(p, 30*time.Second)
	defer cancel()

	judge := pageRow("reader-first")
	if err := chromedp.Run(ctx,
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.Click(checksTab, chromedp.ByQuery),
		chromedp.WaitVisible(judge, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open Library → Checks: %v%s", err, diag())
	}

	// Each cell holds its own value: the description sits under the name, the summary
	// stays short, and the source is in its own cell.
	cell := func(sel string) string { return strings.TrimSpace(textOf(t, p, judge+" "+sel)) }
	if got := cell(".ckp-desc"); got != "Does the text lead with what the reader must do?" {
		t.Errorf("judge .ckp-desc = %q, want its rubric title%s", got, diag())
	}
	if got := cell(".ckp-sum"); strings.Contains(got, "reader must do") || got == "" {
		t.Errorf("judge .ckp-sum = %q: the description shifted into the summary cell%s", got, diag())
	}
	if got := cell(".ckp-src"); got != "yours" {
		t.Errorf("judge .ckp-src = %q, want \"yours\"%s", got, diag())
	}
	var nameW float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('`+judge+` .ckp-title').getBoundingClientRect().width`, &nameW)); err != nil || nameW < 100 {
		t.Errorf("judge title column is %.0fpx wide (%v): the name is squeezed to a letter per line%s", nameW, err, diag())
	}
}

func TestE2E_WithInRangeRegionTooltips(t *testing.T) {
	const pins = `.pin-layer .area-overlay[data-id]`
	app := newLiveApp(t)
	p := bootChromeExternal(t, app.srv.URL, 1200, 900, "--agent")
	diag := captureDiag(t, p)
	ctx, cancel := within(p, 30*time.Second)
	defer cancel()

	annotateRegion(t, p, "first box", diag)
	annotateRegion(t, p, "second box", diag)
	var ids []string
	// annotateRegion returns once a pin is visible, and the first one already is: wait
	// for the second before reading them.
	if err := chromedp.Run(ctx,
		chromedp.Poll(`document.querySelectorAll('`+pins+`').length === 2`, nil),
		chromedp.Evaluate(`[...document.querySelectorAll('`+pins+`')].map(b => b.dataset.id)`, &ids),
	); err != nil || len(ids) != 2 {
		t.Fatalf("want two pins, got %v (%v)%s", ids, err, diag())
	}
	// Act on the second only: the first item's tooltip has no label, the second's does.
	if _, stderr, code := runCLI(t, 10*time.Second, p.binary, "done", "--out", p.store, ids[1]); code != 0 {
		t.Fatalf("prereview done: exit %d\n%s", code, stderr)
	}
	second := `.pin-layer .area-overlay[data-id="` + ids[1] + `"]`
	var t1, t2 string
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(second+`.is-acted`, chromedp.ByQuery),
		chromedp.AttributeValue(`.pin-layer .area-overlay[data-id="`+ids[0]+`"] .pin-btn`, "title", &t1, nil, chromedp.ByQuery),
		chromedp.AttributeValue(second+` .pin-btn`, "title", &t2, nil, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the acted-on box never updated: %v%s", err, diag())
	}
	if t1 != "first box" || t2 != "Worked on — second box" {
		t.Errorf("tooltips = %q / %q, want \"first box\" / \"Worked on — second box\"%s", t1, t2, diag())
	}
}
