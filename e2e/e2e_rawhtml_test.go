//go:build browser

package e2e

import (
	"testing"
)

// TestE2E_RawHTMLInMarkdown: a Markdown page built from raw HTML (a landing
// page) shows its headings and text in the preview, sanitized like GitHub's:
// no script element reaches the page, and no block renders empty.
func TestE2E_RawHTMLInMarkdown(t *testing.T) {
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "x.txt", "x\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "landing.md", "<!-- editors: keep snippets compact -->\n\n<main class=\"doc\">\n\n"+
		"<section class=\"hero\">\n  <h2>Build apps in Go</h2>\n  <p class=\"sub\">Write <code>html/template</code> and handlers.</p>\n"+
		"  <img src=\"x.png\" onerror=\"window.__pwned=1\">\n  <script>window.__pwned=1</script>\n</section>\n\n</main>\n\nPlain Markdown after it.\n")

	p := bootChromeAgainstRepo(t, dir, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("landing.md")
	waitJSDiag(t, p, `[...document.querySelectorAll('.md-rendered h2')].some(h=>h.textContent==='Build apps in Go')`, "the section's heading renders", diag)
	waitJSDiag(t, p, `[...document.querySelectorAll('.md-rendered code')].some(c=>c.textContent==='html/template')`, "its text and inline code render", diag)
	if n := evalInt(t, p, `document.querySelectorAll('.md-view script, .md-view [onerror]').length + (window.__pwned ? 1 : 0)`); n != 0 {
		t.Errorf("a script or handler reached the page (%d)%s", n, diag())
	}
	if n := evalInt(t, p, `[...document.querySelectorAll('.md-block .md-rendered')].filter(e=>!e.textContent.trim()&&!e.querySelector('img,hr')).length`); n != 0 {
		t.Errorf("%d blocks render empty%s", n, diag())
	}
}
