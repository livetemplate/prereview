//go:build browser

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

// longDocRepo is a repo whose one changed file is a long Markdown document, so
// annotations sit screens apart.
func longDocRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "guide.md", "# Guide\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	var b strings.Builder
	b.WriteString("# Guide\n\n")
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&b, "Paragraph %d says something long enough to wrap onto a second line on a phone screen.\n\n", i)
	}
	mustWrite(t, dir, "guide.md", b.String())
	return dir
}

// cardOnScreen reports whether the comment card id is laid out below the sticky
// bars at the top and above the bottom of the viewport.
func cardOnScreen(id string) string {
	return `(()=>{const c=document.querySelector('.inline-comment[data-key="` + id + `"]'); if(!c||c.offsetParent===null) return 'no card';
		const r=c.getBoundingClientRect(); let top=0;
		for (const s of document.querySelectorAll('header.bar, header.file-head, .ann-nav')) { const q=s.getBoundingClientRect(); if (q.top<=1 && q.bottom>top) top=q.bottom; }
		return (r.top>=top-1 && r.top<innerHeight-20)?'ok':'card top '+Math.round(r.top)+', sticky bars end '+Math.round(top)+', viewport '+innerHeight})()`
}

// TestE2E_NavScrollsToItem: on a phone, ‹ and › scroll the card they step to
// into view below the sticky bars, including the only open item after the
// reviewer scrolled away from it.
func TestE2E_NavScrollsToItem(t *testing.T) {
	dir := longDocRepo(t)
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	// Paragraph n is on line 2n+1.
	seedReviewStore(t, dir,
		csv.Row{ID: "early", File: "guide.md", FromLine: 21, ToLine: 21, Side: "new", Body: "EARLY note", Kind: "line", CreatedAt: at},
		csv.Row{ID: "late", File: "guide.md", FromLine: 141, ToLine: 141, Side: "new", Body: "LATE note", Kind: "line", CreatedAt: at.Add(time.Minute)})
	p := bootChromeAgainstRepo(t, dir, 390, 844, "--agent")
	diag := captureDiag(t, p)
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(390, 844),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.ann-nav button[name="navNext"]`, chromedp.ByQuery),
		chromedp.Sleep(time.Second), // the client attaches its listeners
	); err != nil {
		t.Fatalf("open guide.md: %v%s", err, diag())
	}
	scrollTop := func() {
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('main.viewer').scrollTo(0,0), window.scrollTo(0,0), true`, nil), chromedp.Sleep(300*time.Millisecond))
	}
	waitOnScreen := func(id, why string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		got := ""
		for time.Now().Before(deadline) {
			if got = evalStr(t, p, cardOnScreen(id)); got == "ok" {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Errorf("%s: %s%s", why, got, diag())
	}

	navStep(t, p, "navNext", diag)
	waitOnScreen("early", "› to the first comment")
	navStep(t, p, "navNext", diag)
	waitOnScreen("late", "› to the far comment, screens below")
	navStep(t, p, "navPrev", diag)
	waitOnScreen("early", "‹ back up to the first comment")

	// Stepping to the same item again after scrolling away must scroll back.
	navStep(t, p, "navNext", diag)
	waitOnScreen("late", "› to the far comment again")
	scrollTop()
	navStep(t, p, "navNext", diag) // wraps to early, which is near the top
	waitOnScreen("early", "› wraps to the first comment")
	navStep(t, p, "navNext", diag)
	waitOnScreen("late", "› to the far comment after scrolling to the top")

	// One open item left: every step lands on the same card, so the scroll must
	// fire even though the target did not change.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('.inline-comment[data-key="early"] button[name="toggleResolved"]').click(), true`, nil)); err != nil {
		t.Fatalf("resolve the first comment: %v%s", err, diag())
	}
	waitNavLabel(t, p, "1 / 1 open", "one open item left", diag)
	scrollTop()
	navStep(t, p, "navNext", diag)
	waitOnScreen("late", "› to the only open comment")
	scrollTop()
	navStep(t, p, "navNext", diag)
	waitOnScreen("late", "› to the only open comment again, after scrolling away")
	scrollTop()
	navStep(t, p, "navPrev", diag)
	waitOnScreen("late", "‹ to the only open comment, after scrolling away")
}
