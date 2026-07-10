//go:build browser

// End-to-end for #151: per-line comment/suggestion COUNT badges. A line carrying
// annotations shows a tap-sized right-margin badge per kind with the count; the
// count equals the cards actually rendered on that row; the badges are hide-able
// via the overflow menu ("Hide annotations") and in focus mode, and stay clickable
// on a phone-sized (coarse-pointer) viewport.
//
// Run with: go test -tags=browser -run TestE2E_CountBadges ./e2e/...

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestE2E_CountBadges(t *testing.T) {
	// --agent so the server runs WatchLLMStatus — the live suggestion-push path.
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")

	// Console errors would flag a badge render / CSS bug — collect and assert none.
	var consoleErrs []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok && e.Type == "error" {
			consoleErrs = append(consoleErrs, joinArgs(e.Args))
		}
	})

	p.waitReady()
	p.clickFile("app.go")

	shot := func() string {
		var buf []byte
		if chromedp.Run(p.ctx, chromedp.FullScreenshot(&buf, 90)) != nil {
			return ""
		}
		path := filepath.Join(t.TempDir(), "count-badges.png")
		_ = os.WriteFile(path, buf, 0o644)
		return path
	}
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- console errors ---\n" + strings.Join(consoleErrs, "\n") +
			"\n--- server ---\n" + p.stderr.String() +
			"\n--- screenshot ---\n" + shot() +
			"\n--- html ---\n" + html
	}
	// rowSel targets the .line-row that owns a given new-side diff line.
	rowSel := func(line int) string {
		return fmt.Sprintf(`.line-row:has(.line[data-line="%d"][data-side="new"])`, line)
	}
	// intEval reads an integer from the page (−1 on a missing element).
	intEval := func(js string) int {
		var n int
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &n)); err != nil {
			t.Fatalf("eval %q: %v%s", js, err, diag())
		}
		return n
	}
	// badgeText reads a badge's rendered count for the row owning `line`.
	badgeText := func(line int, kind string) string {
		var s string
		js := fmt.Sprintf(`(document.querySelector('%s .line-mark-%s')?.textContent||"").trim()`, rowSel(line), kind)
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &s)); err != nil {
			t.Fatalf("badgeText: %v%s", err, diag())
		}
		return s
	}
	// visibleCount counts elements matching sel that are actually laid out
	// (offsetParent != null), so it reflects what the reviewer sees.
	visibleCount := func(line int, sel string) int {
		return intEval(fmt.Sprintf(
			`[...document.querySelectorAll('%s %s')].filter(el => el.offsetParent !== null).length`,
			rowSel(line), sel))
	}

	// --- Build the annotation set: a suggestion + a comment on line 4, two
	//     comments on line 3. ---
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s4","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""}
	]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(rowSel(4)+` .inline-suggestion`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("suggestion never rendered on line 4: %v%s", err, diag())
	}

	addComment := func(line int, body string) {
		p.clickLine(0, line)
		if err := chromedp.Run(p.ctx,
			chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
			chromedp.SendKeys(`.composer textarea`, body, chromedp.ByQuery),
			chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
			chromedp.Sleep(250*time.Millisecond),
		); err != nil {
			t.Fatalf("add comment on line %d: %v%s", line, err, diag())
		}
	}
	addComment(4, "comment coexisting with a suggestion")
	addComment(3, "first comment on line 3")
	addComment(3, "second comment on line 3")

	// --- The load-bearing assertion: a badge's number equals the cards rendered
	//     on that row (derived from the same filtered maps). ---
	if got := badgeText(3, "comment"); got != "2" {
		t.Errorf("line 3 comment badge = %q, want \"2\"%s", got, diag())
	}
	if got := visibleCount(3, ".inline-comment"); got != 2 {
		t.Errorf("line 3 shows %d comment cards, badge claims 2%s", got, diag())
	}
	if got := badgeText(4, "comment"); got != "1" {
		t.Errorf("line 4 comment badge = %q, want \"1\"%s", got, diag())
	}
	if got := badgeText(4, "suggestion"); got != "1" {
		t.Errorf("line 4 suggestion badge = %q, want \"1\"%s", got, diag())
	}
	if got := visibleCount(4, ".inline-comment"); got != 1 {
		t.Errorf("line 4 shows %d comment cards, badge claims 1%s", got, diag())
	}
	if got := visibleCount(4, ".inline-suggestion"); got != 1 {
		t.Errorf("line 4 shows %d suggestion cards, badge claims 1%s", got, diag())
	}

	// --- Tap target: the badge button is ≥24px in both axes (touch-friendly). ---
	rectDim := func(line, axis int) int {
		fn := "width"
		if axis == 1 {
			fn = "height"
		}
		return intEval(fmt.Sprintf(
			`Math.round(document.querySelector('%s .line-marks').getBoundingClientRect().%s)`,
			rowSel(3), fn))
	}
	if w := rectDim(3, 0); w < 24 {
		t.Errorf("line-marks tap width = %dpx, want ≥24%s", w, diag())
	}
	if h := rectDim(3, 1); h < 24 {
		t.Errorf("line-marks tap height = %dpx, want ≥24%s", h, diag())
	}

	// --- Clicking the badge collapses the row's cards, clicking again expands.
	//     Done at a stable viewport (no mid-toggle re-render, which would strip the
	//     client-only .cards-collapsed class). ---
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector('%s .line-marks').click()`, rowSel(3)), nil),
		chromedp.Sleep(200*time.Millisecond),
	); err != nil {
		t.Fatalf("click badge to collapse: %v%s", err, diag())
	}
	if got := visibleCount(3, ".inline-comment"); got != 0 {
		t.Errorf("after clicking the badge, %d comment cards still visible (want 0/collapsed)%s", got, diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector('%s .line-marks').click()`, rowSel(3)), nil),
		chromedp.Sleep(200*time.Millisecond),
	); err != nil {
		t.Fatalf("click badge to re-expand: %v%s", err, diag())
	}
	if got := visibleCount(3, ".inline-comment"); got != 2 {
		t.Errorf("after re-clicking, %d comment cards visible (want 2)%s", got, diag())
	}

	// --- Coarse pointer / phone: the tap target stays ≥24px at a phone width so a
	//     finger can hit it. ---
	if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(390, 780), chromedp.Sleep(200*time.Millisecond)); err != nil {
		t.Fatalf("emulate phone viewport: %v", err)
	}
	if w, h := rectDim(3, 0), rectDim(3, 1); w < 24 || h < 24 {
		t.Errorf("phone tap target = %dx%dpx, want ≥24 in both axes%s", w, h, diag())
	}

	// --- "Hide annotations" menu toggle removes ALL badges + inline cards. ---
	if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(1200, 800), chromedp.Sleep(150*time.Millisecond)); err != nil {
		t.Fatalf("restore desktop viewport: %v", err)
	}
	p.openViewItem("toggleMarks")
	if err := chromedp.Run(p.ctx, chromedp.Sleep(200*time.Millisecond)); err != nil {
		t.Fatalf("settle after hide: %v", err)
	}
	if n := intEval(`[...document.querySelectorAll('.code .line-marks')].filter(el => el.offsetParent !== null).length`); n != 0 {
		t.Errorf("Hide annotations left %d badges visible%s", n, diag())
	}
	if n := intEval(`[...document.querySelectorAll('.code .inline-comment')].filter(el => el.offsetParent !== null).length`); n != 0 {
		t.Errorf("Hide annotations left %d inline comments visible%s", n, diag())
	}
	// Toggling back restores them.
	p.openViewItem("toggleMarks")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.code .line-marks`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Show annotations did not restore the badges: %v%s", err, diag())
	}

	// --- Focus mode hides the badges too — and the rule reaches the PHONE (it is
	//     NOT trapped in the desktop-only focus-mode media query). ---
	p.openViewItem("toggleFocusMode")
	if err := chromedp.Run(p.ctx, chromedp.Sleep(200*time.Millisecond)); err != nil {
		t.Fatalf("settle after focus: %v", err)
	}
	if n := intEval(`[...document.querySelectorAll('.code .line-marks')].filter(el => el.offsetParent !== null).length`); n != 0 {
		t.Errorf("focus mode left %d badges visible on desktop%s", n, diag())
	}
	if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(390, 780), chromedp.Sleep(200*time.Millisecond)); err != nil {
		t.Fatalf("emulate phone in focus mode: %v", err)
	}
	if n := intEval(`[...document.querySelectorAll('.code .line-marks')].filter(el => el.offsetParent !== null).length`); n != 0 {
		t.Errorf("focus mode left %d badges visible on the phone (rule stuck in a desktop media query?)%s", n, diag())
	}

	if len(consoleErrs) != 0 {
		t.Errorf("browser console errors:\n%s", strings.Join(consoleErrs, "\n"))
	}
}
