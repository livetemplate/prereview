//go:build browser

package e2e

import (
	"strings"
	"sync"
	"testing"

	"github.com/chromedp/chromedp"
	cdpruntime "github.com/chromedp/cdproto/runtime"
)

// longCodeLine is far wider than any viewport under test (~340 chars), so on
// desktop it MUST produce horizontal scroll and on mobile it MUST wrap.
const longCodeLine = "\tresult := doSomethingWithAVeryLongName(ctx, request.Payload.Items, options.WithRetries(5), options.WithTimeout(30*time.Second), options.WithBackoff(backoff.Exponential), options.WithLogger(logger.Named(\"worker\")), options.WithMetrics(metrics.Default))"

func setupFixtureRepoLongLine(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "wide.go", "package wide\n\nfunc run() {\n\tresult := seed()\n\t_ = result\n}\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "wide.go", "package wide\n\nfunc run() {\n"+longCodeLine+"\n\t_ = result\n}\n")
	return dir
}

// lineMetrics is the geometry the truncation assertions read out of the browser.
type lineMetrics struct {
	CodeScrollW  float64 `json:"codeScrollW"`
	CodeClientW  float64 `json:"codeClientW"`
	ScrollLeftAt float64 `json:"scrollLeftAt"` // scrollLeft after asking for a huge value
	LineRectW    float64 `json:"lineRectW"`
	ContentRectW float64 `json:"contentRectW"`
	ContentH     float64 `json:"contentH"`
	LineHeight   float64 `json:"lineHeight"`
	Text         string  `json:"text"`
}

// measureLongLine finds the diff row carrying longCodeLine and reports the
// geometry that distinguishes "scrolls", "wraps", and "clipped".
const measureLongLine = `(() => {
  const code = document.querySelector('.code');
  const contents = [...code.querySelectorAll('.content')];
  const el = contents.find(c => c.textContent.includes('doSomethingWithAVeryLongName'));
  if (!el) return {text: 'NOT_FOUND'};
  const line = el.closest('.line');
  code.scrollLeft = 99999;
  const at = code.scrollLeft;
  code.scrollLeft = 0;
  return {
    codeScrollW: code.scrollWidth,
    codeClientW: code.clientWidth,
    scrollLeftAt: at,
    lineRectW: line.getBoundingClientRect().width,
    contentRectW: el.getBoundingClientRect().width,
    contentH: el.getBoundingClientRect().height,
    lineHeight: parseFloat(getComputedStyle(el).lineHeight),
    text: el.textContent,
  };
})()`

// openWideDiff boots prereview at the given viewport, opens wide.go, and
// returns the measured geometry plus the captured console/WS/stderr context.
func openWideDiff(t *testing.T, w, h int) (lineMetrics, string) {
	t.Helper()
	p := bootChromeAgainstRepo(t, setupFixtureRepoLongLine(t), w, h)

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
		}
	})

	var m lineMetrics
	var html string
	err := chromedp.Run(p.ctx,
		chromedp.EmulateViewport(int64(w), int64(h)),
		chromedp.Navigate(p.url),
		chromedp.Click(`//button[contains(., 'wide.go')]`, chromedp.BySearch),
		chromedp.WaitVisible(`.code .line`, chromedp.ByQuery),
		chromedp.Evaluate(measureLongLine, &m),
		chromedp.OuterHTML(`main.viewer`, &html, chromedp.ByQuery),
	)

	mu.Lock()
	ctxDump := "\nserver stderr: " + p.stderr.String() +
		"\nconsole: " + strings.Join(consoleLines, "\n") +
		"\nws frames: " + strings.Join(wsFrames, "\n") +
		"\nviewer html: " + snippet([]byte(html))
	mu.Unlock()

	if err != nil {
		t.Fatalf("open wide.go at %dx%d: %v%s", w, h, err, ctxDump)
	}
	if m.Text == "NOT_FOUND" {
		t.Fatalf("long line not found in the rendered diff at %dx%d%s", w, h, ctxDump)
	}
	return m, ctxDump
}

// TestE2E_LongLineDesktopScrolls pins the desktop contract: above the 900px
// breakpoint long lines SCROLL horizontally (white-space:pre, .line
// width:max-content) rather than being clipped. The regression this guards is
// silent — the text is present in the DOM and the classes are right, so only
// geometry catches it: `content-visibility:auto` on .line-row applies layout
// AND paint containment to on-screen rows too, so a max-content .line never
// extends .code's scrollWidth and the overflow is clipped instead of scrollable.
func TestE2E_LongLineDesktopScrolls(t *testing.T) {
	m, ctxDump := openWideDiff(t, 1200, 800)

	if m.CodeScrollW <= m.CodeClientW {
		t.Errorf("desktop: .code has nothing to scroll — scrollWidth %.0f <= clientWidth %.0f;\n"+
			"the long line is being CLIPPED instead of scrollable%s", m.CodeScrollW, m.CodeClientW, ctxDump)
	}
	if m.ScrollLeftAt <= 0 {
		t.Errorf("desktop: .code.scrollLeft stayed at %.0f after requesting max scroll — not scrollable%s",
			m.ScrollLeftAt, ctxDump)
	}
	// The line box must actually grow to its content, not stop at the container.
	if m.LineRectW <= m.CodeClientW {
		t.Errorf("desktop: .line width %.0f did not grow past the container %.0f (width:max-content not taking effect)%s",
			m.LineRectW, m.CodeClientW, ctxDump)
	}
	// white-space:pre means exactly one visual line — no wrapping on desktop.
	if m.ContentH > m.LineHeight*1.5 {
		t.Errorf("desktop: content height %.1f exceeds one line (%.1f) — it wrapped, but desktop should scroll%s",
			m.ContentH, m.LineHeight, ctxDump)
	}
	t.Logf("desktop geometry: scrollW=%.0f clientW=%.0f scrollLeftMax=%.0f lineW=%.0f contentH=%.1f",
		m.CodeScrollW, m.CodeClientW, m.ScrollLeftAt, m.LineRectW, m.ContentH)
}

// TestE2E_LongLineMobileWraps pins the mobile contract: below the breakpoint
// long lines WRAP (pre-wrap + overflow-wrap:anywhere) and .code has NO
// horizontal scroll — the Safari focus-scroll fix documented on .code. A
// desktop-side fix must not leak into this path.
func TestE2E_LongLineMobileWraps(t *testing.T) {
	m, ctxDump := openWideDiff(t, 390, 844)

	// Allow a 1px rounding slack on the scroll comparison.
	if m.CodeScrollW > m.CodeClientW+1 {
		t.Errorf("mobile: .code became horizontally scrollable (scrollWidth %.0f > clientWidth %.0f) — "+
			"the wrap regressed and content can be clipped off-screen%s", m.CodeScrollW, m.CodeClientW, ctxDump)
	}
	// Wrapped means the content occupies several visual lines.
	if m.ContentH < m.LineHeight*1.5 {
		t.Errorf("mobile: content height %.1f is a single line (%.1f) — the long line did NOT wrap, so it is clipped%s",
			m.ContentH, m.LineHeight, ctxDump)
	}
	t.Logf("mobile geometry: scrollW=%.0f clientW=%.0f contentH=%.1f lineHeight=%.1f",
		m.CodeScrollW, m.CodeClientW, m.ContentH, m.LineHeight)
}
