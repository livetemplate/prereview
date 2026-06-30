//go:build browser

// End-to-end coverage for issue #49: a local-sibling <img> must render in the
// Markdown preview. Two shapes are exercised against one fixture repo:
//
//   - the GitHub-README hero pattern — a raw-HTML `<p align="center"><img …>`
//     that goldmark's safe mode used to drop wholesale (`<!-- raw HTML
//     omitted -->`); it must now render a centered, loaded image.
//   - a Markdown-syntax `![](sibling.gif)` inside a SUBDIRECTORY README, whose
//     src must resolve server-absolute (`/docs/…`) so it loads — previously it
//     resolved browser-relative to `/` and 404'd from a subdir.
//
// Per project convention the failure path dumps browser console + server
// stderr + rendered HTML.
//
// Run with: go test -tags=browser -run Image ./...

package e2e

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// gif1x1 is a minimal valid 1×1 GIF — enough that a successful decode reports
// naturalWidth > 0, which is how the test proves the image actually loaded
// (and so was served by the static fallback) rather than merely being in the
// DOM as a broken-image tag.
const gif1x1Base64 = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"

// heroReadme is a root README using the raw-HTML centered-hero pattern that
// #49 is about, referencing an image in a subdirectory.
const heroReadme = `<p align="center"><img src="docs/hero.gif" alt="hero" width="80"></p>` + "\n\n" +
	"# Project\n\nBody text under the hero.\n"

// subdirGuide is a README in docs/ using Markdown-syntax image syntax with a
// sibling src — the bundled subdir-resolution fix. `hero.gif` here means
// docs/hero.gif, which must resolve to /docs/hero.gif.
const subdirGuide = "# Guide\n\n![logo](hero.gif)\n"

func setupFixtureImageRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	mustWrite(t, dir, "keep.go", "package keep\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	mustWrite(t, dir, "README.md", heroReadme)
	mustWrite(t, dir, filepath.Join("docs", "GUIDE.md"), subdirGuide)

	// The actual image, served by the static fallback (server.go). Written as
	// raw bytes — mustWrite is string-only — so the GIF decodes for real.
	gif, err := base64.StdEncoding.DecodeString(gif1x1Base64)
	if err != nil {
		t.Fatalf("decode gif fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "hero.gif"), gif, 0o644); err != nil {
		t.Fatalf("write hero.gif: %v", err)
	}
	return dir
}

// imgProbeJS returns, for the first <img> inside .md-rendered: whether it
// loaded (naturalWidth>0), its resolved src, and its wrapper's computed
// text-align — pipe-joined so one Evaluate call yields all three.
const imgProbeJS = `(() => {
	const img = document.querySelector('.md-rendered img');
	if (!img) return 'NO-IMG';
	const p = img.closest('p,div');
	const align = p ? getComputedStyle(p).textAlign : '';
	return [img.naturalWidth > 0 ? 'loaded' : 'broken', img.getAttribute('src'), align].join('|');
})()`

func TestE2E_MarkdownLocalImages(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureImageRepo(t), 1200, 800)

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

	diag := func(html string) string {
		return "\n--- server ---\n" + p.stderr.String() +
			"\n--- console ---\n" + strings.Join(consoleLines, "\n") +
			"\n--- html ---\n" + html
	}

	// --- Case 1: raw-HTML centered hero in the root README ---
	p.clickFile("README.md")
	var heroHTML, heroProbe string
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.md-rendered img`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond), // let the tiny GIF finish decoding
		chromedp.OuterHTML(`.md-view`, &heroHTML, chromedp.ByQuery),
		chromedp.Evaluate(imgProbeJS, &heroProbe),
	)
	cancel()
	if err != nil {
		t.Fatalf("hero image never rendered: %v%s", err, diag(heroHTML))
	}
	if strings.Contains(heroHTML, "raw HTML omitted") {
		t.Errorf("hero <img> was dropped as raw HTML%s", diag(heroHTML))
	}
	loaded, gotSrc, align := split3(heroProbe)
	if loaded != "loaded" {
		t.Errorf("hero image did not load (probe=%q)%s", heroProbe, diag(heroHTML))
	}
	if gotSrc != "/docs/hero.gif" {
		t.Errorf("hero src = %q, want server-absolute /docs/hero.gif%s", gotSrc, diag(heroHTML))
	}
	if align != "center" {
		t.Errorf("hero wrapper text-align = %q, want center%s", align, diag(heroHTML))
	}

	// --- Case 2: Markdown-syntax sibling image in a subdir README ---
	p.clickFile("docs/GUIDE.md")
	var guideHTML, guideProbe string
	ctx2, cancel2 := context.WithTimeout(p.ctx, 15*time.Second)
	err = chromedp.Run(ctx2,
		chromedp.WaitVisible(`.md-rendered img`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.OuterHTML(`.md-view`, &guideHTML, chromedp.ByQuery),
		chromedp.Evaluate(imgProbeJS, &guideProbe),
	)
	cancel2()
	if err != nil {
		t.Fatalf("subdir markdown image never rendered: %v%s", err, diag(guideHTML))
	}
	loaded2, gotSrc2, _ := split3(guideProbe)
	if gotSrc2 != "/docs/hero.gif" {
		t.Errorf("subdir markdown img src = %q, want /docs/hero.gif%s", gotSrc2, diag(guideHTML))
	}
	if loaded2 != "loaded" {
		t.Errorf("subdir markdown image did not load (probe=%q)%s", guideProbe, diag(guideHTML))
	}

	for _, line := range consoleLines {
		if strings.HasPrefix(line, "error ") {
			t.Errorf("browser console error: %s", line)
		}
	}
}

// split3 splits the pipe-joined imgProbeJS result into its three fields.
func split3(s string) (a, b, c string) {
	parts := strings.Split(s, "|")
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}
