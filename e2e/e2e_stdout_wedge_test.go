//go:build browser

// End-to-end for #215: in --agent mode the live event channel is stdout. An agent
// that never drains that pipe (or closes it) must not be able to stop emission —
// `prereview watch` reads events.jsonl, so every event has to keep landing there.
//
//   - Undrained: the event stream used to write unbuffered stdout BEFORE the
//     durable append, so a snapshot bigger than the pipe buffer (64 KiB on Linux)
//     blocked forever: events.jsonl froze and `end` never came.
//   - Closed: the default SIGPIPE on fd 1 killed the server outright, with no log.
//
// Run: go test -tags=browser -run 'TestE2E_Stdout' ./e2e/...

package e2e

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/internal/review"
)

// wedgeBodyBytes is far larger than any default pipe buffer, so ONE snapshot
// carrying a comment this big cannot fit in an undrained stdout.
const wedgeBodyBytes = 256 << 10

// wedgeHarness boots an --agent server whose stdout is handled by afterPreamble,
// wiring console/WS capture and a diag dump (server log, console, WS, HTML).
func wedgeHarness(t *testing.T, afterPreamble func(io.ReadCloser)) (*runningPrereview, func() string) {
	t.Helper()
	dir := setupFixtureRepoReanchor(t)
	p := bootChromeAgainstRepoStdout(t, dir, 1200, 800, afterPreamble, "--agent")

	var mu sync.Mutex
	var consoleLines, wsFrames []string
	// Frames echo the 256 KiB body; Clone so a stored clip doesn't pin it.
	clip := func(s string) string { return strings.Clone(clipFrame(s, 400)) }
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
			wsFrames = append(wsFrames, "recv "+clip(e.Response.PayloadData))
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+clip(e.Response.PayloadData))
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
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), clip(html))
	}
	return p, diag
}

// addHugeComment comments on the TARGET block with a wedgeBodyBytes body. The
// body is set via JS — SendKeys would type 256K keystrokes.
func addHugeComment(t *testing.T, p *runningPrereview, diag func() string) {
	t.Helper()
	p.waitReady()
	p.clickFile("docs.md")
	if !clickMdBlock(p, "TARGET sentence to anchor a comment on.") {
		t.Fatalf("could not find TARGET block%s", diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(`(()=>{const ta=document.querySelector('.composer textarea');
			ta.value='w'.repeat(%d); ta.dispatchEvent(new Event('input',{bubbles:true}));})()`, wedgeBodyBytes), nil),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v%s", err, diag())
	}
}

func hasHugeSnapshot(evs []review.StreamEvent) bool {
	for _, ev := range handoffEvents(evs) {
		for _, c := range ev.CommentList() {
			if len(c.Body) >= wedgeBodyBytes {
				return true
			}
		}
	}
	return false
}

// waitDurable polls the store's events.jsonl — what `prereview watch` reads.
func waitDurable(t *testing.T, p *runningPrereview, pred func([]review.StreamEvent) bool, what string, diag func() string) {
	t.Helper()
	waitEvents(t, "events.jsonl", func() string { return readEvents(t, p.repo) }, pred, what, diag)
}

func TestE2E_StdoutUndrained(t *testing.T) {
	// Keep the read end OPEN but never read it: an *os.File dropped on the floor
	// would be closed by its finalizer, turning the wedge into a closed pipe.
	held := make(chan io.ReadCloser, 1)
	p, diag := wedgeHarness(t, func(r io.ReadCloser) { held <- r })
	t.Cleanup(func() {
		select {
		case r := <-held:
			_ = r.Close()
		default:
		}
	})

	addHugeComment(t, p, diag)
	waitDurable(t, p, hasHugeSnapshot, "the oversized snapshot (emission wedged on stdout)", diag)

	endSession(t, p, diag)
	waitDurable(t, p, func(evs []review.StreamEvent) bool {
		return len(evs) > 0 && evs[len(evs)-1].Event == "end"
	}, "the end terminator", diag)

	// The flush of the wedged live channel is bounded, so shutdown still completes.
	exited := make(chan struct{})
	go func() { _, _ = p.cmd.Process.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(15 * time.Second):
		t.Fatalf("server did not exit after End session with an undrained stdout%s", diag())
	}
}

func TestE2E_StdoutClosed(t *testing.T) {
	p, diag := wedgeHarness(t, func(r io.ReadCloser) { _ = r.Close() })

	addHugeComment(t, p, diag)
	waitDurable(t, p, hasHugeSnapshot, "the snapshot after stdout was closed", diag)

	// Still serving: a fresh load renders (the old SIGPIPE death took the server down).
	p.waitReady()
	if err := p.cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("server process gone after writing to a closed stdout: %v%s", err, diag())
	}
	if s := p.stderr.String(); !strings.Contains(s, "live event channel closed") {
		t.Errorf("want one 'live event channel closed' warning in the server log%s", diag())
	}
}
