//go:build browser

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// setupTallNewFile: a repo whose working tree adds a 60-line untracked file, so
// prereview renders every line (no fold) → a genuinely scrollable page.
func setupTallNewFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "t@t.co")
	runCmd(t, dir, "git", "config", "user.name", "t")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "seed.txt", "seed\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	var b strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	mustWrite(t, dir, "tall.txt", b.String())
	return dir
}

// TestE2E_ResolveDoesNotScroll reproduces "resolving a comment jumps the page":
// navigate to a TOP comment (so ScrollToCommentID points there — the stale
// target), scroll to the BOTTOM comment, resolve it, and assert the viewport
// didn't jump back up to the top. Prints the scroll delta + activeElement so the
// mechanism is visible even if the assertion is loose.
func TestE2E_ResolveDoesNotScroll(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupTallNewFile(t), 1000, 700)
	p.waitReadyAt(1000, 700)
	p.clickFile("tall.txt")

	addLineComment(t, p, 0, 3, "top comment")
	addLineComment(t, p, 0, 55, "bottom comment")

	// Navigate to the TOP comment → ScrollToCommentID = the top one (stale target).
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent("p"), chromedp.Sleep(400*time.Millisecond)); err != nil {
		t.Fatalf("prevComment: %v", err)
	}

	// Scroll to the bottom comment.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(()=>{const c=[...document.querySelectorAll('.inline-comment')].find(e=>e.textContent.includes('bottom comment'));c&&c.scrollIntoView({block:'center'});})()`, nil),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("scroll to bottom: %v", err)
	}

	// The scroll container is main.viewer (overflow-y:auto), not window.
	scrollY := func() float64 {
		var y float64
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('main.viewer')||{}).scrollTop||0`, &y))
		return y
	}
	before := scrollY()

	// Resolve the BOTTOM comment (a different comment than ScrollToCommentID).
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`(()=>{const c=[...document.querySelectorAll('.inline-comment')].find(e=>e.textContent.includes('bottom comment'));const b=c&&c.querySelector('button[name="toggleResolved"]');if(b){b.click();return true}return false})()`, nil),
		chromedp.Sleep(600*time.Millisecond),
	); err != nil {
		t.Fatalf("resolve bottom: %v", err)
	}
	after := scrollY()

	var active string
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`(()=>{const a=document.activeElement;if(!a)return'none';return a.tagName+'.'+(a.className||'')+(a.closest('.inline-comment')?' [in-comment:'+(a.closest('.inline-comment').textContent.slice(0,20))+']':'')})()`, &active))

	t.Logf("scrollY before=%.0f after=%.0f delta=%.0f | activeElement=%s", before, after, after-before, active)

	// The bug jumps UP toward the top comment (large negative delta). Allow a small
	// reflow tolerance (the resolved card's own height leaving the layout).
	if before-after > 150 {
		t.Errorf("resolving the bottom comment jumped the page UP by %.0fpx (toward the stale top target) — should stay put", before-after)
	}
}
