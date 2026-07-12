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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_HideResolvedComment(t *testing.T) {
	// Two already-resolved comments (seeded — robust vs the UI resolve loop, which is
	// finicky now that resolving collapses a card mid-interaction). Both start collapsed
	// to a green badge (#165).
	repo := setupFixtureRepo(t)
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,3,3,new,first-note,2026-06-30T12:00:00Z,true,,,line,,\n" +
		"c2,edited.go,4,4,new,second-note,2026-06-30T12:00:00Z,true,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")

	// #165: resolved cards stay in the DOM (collapsed to a green badge), so count and
	// read only the VISIBLE ones.
	countInline := func() int {
		var n int
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`[...document.querySelectorAll('.inline-comment')].filter(e=>e.offsetParent).length`, &n)); err != nil {
			t.Fatalf("count inline: %v", err)
		}
		return n
	}
	firstCardBody := func() string {
		var s string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`([...document.querySelectorAll('.inline-comment')].find(e=>e.offsetParent)?.querySelector('.body')||{textContent:""}).textContent`, &s)); err != nil {
			t.Fatalf("read first card body: %v", err)
		}
		return strings.TrimSpace(s)
	}

	if n := countInline(); n != 0 {
		t.Fatalf("both resolved should be collapsed (not visible) by default; %d still shown", n)
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
