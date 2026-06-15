//go:build browser

package main

// E2E coverage for issue #26: the HTML preview must render the file's own CSS
// with real-document fidelity. Rendering is via a sandboxed <iframe srcdoc>, so
// the assertions read computed styles from the iframe's contentDocument (the
// same access path the client directive uses for click-to-select).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

// setupFixtureCSSRepo builds a repo with two reviewable HTML files:
//   - page.html at the root: a page-level layout (inline <style> with :root
//     custom props, body background, @media + vw, position:sticky).
//   - docs/index.html in a subdirectory linking a sibling docs/styles.css —
//     the issue's own shape, where the stylesheet must load from the right
//     subdir path.
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

// iframeBodyBg returns getComputedStyle(body).backgroundColor read from inside
// the preview iframe's contentDocument (empty string if the iframe or its doc
// isn't reachable yet).
const iframeBodyBgJS = `(() => {
	const fr = document.querySelector('iframe.html-preview');
	if (!fr) return 'NO-IFRAME';
	const doc = fr.contentDocument;
	if (!doc || !doc.body) return 'NO-CONTENTDOC';
	return getComputedStyle(doc.body).backgroundColor;
})()`

func TestE2E_HTMLPreviewRendersOwnCSS(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureCSSRepo(t), 1200, 800)
	p.waitReady()

	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return fmt.Sprintf("\n--- server ---\n%s\n--- html ---\n%s", p.stderr.String(), html)
	}

	for _, tc := range []struct {
		file string
		desc string
	}{
		{"page.html", "inline <style> page-level fixture"},
		{"docs/index.html", "subdir linked stylesheet"},
	} {
		p.clickFile(tc.file)

		var bg string
		ctx, cancel := context.WithTimeout(p.ctx, 15e9)
		err := chromedp.Run(ctx,
			chromedp.WaitVisible(`iframe.html-preview`, chromedp.ByQuery),
			chromedp.Poll(
				`(() => {
					const fr = document.querySelector('iframe.html-preview');
					const doc = fr && fr.contentDocument;
					return !!(doc && doc.body && getComputedStyle(doc.body).backgroundColor === 'rgb(0, 128, 0)');
				})()`,
				nil,
				chromedp.WithPollingTimeout(10e9),
			),
			chromedp.Evaluate(iframeBodyBgJS, &bg),
		)
		cancel()
		if err != nil {
			t.Fatalf("[%s: %s] preview body background never became green: %v%s",
				tc.file, tc.desc, err, diag())
		}
		if !strings.Contains(bg, "0, 128, 0") {
			t.Errorf("[%s: %s] iframe body background = %q, want rgb(0, 128, 0)%s",
				tc.file, tc.desc, bg, diag())
		}
	}
}
