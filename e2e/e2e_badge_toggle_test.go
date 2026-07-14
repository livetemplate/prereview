//go:build browser

// End-to-end for #174 (and #173, which it subsumes).
//
// The count badge is a real show/hide toggle:
//   - YELLOW (open) badge — the card is visible; clicking COLLAPSES it, so an unresolved
//     comment can be set aside and returned to later. This did NOTHING before: the badge
//     toggled a client-only class that no CSS rule matched.
//   - GREEN (done) badge — the card is collapsed; clicking REVEALS it (unchanged).
//
// And the collapse SURVIVES A RE-RENDER, which is #173: the class used to be client-only,
// so the server's next render morphed it away and silently re-opened (or re-collapsed) the
// card the reviewer had just toggled.
//
// Run: go test -tags=browser -run TestE2E_BadgeToggle ./e2e/...

package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_BadgeTogglesOpenAnnotation(t *testing.T) {
	repo := setupFixtureRepo(t)
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// One OPEN (unresolved) comment on line 3 — the case that had no way to be hidden.
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,3,3,new,come back to this later,2026-07-13T12:00:00Z,false,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	p := bootChromeAgainstRepo(t, repo, 1400, 1000)
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("edited.go")

	row := `.line-row:has(.line[data-line="3"][data-side="new"])`

	// The open comment is visible, and its badge is YELLOW (is-open).
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(row+` .inline-comment`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .line-mark.is-open`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("an open comment should render inline with a yellow badge: %v%s", err, diag())
	}

	// Click the YELLOW badge → the card COLLAPSES. This is the bug: it used to do nothing.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(row+` .line-marks`, chromedp.ByQuery),
		chromedp.WaitNotVisible(row+` .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("clicking the yellow badge must COLLAPSE the open comment — the whole point "+
			"of #174 (before, it toggled a class no rule matched): %v%s", err, diag())
	}
	// The badge itself stays — it is the way back. A card with no way back is a dismissal,
	// not a collapse.
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(row+` .line-marks`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the badge must remain after collapsing — it is the way back: %v%s", err, diag())
	}

	// #173: the collapse must SURVIVE A SERVER RE-RENDER. Trigger one that has nothing to do
	// with this row — selecting a different line — then assert the card is still collapsed.
	// With the old client-only class, the morph stripped it here and the card silently
	// re-appeared: you hid something and it came back on its own.
	p.clickLine(1, 1)
	// Wait for THAT render to actually land, via a condition that is false before it and
	// true after: the composer opening on line 1. `row-toggled` cannot serve — it is
	// already on the row from the collapse above, so waiting on it passes instantly against
	// the stale DOM, the #173 assertion below proves nothing (the re-render it claims to
	// survive hasn't happened yet), and the composer's insertion then shifts row 3 down
	// mid-flight so the coordinate-based badge click further down misses it.
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(
		`.line-row:has(.line[data-line="1"][data-side="new"]) .composer textarea`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the unrelated re-render never landed, so there is nothing to survive: %v%s", err, diag())
	}
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(row+`.row-toggled`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the collapse must survive an unrelated server re-render (#173) — a client-only "+
			"class gets morphed away: %v%s", err, diag())
	}
	var shown int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`[...document.querySelectorAll('`+row+` .inline-comment')].filter(e=>e.offsetParent).length`, &shown))
	if shown != 0 {
		t.Errorf("the comment re-appeared after a re-render — the toggle is not server state%s", diag())
	}

	// Click again → it comes back. "Come back to it later" has to actually work.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(row+` .line-marks`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("clicking the badge again must bring the comment back: %v%s", err, diag())
	}

	// The comment is still OPEN and still the agent's work — hiding it is a VIEW action, it
	// must never resolve anything or change what the agent is handed.
	rows := p.readCSV()
	for _, r := range rows[1:] {
		if r[0] == "c1" && r[7] != "false" {
			t.Errorf("collapsing a comment must not RESOLVE it (resolved=%q) — it is a view "+
				"action; the work is still open", r[7])
		}
	}
}

// The GREEN (done) badge keeps its existing behaviour: the card is collapsed, and clicking
// reveals it. #174 must not regress #165's ladder.
func TestE2E_BadgeTogglePeeksDoneAnnotation(t *testing.T) {
	repo := setupFixtureRepo(t)
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,3,3,new,already handled,2026-07-13T12:00:00Z,true,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	p := bootChromeAgainstRepo(t, repo, 1400, 1000)
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("edited.go")

	row := `.line-row:has(.line[data-line="3"][data-side="new"])`

	// A resolved comment starts COLLAPSED behind a green badge (#165).
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(row+` .line-mark.is-done`, chromedp.ByQuery),
		chromedp.WaitNotVisible(row+` .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a resolved comment should start collapsed behind a green badge: %v%s", err, diag())
	}

	// Clicking reveals it — the existing peek, now server-backed.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(row+` .line-marks`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("clicking the green badge must still reveal the done card (#165 unchanged): %v%s",
			err, diag())
	}
}
