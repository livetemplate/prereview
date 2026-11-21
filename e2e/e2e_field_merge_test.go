//go:build browser

// End-to-end for persist's per-field merge: a tab that hasn't seen another
// tab's change can't undo it by saving. Two tabs on one review; the second
// still shows a comment open after the first resolved it, then saves.
//
// Run: go test -tags=browser -run 'TestE2E_StaleTabKeepsOthersResolve' ./e2e/...

package e2e

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

func TestE2E_StaleTabKeepsOthersResolve(t *testing.T) {
	repo := setupFixtureRepo(t)
	now := time.Now().UTC()
	seedReviewStore(t, repo,
		csv.Row{ID: "x", File: "edited.go", FromLine: 3, ToLine: 3, Side: "new", Body: "first", CreatedAt: now, Kind: "line"},
		csv.Row{ID: "y", File: "edited.go", FromLine: 4, ToLine: 4, Side: "new", Body: "second", CreatedAt: now, Kind: "line"})
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	card := func(id string) string { return `.inline-comment[data-key="` + id + `"]` }
	// The second tab opens before anything changes.
	tabB, closeB := chromedp.NewContext(p.ctx)
	defer closeB()
	if err := chromedp.Run(tabB, chromedp.Navigate(p.url), chromedp.WaitVisible(card("y"), chromedp.ByQuery)); err != nil {
		t.Fatalf("open the second tab: %v%s", err, diag())
	}
	// Tab A resolves x.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(card("x"), chromedp.ByQuery),
		chromedp.Click(card("x")+` button[name="toggleResolved"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("resolve x in tab A: %v%s", err, diag())
	}
	waitRows(t, p, func(rows []csv.Row) bool { return len(rows) == 2 && rows[0].Resolved }, "tab A's resolve of x wasn't saved", diag)

	// Tab B still shows x open; it resolves y, saving its whole copy.
	var xOpenInB bool
	if err := chromedp.Run(tabB, chromedp.Evaluate(`!document.querySelector('`+card("x")+`').classList.contains('is-resolved')`, &xOpenInB)); err != nil || !xOpenInB {
		t.Fatalf("tab B isn't stale (x open there: %v, %v)%s", xOpenInB, err, diag())
	}
	if err := chromedp.Run(tabB, chromedp.Click(card("y")+` button[name="toggleResolved"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("resolve y in tab B: %v%s", err, diag())
	}
	waitRows(t, p, func(rows []csv.Row) bool { return len(rows) == 2 && rows[1].Resolved }, "tab B's resolve of y wasn't saved", diag)
	rows, err := csv.Read(filepath.Join(p.store, "comments.csv"))
	if err != nil || len(rows) != 2 || !rows[0].Resolved {
		t.Errorf("tab B's save undid tab A's resolve of x: %+v (%v)%s", rows, err, diag())
	}
}
