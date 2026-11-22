//go:build browser

// prereview's attribute handlers (selection, deep links, viewport report, the
// bridges) ship in /prereview-handlers.js and register with the livetemplate
// client through its public API (livetemplate#473 Phase 4). These tests pin
// both sides of that split:
//
//   - with the bundle, a deep link works on page load. That path needs
//     registration from a second script, runOnConnect and the server channel;
//   - without it, the client says which attributes nothing handles, nothing
//     throws, and the core client keeps working.

package e2e

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/fetch"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const unclaimedWarning = "no attribute handler claims it"

// consoleRecorder collects console messages and uncaught exceptions.
type consoleRecorder struct {
	mu         sync.Mutex
	lines      []string
	exceptions []string
}

func recordConsole(p *runningPrereview) *consoleRecorder {
	r := &consoleRecorder{}
	chromedp.ListenTarget(p.ctx, func(ev any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			r.lines = append(r.lines, string(e.Type)+" "+joinArgs(e.Args))
		case *cdpruntime.EventExceptionThrown:
			r.exceptions = append(r.exceptions, e.ExceptionDetails.Error())
		}
	})
	return r
}

func (r *consoleRecorder) matching(substr string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, l := range r.lines {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return out
}

func (r *consoleRecorder) dump(p *runningPrereview) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var html string
	_ = chromedp.Run(p.ctx, chromedp.OuterHTML("main.viewer", &html, chromedp.ByQuery, chromedp.AtLeast(0)))
	return "\nconsole:\n" + strings.Join(r.lines, "\n") +
		"\nexceptions:\n" + strings.Join(r.exceptions, "\n") +
		"\nserver:\n" + p.stderr.String() +
		"\nmain.viewer:\n" + html
}

// openDeepLink loads the review at #fresh.go:L1 and waits for the load event
// plus the census check that runs after it.
func openDeepLink(p *runningPrereview) error {
	return chromedp.Run(p.ctx,
		chromedp.EmulateViewport(1400, 900),
		chromedp.Navigate(p.url+"#fresh.go:L1"),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
		chromedp.Poll(`document.readyState === "complete"`, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Sleep(time.Second),
	)
}

func TestE2E_HandlerBundleDeepLinkOnLoad(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1400, 900)
	rec := recordConsole(p)

	if err := openDeepLink(p); err != nil {
		t.Fatalf("open deep link: %v%s", err, rec.dump(p))
	}
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`main.viewer .file-head-name[title="fresh.go"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("#fresh.go:L1 did not open fresh.go, so the url-hash handler did not run on connect: %v%s", err, rec.dump(p))
	}
	if got := rec.matching(unclaimedWarning); len(got) > 0 {
		t.Fatalf("attributes reported as unhandled with the handler bundle loaded:\n%s%s", strings.Join(got, "\n"), rec.dump(p))
	}
	if len(rec.exceptions) > 0 {
		t.Fatalf("uncaught exceptions%s", rec.dump(p))
	}
}

func TestE2E_HandlerBundleMissingWarnsAndKeepsWorking(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1400, 900)
	rec := recordConsole(p)

	// Fail the handler bundle's request, as a page that forgot its <script>
	// tag would never ask for it.
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*fetch.EventRequestPaused); ok {
			go func() {
				_ = chromedp.Run(p.ctx, fetch.FailRequest(e.RequestID, "BlockedByClient"))
			}()
		}
	})
	if err := chromedp.Run(p.ctx, fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*/prereview-handlers.js"}})); err != nil {
		t.Fatalf("enable request interception: %v", err)
	}

	if err := openDeepLink(p); err != nil {
		t.Fatalf("open deep link: %v%s", err, rec.dump(p))
	}

	warned := strings.Join(rec.matching(unclaimedWarning), "\n")
	for _, name := range []string{"lvt-fx:url-hash", "lvt-fx:text-select", "lvt-fx:viewport-report"} {
		if !strings.Contains(warned, name) {
			t.Errorf("no unhandled-attribute warning for %s%s", name, rec.dump(p))
		}
	}
	var fresh bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`!!document.querySelector('main.viewer .file-head-name[title="fresh.go"]')`, &fresh)); err != nil {
		t.Fatalf("read selected file: %v", err)
	}
	if fresh {
		t.Errorf("fresh.go opened from the deep link with no url-hash handler loaded; the test is not exercising the missing bundle%s", rec.dump(p))
	}
	if len(rec.exceptions) > 0 {
		t.Fatalf("uncaught exceptions without the handler bundle%s", rec.dump(p))
	}

	// The core client still works: selecting a file round-trips over the socket.
	p.clickFile("fresh.go")
}
