//go:build browser

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// setupFixtureRepoLongLineDeep builds a diff long enough that
// `content-visibility: auto` actually skips off-screen rows, with the single
// very long line placed far below the fold. That is the configuration where
// sizing .line-row to max-content could in principle make `.code`'s
// scrollWidth depend on which rows happen to be rendered.
func setupFixtureRepoLongLineDeep(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	build := func(withLong bool) string {
		var b strings.Builder
		b.WriteString("package deep\n\nfunc run() {\n")
		for i := 1; i <= 600; i++ {
			if i == 500 && withLong {
				b.WriteString(longCodeLine + "\n")
				continue
			}
			fmt.Fprintf(&b, "\tstep%d()\n", i)
		}
		b.WriteString("}\n")
		return b.String()
	}
	mustWrite(t, dir, "deep.go", build(false))
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed deep")
	mustWrite(t, dir, "deep.go", build(true))
	return dir
}

// TestE2E_LongLineScrollWidthStable guards the tradeoff made by sizing
// .line-row to max-content on desktop (see the @media (min-width:900px)
// .line-row rule): `content-visibility: auto` still skips off-screen rows, so
// this pins that `.code`'s horizontal scroll range does NOT fluctuate as rows
// virtualize in and out — a jittering scrollbar would be a visible regression.
func TestE2E_LongLineScrollWidthStable(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoLongLineDeep(t), 1200, 800)

	readScrollW := `(() => { const c = document.querySelector('.code'); return c.scrollWidth; })()`

	var atTop, atBottom, backAtTop float64
	if err := chromedp.Run(p.ctx,
		chromedp.EmulateViewport(1200, 800),
		chromedp.Navigate(p.url),
		chromedp.Click(`//button[contains(., 'deep.go')]`, chromedp.BySearch),
		chromedp.WaitVisible(`.code .line`, chromedp.ByQuery),
		chromedp.Evaluate(readScrollW, &atTop),
		// Scroll the long line into view so its row is definitely rendered.
		chromedp.Evaluate(`(() => {
			const el = [...document.querySelectorAll('.code .content')]
				.find(c => c.textContent.includes('doSomethingWithAVeryLongName'));
			el.scrollIntoView({block: 'center'});
			return true;
		})()`, nil),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(readScrollW, &atBottom),
		chromedp.Evaluate(`(() => { window.scrollTo(0,0); document.querySelector('.code').scrollTop = 0; return true; })()`, nil),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(readScrollW, &backAtTop),
	); err != nil {
		t.Fatalf("scroll-stability run: %v\nserver stderr: %s", err, p.stderr.String())
	}

	t.Logf("scrollWidth: atTop=%.0f afterReachingLongLine=%.0f backAtTop=%.0f", atTop, atBottom, backAtTop)

	if atBottom <= 1200 {
		t.Fatalf("the long line never extended the scroll range (%.0f) — fixture or fix is not exercising the path", atBottom)
	}
	// Once the row has been laid out, the range must not collapse again when it
	// scrolls back out of view (content-visibility's `auto` remembers the size).
	if backAtTop < atBottom {
		t.Errorf("scrollWidth COLLAPSED after the long row scrolled out of view: %.0f -> %.0f;\n"+
			"the horizontal scrollbar would jitter while scrolling vertically", atBottom, backAtTop)
	}
}
