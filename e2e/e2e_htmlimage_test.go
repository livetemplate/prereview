//go:build browser

// End-to-end coverage for issue #104: the rendered-Markdown view must show a
// local raw-HTML <img> even when its block also carries a caption paragraph.
// The GitHub hero pattern puts a centered image <p> immediately before a caption
// <p> with NO blank line between them; goldmark (safe mode) groups both into one
// raw-HTML block, and before the fix the caption vetoed the whole block so the
// image rendered as `<!-- raw HTML omitted -->`. Boots prereview against a repo
// whose README exercises the pattern and reads the rendered .md-view DOM the
// reviewer actually sees. Per project convention the failure path dumps browser
// console + server stderr + rendered HTML.
//
// Run with: go test -tags=browser -run HeroImageWithCaption ./...

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// heroDoc is the fixture README: a centered image <p> followed immediately (no
// blank line) by a caption <p>. The single "\n" between the two <p> lines is
// load-bearing — a blank line would make goldmark parse them as separate blocks
// and never exercise the merged-block path this test guards.
const heroDoc = `<p align="center"><img src="docs/hero.gif" alt="hero" width="820"></p>` + "\n" +
	`<p align="center"><sub><em>a caption</em></sub></p>` + "\n"

func setupFixtureHeroRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	mustWrite(t, dir, "keep.go", "package keep\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	mustWrite(t, dir, "README.md", heroDoc)
	return dir
}

func TestE2E_HeroImageWithCaption(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureHeroRepo(t), 1200, 800)

	var consoleLines []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
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

	p.waitReady()
	p.clickFile("README.md")

	var html, text string
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.md-rendered`, chromedp.ByQuery),
		chromedp.OuterHTML(`.md-view`, &html, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.md-view').textContent`, &text),
	)
	cancel()

	diag := func() string {
		return "\n--- server ---\n" + p.stderr.String() +
			"\n--- console ---\n" + strings.Join(consoleLines, "\n") +
			"\n--- html ---\n" + html
	}
	if err != nil {
		t.Fatalf("rendered markdown never appeared: %v%s", err, diag())
	}

	// The hero image renders — a real <img> element with the src resolved
	// server-absolute against the file's directory (README.md at the root →
	// /docs/hero.gif), centered by the surviving wrapper align.
	if !strings.Contains(html, `<img src="/docs/hero.gif"`) {
		t.Errorf("hero <img> not rendered/resolved in .md-view%s", diag())
	}
	if !strings.Contains(html, `align="center"`) {
		t.Errorf("hero centering lost%s", diag())
	}
	// The block must NOT collapse to the omitted-raw-HTML placeholder — that is
	// the bug (the caption vetoing the whole block).
	if strings.Contains(html, "raw HTML omitted") {
		t.Errorf("merged image+caption block dropped as omitted raw HTML%s", diag())
	}
	// Only the image + wrapper are emitted; the caption text is dropped.
	if strings.Contains(text, "a caption") {
		t.Errorf("caption text leaked into rendered output%s", diag())
	}

	for _, line := range consoleLines {
		if strings.HasPrefix(line, "error ") {
			t.Errorf("browser console error: %s", line)
		}
	}
}
