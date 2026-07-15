//go:build browser

// Regression coverage for the "Ctrl+C to copy opens the file-comment dialog"
// bug: bare-letter shortcuts (here "c" = openFileComment) used to fire even when
// a command modifier was held, so copying selected diff text hijacked the copy
// chord and popped the composer. The fix lives in the vendored livetemplate
// client's keyFilterMatches (a bare key now matches only when no Ctrl/Meta/Alt
// is held; Shift stays allowed for keys like "?").
//
// The chord is delivered as a synthetic window keydown, NOT a CDP key event:
// Chrome swallows a real Ctrl+C (rawKeyDown + Ctrl modifier) as its native copy
// command and dispatches NO DOM keydown at all, so CDP input can't exercise this
// path. A synthetic KeyboardEvent runs the exact client listener + keyFilterMatches
// logic the fix lives in. A synthetic plain "c" (composer opens) is the control
// that proves the negative assertion isn't passing trivially.
//
// Run with: go test -tags=browser -run CopyShortcut ./...

package e2e

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestE2E_CopyShortcutDoesNotOpenComposer(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)

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
				} else {
					parts = append(parts, a.Description)
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
		t.Fatalf("enable network: %v", err)
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
	p.clickFile("edited.go")

	composerPresent := func() bool {
		var v bool
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`!!document.querySelector('.composer')`, &v))
		return v
	}

	// Select a word in the diff, mirroring the reviewer about to copy it. Picks
	// the first non-empty code token so it doesn't hardcode line content.
	selectJS := `(() => {
		const span = [...document.querySelectorAll('.code [data-line-text] span')]
			.find(s => s.firstChild && s.textContent.trim().length > 1);
		if (!span) return 'no token';
		const node = span.firstChild;
		const r = document.createRange();
		r.setStart(node, 0);
		r.setEnd(node, Math.min(3, node.textContent.length));
		const sel = window.getSelection();
		sel.removeAllRanges();
		sel.addRange(r);
		return 'ok';
	})()`
	var selResult string
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.code [data-line-text]`, chromedp.ByQuery),
		chromedp.Evaluate(selectJS, &selResult),
	)
	cancel()
	if err != nil || selResult != "ok" {
		t.Fatalf("selecting a diff word failed: %v (select=%q)%s", err, selResult, diag())
	}

	// The bug: Ctrl+C (event.key=="c", ctrlKey) fired the "c" openFileComment
	// shortcut. Dispatch the copy chord as a synthetic window keydown and assert
	// the composer stays closed.
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`window.dispatchEvent(new KeyboardEvent('keydown',{key:'c',ctrlKey:true,bubbles:true,cancelable:true}))`, nil,
	)); err != nil {
		t.Fatalf("dispatch Ctrl+C: %v%s", err, diag())
	}
	// Give a shortcut round-trip (WS + morphdom) time to (wrongly) open it.
	for i := 0; i < 8; i++ {
		if composerPresent() {
			t.Fatalf("Ctrl+C opened the file-comment composer — the copy chord hijacked the \"c\" shortcut%s", diag())
		}
		time.Sleep(75 * time.Millisecond)
	}

	// Control: the SAME synthetic path with a plain "c" (no modifier) MUST open
	// the composer — otherwise the negative assertion above passes trivially and
	// the fix narrowed the match rather than disabling the shortcut.
	stepCtx, stepCancel := context.WithTimeout(p.ctx, 15*time.Second)
	err = chromedp.Run(stepCtx,
		chromedp.Evaluate(`window.dispatchEvent(new KeyboardEvent('keydown',{key:'c',bubbles:true,cancelable:true}))`, nil),
		chromedp.WaitVisible(`.composer textarea[name="body"]`, chromedp.ByQuery),
	)
	stepCancel()
	if err != nil {
		t.Fatalf("plain \"c\" should still open the composer: %v%s", err, diag())
	}

	mu.Lock()
	for _, l := range consoleLines {
		if strings.Contains(strings.ToLower(l), "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
}
