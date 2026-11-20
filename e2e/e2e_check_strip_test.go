//go:build browser

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_CheckStripCollapsed: on a phone, the failing-checks bar is one summary
// line, not a wall of pills. A tap lists them; the list stays open when the
// checks change under it (a live update), and picking a pill still selects
// that check.
func TestE2E_CheckStripCollapsed(t *testing.T) {
	const w, h = 390, 844
	repo := setupFixtureRepoChecks(t, "notes.md")
	p := bootChromeAgainstRepo(t, repo, w, h)
	diag := captureDiag(t, p)
	p.waitReadyAt(w, h)
	ctx, cancel := within(p, 30*time.Second)
	defer cancel()

	if err := chromedp.Run(ctx, chromedp.WaitVisible(`.ck-strip-sum`, chromedp.ByQuery)); err != nil {
		t.Fatalf("no summary line: %v%s", err, diag())
	}
	if got := textOf(t, p, `.ck-strip-sum`); got != "1 failing · 1 check" {
		t.Errorf("summary = %q%s", got, diag())
	}
	if n := countSel(t, p, `.ck-strip-pick`); n != 0 {
		t.Errorf("a collapsed bar shows %d pills%s", n, diag())
	}
	var height float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('.ck-strip').getBoundingClientRect().height`, &height)); err != nil || height > 60 {
		t.Errorf("collapsed bar is %.0fpx tall (%v), want one line%s", height, err, diag())
	}
	// Its items sit together, not stretched across the row (Pico's
	// [role=group] > * { flex: 1 1 auto } once spread them out), at both widths.
	for _, vw := range []int64{390, 1600} {
		var gap float64
		if err := chromedp.Run(ctx, chromedp.EmulateViewport(vw, h),
			chromedp.Evaluate(`document.querySelector('.ck-strip-all').getBoundingClientRect().left - document.querySelector('.ck-strip-sum').getBoundingClientRect().right`, &gap)); err != nil || gap > 24 {
			t.Errorf("at %dpx, All checks sits %.0fpx after the summary (%v), want them together%s", vw, gap, err, diag())
		}
	}
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(w, h)); err != nil {
		t.Fatal(err)
	}
	// And the bar fits the phone's width (Pico's group width: 100% plus its
	// margins once pushed it 11px past the edge, where it was clipped).
	var right float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('.ck-strip').getBoundingClientRect().right`, &right)); err != nil || right > w {
		t.Errorf("the bar's right edge is at %.0fpx on a %dpx screen (%v)%s", right, w, err, diag())
	}

	if err := chromedp.Run(ctx, openCheckStrip()); err != nil {
		t.Fatalf("open the bar: %v%s", err, diag())
	}
	if n := countSel(t, p, `.ck-strip-pick`); n != 1 {
		t.Errorf("opened bar lists %d pills, want 1%s", n, diag())
	}

	// A second check starts failing while the list is open: it stays open.
	writeRule(t, repo, "house.yml", "extends: existence\nname: House\nmessage: Not Plain.\ntokens: [Plain]\n")
	if err := chromedp.Run(ctx, chromedp.Poll(`document.querySelectorAll('.ck-strip-pick').length === 2`, nil, chromedp.WithPollingTimeout(15*time.Second))); err != nil {
		t.Fatalf("the open list didn't pick up the new failing check: %v%s", err, diag())
	}
	if got := textOf(t, p, `.ck-strip-sum`); got != "2 failing · 2 checks" {
		t.Errorf("summary after the update = %q%s", got, diag())
	}

	clickStripPick(t, p, "House", diag)
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`.ck-strip:not(.is-idle)`, chromedp.ByQuery)); err != nil {
		t.Fatalf("picking a pill didn't select the check: %v%s", err, diag())
	}
	noHorizontalScroll(t, p, diag)
}
