//go:build browser

package e2e

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

// TestE2E_FrontMatterRows: #294. A page's YAML front matter renders as
// compact key/value rows, not a rule and an <h2>; the TOC leaves the keys
// out; and a click on the title row comments on just that line.
func TestE2E_FrontMatterRows(t *testing.T) {
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "page.md", "# Old\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "page.md", "---\ntitle: \"Getting started\"\ndescription: Install and run your first app.\nlayout: landing\n---\n\n# Getting started\n\n## Install\n\nRun the installer.\n")

	p := bootChromeAgainstRepo(t, dir, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("page.md")
	waitJSDiag(t, p, `document.querySelectorAll('.md-rendered table.md-frontmatter').length === 4`, "header + three key rows", diag)

	if n := evalInt(t, p, `[...document.querySelectorAll('.md-rendered h2, .md-rendered hr')].filter(e=>/layout|title:/.test(e.textContent)||e.tagName==='HR').length`); n != 0 {
		t.Errorf("front matter still renders as a rule or a heading (%d)%s", n, diag())
	}
	if n := evalInt(t, p, `[...document.querySelectorAll('.toc a, nav.toc a, .toc-list a')].filter(a=>/layout|landing/.test(a.textContent)).length`); n != 0 {
		t.Errorf("the TOC lists the front matter keys%s", diag())
	}
	var key string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`[...document.querySelectorAll('.md-frontmatter tbody th')].map(e=>e.textContent).join(',')`, &key)); err != nil || key != "title,description,layout" {
		t.Fatalf("keys = %q (%v), want title,description,layout%s", key, err, diag())
	}

	// Comment on the title row alone.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`[...document.querySelectorAll('.md-block .md-rendered')].find(e=>e.textContent.includes('Getting started')&&e.querySelector('.md-frontmatter')).click()`, nil),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "Shorter title", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.md-block .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("comment on the title row: %v%s", err, diag())
	}
	rows := p.readCSV()
	if len(rows) != 2 || rows[1][2] != "2" || rows[1][3] != "2" || !strings.Contains(rows[1][5], "Shorter title") {
		t.Fatalf("want one comment on L2, got %v%s", rows, diag())
	}
}
