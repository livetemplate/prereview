//go:build browser

package e2e

// E2E coverage for issue #26 (HTML preview renders the file's own CSS with
// real-document fidelity) under the #63 rework: the preview iframe is now an
// opaque-origin sandbox (sandbox="allow-scripts", no allow-same-origin) so it
// can run the page's own scripts — which means chromedp in the PARENT can no
// longer read the iframe's contentDocument. CSS-applied is therefore asserted by
// SCREENSHOTTING the iframe and sampling pixels (origin-agnostic), and bridge
// readiness by the parent-readable iframe.style.height the bridge sets.

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// setupFixtureCSSRepo builds a repo with two reviewable HTML files:
//   - page.html at the root: a page-level layout (inline <style> with :root
//     custom props, body background, @media + vw, position:sticky).
//   - docs/index.html in a subdirectory linking a sibling docs/styles.css —
//     the issue's own shape, where the stylesheet must load from the right
//     subdir path (now ALSO proving opaque-origin subresource loading).
func setupFixtureCSSRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	mustWrite(t, dir, "keep.go", "package keep\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	page, err := os.ReadFile(filepath.Join("testdata", "htmlpreview", "page.html"))
	if err != nil {
		t.Fatalf("read page.html fixture: %v", err)
	}
	mustWrite(t, dir, "page.html", string(page))

	// Subdir linked-CSS case: reuse the existing index.html/styles.css fixture
	// (a <link rel=stylesheet href=styles.css> with body{background:green}) but
	// place it under docs/ so the stylesheet only loads if the preview resolves
	// it against the file's directory.
	for _, name := range []string{"index.html", "styles.css"} {
		body, err := os.ReadFile(filepath.Join("testdata", "htmlpreview", name))
		if err != nil {
			t.Fatalf("read testdata/htmlpreview/%s: %v", name, err)
		}
		mustWrite(t, dir, filepath.Join("docs", name), string(body))
	}
	return dir
}

// waitPreviewBridgeReady blocks until the preview iframe has been auto-sized by
// the bridge (the in-iframe beacon posted its height → parent set style.height).
// A positive style.height proves the postMessage round-trip completed, so the
// document has laid out and the screenshot will be meaningful.
func waitPreviewBridgeReady(t *testing.T, p *runningPrereview) {
	t.Helper()
	ctx, cancel := context.WithTimeout(p.ctx, 12*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`iframe.html-preview`, chromedp.ByQuery),
		chromedp.Poll(
			`(() => { const f=document.querySelector('iframe.html-preview'); return !!f && parseFloat(f.style.height) > 0; })()`,
			nil, chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("preview bridge never sized the iframe (height stayed 0): %v\nstderr: %s", err, p.stderr.String())
	}
}

// previewFractionGreen screenshots the preview iframe and returns the fraction
// of a sampled interior grid whose pixels are near-green. A grid (not a single
// pixel) is robust to fixtures that paint sparse non-background content (page
// .html has a small dark <nav> + heading text over a green body) — the body
// background dominates the area, so a strong majority being green proves the
// page's CSS applied. The opaque iframe's contentDocument is unreadable, so the
// rendered pixels are the only origin-agnostic signal.
func previewFractionGreen(t *testing.T, p *runningPrereview) (float64, string) {
	t.Helper()
	var buf []byte
	ctx, cancel := context.WithTimeout(p.ctx, 8*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Screenshot(`iframe.html-preview`, &buf, chromedp.ByQuery)); err != nil {
		return 0, "screenshot failed: " + err.Error()
	}
	img, err := png.Decode(bytes.NewReader(buf))
	if err != nil {
		return 0, "png decode failed: " + err.Error()
	}
	b := img.Bounds()
	const n = 6 // 6x6 interior grid
	green, total := 0, 0
	for i := 1; i <= n; i++ {
		for j := 1; j <= n; j++ {
			x := b.Min.X + (b.Dx()*i)/(n+1)
			y := b.Min.Y + (b.Dy()*j)/(n+1)
			r, g, bl, _ := img.At(x, y).RGBA()
			r8, g8, b8 := r>>8, g>>8, bl>>8
			if g8 > 100 && r8 < 90 && b8 < 90 {
				green++
			}
			total++
		}
	}
	return float64(green) / float64(total), fmt.Sprintf("%d/%d grid pixels near-green", green, total)
}

// TestE2E_HTMLPreviewRunsScripts is the core #63 fix: a page whose CSS is
// produced by its OWN JavaScript must render styled in the preview. The opaque-
// origin sandbox (sandbox="allow-scripts") runs the script; the bridge sizes the
// iframe. Hermetic — an inline script sets the background (NOT the real Tailwind
// CDN, which would need network and shift layout async).
func TestE2E_HTMLPreviewRunsScripts(t *testing.T) {
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "t@e.com")
	runCmd(t, dir, "git", "config", "user.name", "T")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "keep.go", "package keep\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "jsbg.html",
		"<!doctype html><html><head><style>html,body{margin:0;min-height:300px}</style></head>"+
			"<body><h1>hi</h1><script>document.body.style.background='rgb(0,128,0)'</script></body></html>\n")

	p := bootChromeAgainstRepo(t, dir, 1200, 800)
	p.waitReady()
	p.clickFile("jsbg.html")
	waitPreviewBridgeReady(t, p)
	frac, info := previewFractionGreen(t, p)
	if frac < 0.5 {
		t.Errorf("page script did not run in the sandbox — preview not green (%s)\nstderr: %s", info, p.stderr.String())
	}
}

// TestE2E_HTMLPreviewImplicitBody is issue #79: an HTML5 page that omits the
// optional <head>/<body> tags must still render a preview (not fall back to the
// raw line view). The fixture is the issue's exact repro — doctype + <html>, a
// head <style>, an <h1> flow element, no <body> — plus a min-height so the green
// body background has area to sample. A visible iframe proves ShowRenderedHTML()
// flipped; the green pixels prove the page's CSS applied inside it.
func TestE2E_HTMLPreviewImplicitBody(t *testing.T) {
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "t@e.com")
	runCmd(t, dir, "git", "config", "user.name", "T")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "keep.go", "package keep\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "nobody.html",
		"<!doctype html><html>\n"+
			"<style>html,body{margin:0;min-height:300px}body{background:rgb(0,128,0)}</style>\n"+
			"<h1>hi</h1>\n"+
			"</html>\n")

	p := bootChromeAgainstRepo(t, dir, 1200, 800)
	p.waitReady()
	p.clickFile("nobody.html")
	// If the fix regressed, no iframe would ever appear (raw view instead) and
	// this wait fails loudly rather than silently passing.
	waitPreviewBridgeReady(t, p)
	frac, info := previewFractionGreen(t, p)
	if frac < 0.5 {
		t.Errorf("body-less page did not render a styled preview — %s (want majority green)\nstderr: %s", info, p.stderr.String())
	}
}

func TestE2E_HTMLPreviewRendersOwnCSS(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureCSSRepo(t), 1200, 800)
	p.waitReady()

	for _, tc := range []struct {
		file string
		desc string
	}{
		{"page.html", "inline <style> page-level fixture"},
		{"docs/index.html", "subdir linked stylesheet (opaque-origin subresource)"},
	} {
		p.clickFile(tc.file)
		waitPreviewBridgeReady(t, p)
		frac, info := previewFractionGreen(t, p)
		if frac < 0.5 {
			var html string
			_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`.html-view`, &html, chromedp.ByQuery))
			t.Errorf("[%s: %s] preview CSS did not apply — %s (want majority green)\n--- server ---\n%s\n--- html ---\n%s",
				tc.file, tc.desc, info, p.stderr.String(), html)
		}
	}
}
