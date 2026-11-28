//go:build browser

package e2e

import (
	"testing"

	"github.com/chromedp/chromedp"
)

// TestE2E_BackToFilesOpensThePage: in a combined session, switching from the
// live site back to the files opens the file that serves the page the site
// was on (/page2 → page2.md). A page no reviewed file serves leaves the file
// that was open.
func TestE2E_BackToFilesOpensThePage(t *testing.T) {
	app := newLiveApp(t)
	repo := setupImportRepo(t)
	mustWrite(t, repo, "page2.md", "# Page two\n\nIts source.\n")
	p := bootChromeCombined(t, repo, app.srv.URL, 1200, 900, "--agent")
	diag := captureDiag(t, p)

	p.clickFile("plan.md")
	waitJSDiag(t, p, `document.querySelector('header.file-head .fh-base')?.textContent === 'plan.md'`, "plan.md open", diag)

	pickSurface(t, p, true, diag)
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(()=>{const f=document.querySelector('iframe.ext-frame');f.src=new URL('/page2',f.src).href;})()`, nil)); err != nil {
		t.Fatalf("move the site to /page2: %v%s", err, diag())
	}
	waitFramedPage(t, p, "/page2", diag)
	pickSurface(t, p, false, diag)
	waitJSDiag(t, p, `document.querySelector('header.file-head .fh-base')?.textContent === 'page2.md'`, "back on the files, page2.md is open", diag)

	// The site's home page has no file here: the open file stays.
	pickSurface(t, p, true, diag)
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(()=>{const f=document.querySelector('iframe.ext-frame');f.src=new URL('/',f.src).href;})()`, nil)); err != nil {
		t.Fatalf("move the site home: %v%s", err, diag())
	}
	waitFramedPage(t, p, "/", diag)
	pickSurface(t, p, false, diag)
	waitJSDiag(t, p, `document.querySelector('header.file-head .fh-base')?.textContent === 'page2.md'`, "no file for /, so page2.md stays open", diag)
}
