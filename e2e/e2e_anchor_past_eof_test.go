//go:build browser

// End-to-end for #216: deleting lines so the reviewed file shrinks BELOW a
// comment's recorded line made re-anchoring index past the end of the file and
// panic on every page load. The comment must instead come back outdated, with
// the page rendering normally and nothing panicking in the server log.
//
// Run: go test -tags=browser -run TestE2E_AnchorPastEOF ./e2e/...

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestE2E_AnchorPastEOF(t *testing.T) {
	dir := setupFixtureRepoReanchor(t) // 6 lines; TARGET on line 5
	p := bootChromeAgainstRepo(t, dir, 1200, 800)

	var mu sync.Mutex
	var consoleLines, wsFrames []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		case *cdpnetwork.EventWebSocketFrameReceived:
			wsFrames = append(wsFrames, "recv "+e.Response.PayloadData)
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+e.Response.PayloadData)
		}
	})
	if err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.Enable().Do(ctx)
	})); err != nil {
		t.Fatalf("enable network domain: %v", err)
	}
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.waitReady()
	p.clickFile("docs.md")
	if !clickMdBlock(p, "TARGET sentence to anchor a comment on.") {
		t.Fatalf("could not find TARGET block%s", diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "drop this line", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v%s", err, diag())
	}

	// Shrink the file to 3 lines: the commented line 5 (and its text) no longer
	// exist, so the line is past the end of the file.
	if err := os.WriteFile(filepath.Join(dir, "docs.md"),
		[]byte("# Reanchor Fixture\n\nFirst context sentence stays put.\n"), 0o644); err != nil {
		t.Fatalf("shrink doc: %v", err)
	}

	// Reload: each load re-anchors twice (SSR + WS Mount); pre-fix, the SSR
	// request itself panicked (ERR_EMPTY_RESPONSE).
	p.waitReady()
	p.clickFile("docs.md")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reload: page did not render the doc: %v%s", err, diag())
	}

	if s := p.stderr.String(); strings.Contains(s, "panic") || strings.Contains(s, "index out of range") {
		t.Fatalf("server panicked re-anchoring a past-EOF comment%s", diag())
	}
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("want header + 1 comment row, got %v%s", rows, diag())
	}
	if rows[1][2] != "5" || rows[1][3] != "5" {
		t.Errorf("outdated comment ints must stay 5/5, got %s/%s%s", rows[1][2], rows[1][3], diag())
	}
	if rows[1][9] != "outdated" {
		t.Errorf("anchor_status = %q, want outdated (a deletion is not an in-place edit)%s", rows[1][9], diag())
	}
	// #218: its line no longer exists, so there is no row to hang the card on. It
	// must still be visible in the file view itself, in the end-of-file block, with
	// no extra navigation.
	var inBlock, blockBadge bool
	blockCtx, cancelBlock := within(p, 15*time.Second)
	defer cancelBlock()
	if err := chromedp.Run(blockCtx,
		chromedp.WaitVisible(`section.past-eof .inline-comment`, chromedp.ByQuery),
		chromedp.Evaluate(`(()=>{const c=[...document.querySelectorAll('section.past-eof .inline-comment')].find(x=>x.textContent.includes('drop this line')); return !!c && !!c.querySelector('.outdated-badge');})()`, &inBlock),
		chromedp.Evaluate(`(document.querySelector('section.past-eof .line-mark.is-open')||{}).textContent==='1'`, &blockBadge),
	); err != nil || !inBlock || !blockBadge {
		t.Fatalf("expected the past-EOF comment in the file view's end-of-file block with an outdated badge and an open count of 1 (err=%v card=%v badge=%v)%s", err, inBlock, blockBadge, diag())
	}
	// It appears exactly once: no inline row also carries it.
	var cards int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`[...document.querySelectorAll('.inline-comment')].filter(x=>x.textContent.includes('drop this line')).length`, &cards))
	if cards != 1 {
		t.Errorf("the past-EOF comment renders %d times in the file view, want 1%s", cards, diag())
	}

	// The All-comments view (`a`) lists it too.
	var badge bool
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent("a"),
		chromedp.WaitVisible(`section.all-comments`, chromedp.ByQuery),
		chromedp.Evaluate(`(()=>{const li=[...document.querySelectorAll('section.all-comments .ac-item')].find(x=>x.textContent.includes('drop this line')); return !!li && !!li.querySelector('.outdated-badge');})()`, &badge),
	); err != nil || !badge {
		t.Errorf("expected the comment listed in All comments with an outdated badge (err=%v)%s", err, diag())
	}
	// Back in the file view, the block's Re-anchor starts re-anchoring that comment.
	reCtx, cancelRe := within(p, 15*time.Second)
	defer cancelRe()
	if err := chromedp.Run(reCtx,
		chromedp.KeyEvent("a"),
		chromedp.WaitNotPresent(`section.all-comments`, chromedp.ByQuery),
		chromedp.Click(`section.past-eof button[name='reanchorComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.reanchor-prompt`, chromedp.ByQuery),
	); err != nil {
		t.Errorf("Re-anchor from the end-of-file block: %v%s", err, diag())
	}
	mu.Lock()
	for _, l := range consoleLines {
		if strings.HasPrefix(l, "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	mu.Unlock()
}

// TestE2E_SuggestionPastEOF (#218): a suggestion on a line the file later loses is
// shown in the end-of-file block, in both the rendered-Markdown and the Raw view.
func TestE2E_SuggestionPastEOF(t *testing.T) {
	dir := setupFixtureRepoReanchor(t) // 6 lines; TARGET on line 5
	p := bootChromeAgainstRepo(t, dir, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("docs.md")
	submitSuggestions(t, p.binary, p.repo, `[{"id":"eof1","file":"docs.md","from_line":6,"to_line":6,"original":"Trailing sentence after the target.","proposed":"A tighter trailing sentence.","note":"tighten"}]`)

	// Shrink the file to 3 lines: line 6 no longer exists.
	if err := os.WriteFile(filepath.Join(dir, "docs.md"),
		[]byte("# Reanchor Fixture\n\nFirst context sentence stays put.\n"), 0o644); err != nil {
		t.Fatalf("shrink doc: %v", err)
	}
	p.waitReady()
	p.clickFile("docs.md")

	count := func() int {
		var n int
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`[...document.querySelectorAll('.inline-suggestion')].filter(x=>x.textContent.includes('A tighter trailing sentence.')).length`, &n))
		return n
	}
	ctx, cancel := within(p, 15*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.WaitVisible(`section.past-eof .inline-suggestion`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("rendered view: no suggestion in the end-of-file block: %v%s", err, diag())
	}
	if n := count(); n != 1 {
		t.Errorf("rendered view: the past-EOF suggestion renders %d times, want 1%s", n, diag())
	}

	// Raw view: the same block, still exactly once.
	if err := chromedp.Run(ctx,
		chromedp.Click(`form[aria-label="Markdown view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
		chromedp.WaitVisible(`.code .line`, chromedp.ByQuery),
		chromedp.WaitVisible(`section.past-eof .inline-suggestion`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("raw view: no suggestion in the end-of-file block: %v%s", err, diag())
	}
	if n := count(); n != 1 {
		t.Errorf("raw view: the past-EOF suggestion renders %d times, want 1%s", n, diag())
	}
}
