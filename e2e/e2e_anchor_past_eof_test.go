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
	// Its line no longer exists, so there is no inline row to hang the card on;
	// the All-comments view (`a`) is where a past-EOF comment surfaces.
	var badge bool
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent("a"),
		chromedp.WaitVisible(`section.all-comments`, chromedp.ByQuery),
		chromedp.Evaluate(`(()=>{const li=[...document.querySelectorAll('section.all-comments .ac-item')].find(x=>x.textContent.includes('drop this line')); return !!li && !!li.querySelector('.outdated-badge');})()`, &badge),
	); err != nil || !badge {
		t.Errorf("expected the comment listed in All comments with an outdated badge (err=%v)%s", err, diag())
	}
	mu.Lock()
	for _, l := range consoleLines {
		if strings.HasPrefix(l, "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	mu.Unlock()
}
