//go:build browser

// End-to-end coverage for issue #88 item 2: individually re-hiding a resolved
// comment. With "Show resolved" ON, a reviewer can hide ONE resolved comment
// (it drops out of the diff) without turning the whole resolved group back off;
// the hidden state is durable in comments.csv (survives a reload), and an
// "Unhide" affordance in the View menu brings them all back.
//
// Run with: go test -tags=browser -run TestE2E_HideResolved ./e2e/...

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_HideResolvedComment(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")

	// addComment seeds one comment on the given diff line.
	addComment := func(oldNum, newNum int, body string) {
		p.clickLine(oldNum, newNum)
		if err := chromedp.Run(p.ctx,
			chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
			chromedp.SendKeys(`.composer textarea`, body, chromedp.ByQuery),
			chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
			chromedp.Sleep(250*time.Millisecond),
		); err != nil {
			t.Fatalf("add comment %q: %v\nstderr: %s", body, err, p.stderr.String())
		}
	}
	countInline := func() int {
		var n int
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`document.querySelectorAll('.inline-comment').length`, &n)); err != nil {
			t.Fatalf("count inline: %v", err)
		}
		return n
	}
	firstCardBody := func() string {
		var s string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`(document.querySelector('.inline-comment .body')||{textContent:""}).textContent`, &s)); err != nil {
			t.Fatalf("read first card body: %v", err)
		}
		return strings.TrimSpace(s)
	}

	// Two comments, both then resolved (resolving hides them by default).
	addComment(3, 3, "first-note")
	addComment(0, 4, "second-note")
	for range []int{0, 1} {
		if err := chromedp.Run(p.ctx,
			chromedp.WaitVisible(`.inline-comment button[name='toggleResolved']`, chromedp.ByQuery),
			chromedp.Click(`.inline-comment button[name='toggleResolved']`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		); err != nil {
			t.Fatalf("resolve a comment: %v\nstderr: %s", err, p.stderr.String())
		}
	}
	if n := countInline(); n != 0 {
		t.Fatalf("both resolved should be hidden by default; %d still shown", n)
	}

	// Show resolved → both reappear (as .is-resolved), each with a Hide button.
	p.openViewItem("toggleShowResolved")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-comment.is-resolved button[name='hideComment']`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("show resolved: %v\nstderr: %s", err, p.stderr.String())
	}
	if n := countInline(); n != 2 {
		t.Fatalf("show resolved: want 2 resolved cards, got %d", n)
	}

	// Hide the FIRST card only. It drops out; the other stays.
	hiddenBody := firstCardBody()
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-comment.is-resolved button[name='hideComment']`, chromedp.ByQuery),
		chromedp.Sleep(350*time.Millisecond),
	); err != nil {
		t.Fatalf("hide first: %v\nstderr: %s", err, p.stderr.String())
	}
	if n := countInline(); n != 1 {
		t.Fatalf("after hiding one, want 1 card, got %d", n)
	}
	if got := firstCardBody(); got == hiddenBody {
		t.Fatalf("hidden card %q is still the one showing", hiddenBody)
	}
	// CSV: exactly one row carries hidden=true (col 16 / index 15).
	countHidden := func() int {
		n := 0
		for _, r := range p.readCSV()[1:] {
			if len(r) >= 16 && r[15] == "true" {
				n++
			}
		}
		return n
	}
	if h := countHidden(); h != 1 {
		t.Fatalf("CSV should have 1 hidden row, got %d", h)
	}

	// Reload (same server): ShowResolved persists (Phase 1), and the hidden
	// comment stays hidden — durable in the CSV.
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.inline-comment.is-resolved`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("reload: %v\nstderr: %s", err, p.stderr.String())
	}
	if n := countInline(); n != 1 {
		t.Fatalf("after reload, hidden comment should stay hidden; want 1 card, got %d", n)
	}

	// Unhide all (View menu item, only present while hidden ones exist) → both back.
	p.openViewItem("unhideAllResolved")
	if err := chromedp.Run(p.ctx, chromedp.Sleep(350*time.Millisecond)); err != nil {
		t.Fatalf("unhide all: %v", err)
	}
	if n := countInline(); n != 2 {
		t.Fatalf("after unhide-all, want 2 cards, got %d", n)
	}
	if h := countHidden(); h != 0 {
		t.Fatalf("CSV should have 0 hidden rows after unhide-all, got %d", h)
	}
}
