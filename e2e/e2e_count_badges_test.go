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
	"github.com/chromedp/chromedp/device"
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
	// badgeText reads the row's ONE unified badge count (#165: total comments + suggestions).
	badgeText := func(line int) string {
		var s string
		js := fmt.Sprintf(`(document.querySelector('%s .line-mark')?.textContent||"").trim()`, rowSel(line))
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

	// --- The load-bearing assertion: the ONE unified badge's number equals the total
	//     cards rendered on that row (#165). Line 3 = 2 comments; line 4 = 1 comment + 1
	//     suggestion = 2. ---
	if got := badgeText(3); got != "2" {
		t.Errorf("line 3 badge = %q, want \"2\"%s", got, diag())
	}
	if got := visibleCount(3, ".inline-comment"); got != 2 {
		t.Errorf("line 3 shows %d comment cards, badge claims 2%s", got, diag())
	}
	if got := badgeText(4); got != "2" {
		t.Errorf("line 4 badge = %q, want \"2\" (1 comment + 1 suggestion)%s", got, diag())
	}
	if got := visibleCount(4, ".inline-comment"); got != 1 {
		t.Errorf("line 4 shows %d comment cards%s", got, diag())
	}
	if got := visibleCount(4, ".inline-suggestion"); got != 1 {
		t.Errorf("line 4 shows %d suggestion cards%s", got, diag())
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

	// (#165 retired the manual per-line collapse: OPEN cards always show; the badge now
	// peeks DONE cards — covered by TestE2E_AnnotationBadges. Open comments stay visible.)
	if got := visibleCount(3, ".inline-comment"); got != 2 {
		t.Errorf("open comment cards should stay visible; got %d%s", got, diag())
	}

	// --- Coarse pointer / phone: on a real touch device the badge is the primary tap
	//     affordance, so the `@media (pointer: coarse)` rule grows it to ~44px. Device
	//     emulation (touch) is what actually flips `pointer: coarse` — SetEmulatedMedia
	//     does NOT override `pointer` (see e2e_kbdhint_test.go). ---
	if err := chromedp.Run(p.ctx, chromedp.Emulate(device.IPhone11), chromedp.Sleep(250*time.Millisecond)); err != nil {
		t.Fatalf("emulate phone (touch): %v", err)
	}
	// Guard the guard: the media query must actually be active, or the ≥44 assertion
	// below would pass vacuously on the base 24px badge.
	if intEval(`matchMedia('(pointer: coarse)').matches ? 1 : 0`) != 1 {
		t.Fatalf("(pointer: coarse) not active under device emulation — the tap-target assertion would be vacuous%s", diag())
	}
	if w, h := rectDim(3, 0), rectDim(3, 1); w < 44 || h < 44 {
		t.Errorf("phone tap target = %dx%dpx, want ≥44 in both axes (coarse-pointer enlarge)%s", w, h, diag())
	}
	// The enlarged badge must NOT sit over the code: its left edge stays within the
	// gutter that `.content`'s padding-right reserves, so text is never occluded.
	noOverlap := intEval(fmt.Sprintf(`(() => {
		const row = document.querySelector('%s');
		const marks = row.querySelector('.line-marks').getBoundingClientRect();
		const content = row.querySelector('.content');
		const cr = content.getBoundingClientRect();
		const textRight = cr.right - parseFloat(getComputedStyle(content).paddingRight);
		return marks.left >= textRight - 1 ? 1 : 0;
	})()`, rowSel(3)))
	if noOverlap != 1 {
		t.Errorf("the enlarged badge overlaps the code text (padding-right reserve too small)%s", diag())
	}

	// --- "Hide annotations" menu toggle removes ALL badges + inline cards. ---
	if err := chromedp.Run(p.ctx, chromedp.Emulate(device.Reset), chromedp.EmulateViewport(1200, 800), chromedp.Sleep(150*time.Millisecond)); err != nil {
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
