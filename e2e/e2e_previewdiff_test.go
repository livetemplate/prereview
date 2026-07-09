//go:build browser

// End-to-end coverage for issue #110: "diff highlighting should also show up in
// preview mode". The code/line view colours each added line green; the
// rendered-Markdown preview must surface the same add signal on its blocks
// (BlockDiffStatus → md-added / md-changed class on .md-block, a left change-bar
// on .md-rendered).
//
// The fixture COMMITS a multi-block Markdown file, then appends a brand-new
// paragraph in the working tree — so a `--base HEAD` diff carries context lines
// (the original blocks) plus one addition (the new paragraph). This is the case
// the highlight is for: a wholly-new file is a pure-add and deliberately shows
// NO highlight (parity with .code.pure-add), so it would be a false test.
//
// Run with: go test -tags=browser -run PreviewDiff ./e2e/...

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// setupFixturePreviewDiffRepo commits a 3-block Markdown doc, then appends a
// fourth (brand-new) paragraph in the working tree. Blocks 1–3 are unchanged
// (context); block 4 is a pure addition.
func setupFixturePreviewDiffRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	// prereview's own version store lives under .prereview/ and is always
	// gitignored in real repos; without this it pollutes the review file tree
	// (untracked blobs) and churns the render.
	mustWrite(t, dir, ".gitignore", ".prereview/\n")

	const base = "# Heading One\n\n" +
		"Alpha paragraph stays the same.\n\n" +
		"Bravo paragraph stays the same.\n"
	mustWrite(t, dir, "doc.md", base)
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	// Working-tree edit: append a brand-new paragraph (a pure-add block).
	mustWrite(t, dir, "doc.md", base+"\nCharlie paragraph is brand new.\n")
	return dir
}

func TestE2E_PreviewDiffHighlight(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixturePreviewDiffRepo(t), 1200, 900)

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
		case *cdpnetwork.EventWebSocketFrameReceived:
			wsFrames = append(wsFrames, "recv "+e.Response.PayloadData)
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+e.Response.PayloadData)
		}
	})
	if err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.Enable().Do(ctx)
	})); err != nil {
		t.Fatalf("enable network: %v", err)
	}

	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`.md-view`, &html, chromedp.ByQuery))
		// Screenshot to a stable temp path (NOT t.TempDir(), which is removed
		// when the test returns) so a CI failure leaves it behind to inspect.
		var shot []byte
		if err := chromedp.Run(p.ctx, chromedp.CaptureScreenshot(&shot)); err == nil {
			path := filepath.Join(os.TempDir(), "prereview-preview-diff.png")
			if os.WriteFile(path, shot, 0o644) == nil {
				t.Logf("screenshot: %s", path)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.waitReady()
	p.clickFile("doc.md")

	// The rendered-Markdown preview must be showing (not the line view).
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-view .md-rendered`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("rendered markdown preview never appeared: %v%s", err, diag())
	}

	// classOfBlockContaining returns the .md-block className whose rendered text
	// contains needle — the classList is where md-added / md-changed lands.
	classOfBlockContaining := func(needle string) string {
		js := fmt.Sprintf(`(()=>{
			const blocks=[...document.querySelectorAll('.md-view .md-block')];
			const b=blocks.find(el=>el.textContent.includes(%q));
			return b?b.className:'NOT_FOUND';
		})()`, needle)
		var cls string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &cls)); err != nil {
			t.Fatalf("eval block class for %q: %v%s", needle, err, diag())
		}
		return cls
	}

	// The brand-new paragraph's block is highlighted as an addition. Poll: the
	// class lands on the block once the file-select render settles.
	added := func() bool {
		for i := 0; i < 40; i++ {
			if strings.Contains(classOfBlockContaining("Charlie paragraph is brand new."), "md-added") {
				return true
			}
			chromedp.Run(p.ctx, chromedp.Sleep(75*time.Millisecond))
		}
		return false
	}
	if !added() {
		t.Errorf("new-paragraph block never carried md-added%s", diag())
	}

	// An unchanged block carries NO diff-highlight class (the whole point — only
	// changed content is flagged, matching the code view).
	for _, unchanged := range []string{"Heading One", "Alpha paragraph stays the same.", "Bravo paragraph stays the same."} {
		cls := classOfBlockContaining(unchanged)
		if strings.Contains(cls, "md-added") || strings.Contains(cls, "md-changed") {
			t.Errorf("unchanged block %q should have no diff-highlight class, got %q%s", unchanged, cls, diag())
		}
	}

	// The highlight is a real paint, not just a class: the change-bar is an inset
	// box-shadow on the added block's .md-rendered. Assert it renders a non-empty
	// box-shadow (guards the class→CSS wiring, which a class-only check misses —
	// see the gutter-occlusion memory).
	var shadow string
	shadowJS := `(()=>{
		const blocks=[...document.querySelectorAll('.md-view .md-block')];
		const b=blocks.find(el=>el.textContent.includes('Charlie paragraph is brand new.'));
		if(!b)return 'NO_BLOCK';
		const r=b.querySelector('.md-rendered');
		return r?getComputedStyle(r).boxShadow:'NO_RENDERED';
	})()`
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(shadowJS, &shadow)); err != nil {
		t.Fatalf("eval box-shadow: %v%s", err, diag())
	}
	if shadow == "" || shadow == "none" || strings.HasPrefix(shadow, "NO_") {
		t.Errorf("added block's .md-rendered has no change-bar box-shadow (got %q)%s", shadow, diag())
	}

	// No console errors (warnings tolerated).
	mu.Lock()
	for _, l := range consoleLines {
		if strings.HasPrefix(strings.ToLower(l), "error ") {
			t.Errorf("browser console error: %s", l)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
	if strings.Contains(p.stderr.String(), "panic") {
		t.Fatalf("server logged a panic:%s", diag())
	}
}
