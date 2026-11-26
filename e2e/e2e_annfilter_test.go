//go:build browser

// End-to-end for the per-file Annotations filter: each file keeps its own
// setting, a reload keeps it, and a filter that leaves nothing open still leaves
// its own control on screen.
//
// Run with: go test -tags=browser -run TestE2E_AnnFilterPerFile ./e2e/...

package e2e

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// annFilterLabel is the filter trigger's text, on the bar or its chip.
func annFilterLabel(t *testing.T, p *runningPrereview) string {
	t.Helper()
	return evalStr(t, p, `((document.querySelector('.ann-filter-trigger')||{}).textContent||"").trim()`)
}

func waitAnnFilterLabel(t *testing.T, p *runningPrereview, want, why string, diag func() string) {
	t.Helper()
	ctx, cancel := within(p, 10*time.Second)
	defer cancel()
	js := `((document.querySelector('.ann-filter-trigger')||{}).textContent||"").trim() === "` + want + `"`
	if err := chromedp.Run(ctx, chromedp.Poll(js, nil, chromedp.WithPollingInterval(50*time.Millisecond))); err != nil {
		t.Fatalf("%s: filter reads %q, want %q%s", why, annFilterLabel(t, p), want, diag())
	}
}

func TestE2E_AnnFilterPerFile(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1280, 900, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(1280, 900)
	p.clickFile("app.go")

	// app.go: a comment and a suggestion. folded.go: a suggestion.
	addLineComment(t, p, 0, 3, "rename Greet")
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"near","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""},
	  {"id":"far","file":"folded.go","from_line":11,"to_line":11,"original":"func keepMe() {}","proposed":"func keepMe() { _ = 1 }"}
	]`)
	waitNavLabel(t, p, "3 open", "three open items across two files", diag)
	waitAnnFilterLabel(t, p, "Annotations: Open", "a file nobody has touched starts at Open", diag)

	// None on app.go: its badges and cards go, and only folded.go's stop is left.
	pickAnnFilter(t, p, "setAnnShow", "none", diag)
	waitAnnFilterLabel(t, p, "Annotations: None", "app.go set to None", diag)
	waitNavLabel(t, p, "1 open", "app.go's items stop being stops", diag)
	if evalInt(t, p, `document.querySelectorAll(".layout.marks-hidden").length`) == 0 {
		t.Errorf("None should hide app.go's badges and cards (marks-hidden)%s", diag())
	}

	// folded.go keeps its own default.
	p.clickFile("folded.go")
	waitAnnFilterLabel(t, p, "Annotations: Open", "folded.go keeps its own setting", diag)
	if evalInt(t, p, `document.querySelectorAll(".layout.marks-hidden").length`) != 0 {
		t.Errorf("app.go's None leaked onto folded.go%s", diag())
	}
	p.clickFile("app.go")
	waitAnnFilterLabel(t, p, "Annotations: None", "app.go remembers None", diag)

	// None on both files leaves nothing open. After a reload the bar has no
	// open items and wasn't seen this visit, so it collapses to its chip, and
	// the chip still carries the filter.
	p.clickFile("folded.go")
	pickAnnFilter(t, p, "setAnnShow", "none", diag)
	waitNavLabel(t, p, "0 open", "nothing open anywhere", diag)
	ctx, cancel := within(p, 15*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reload: %v%s", err, diag())
	}
	p.clickFile("app.go")
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`.ann-nav.is-collapsed .ann-filter-trigger`, chromedp.ByQuery)); err != nil {
		t.Fatalf("with nothing open the filter must stay reachable as a chip: %v%s", err, diag())
	}
	waitAnnFilterLabel(t, p, "Annotations: None", "the setting survives a reload", diag)
	if n := evalInt(t, p, `document.querySelectorAll('.ann-nav button[name="expandNav"]').length`); n != 0 {
		t.Errorf("the chip shown because nothing is open has nothing to expand, got %d%s", n, diag())
	}
}
