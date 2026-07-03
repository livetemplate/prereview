//go:build browser

// End-to-end coverage for issue #91: the cmd+k search palette. Boots prereview
// against a fixture with (a) a content match deep in an UNCHANGED region (folded
// away in diff view), (b) a Markdown match (rendered as blocks by default), and
// (c) an UNCHANGED file (outside the default changed scope). Asserts: the toolbar
// button + the Ctrl+K chord open the palette; typing lists hits; clicking a
// folded content hit reveals the full file and lands is-cursor on the exact line;
// a Markdown hit falls to the raw line view; the scope toggle reaches unchanged
// files; a filename hit opens the file; Esc closes.
//
// Run with: go test -tags=browser -run TestE2E_Search ./e2e/...

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// setupSearchRepo seeds a git repo where the needles sit in specific places:
//   - app.go: "UNCHANGEDNEEDLE" on a line far from the only change, so diff view
//     folds it (tests the full-file reveal on jump).
//   - notes.md: "MARKDOWNNEEDLE" in a changed Markdown file (tests raw-view reveal).
//   - legacy.go: "LEGACYNEEDLE" in an UNCHANGED file (tests the All-files scope).
func setupSearchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	// app.go: 24 lines; the needle is on line 12, changes will happen on line 1.
	appLines := []string{"package app", ""}
	for i := 0; i < 22; i++ {
		if i == 10 {
			appLines = append(appLines, "// UNCHANGEDNEEDLE lives deep in an unchanged region")
		} else {
			appLines = append(appLines, fmt.Sprintf("// filler line %d", i))
		}
	}
	mustWrite(t, dir, "app.go", strings.Join(appLines, "\n")+"\n")
	mustWrite(t, dir, "notes.md", "# Notes\n\nnothing special yet\n")
	mustWrite(t, dir, "legacy.go", "package legacy\n\n// LEGACYNEEDLE never changes\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	// Change ONLY app.go line 1 (far from the line-12 needle → it stays folded)
	// and notes.md (so it's in the changed set); legacy.go is left untouched.
	appLines[0] = "package app // touched"
	mustWrite(t, dir, "app.go", strings.Join(appLines, "\n")+"\n")
	mustWrite(t, dir, "notes.md", "# Notes\n\na MARKDOWNNEEDLE appears here\n")
	return dir
}

func TestE2E_SearchPalette(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSearchRepo(t), 1200, 800)
	p.waitReady()

	openViaButton := func() {
		// Open only if closed — clicking the toolbar button while the modal's
		// full-screen backdrop is up would land on the backdrop (closeSearch).
		var open bool
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`!!document.querySelector('.search-modal.is-open')`, &open))
		if !open {
			if err := chromedp.Run(p.ctx, chromedp.Click(`header.bar button[name="openSearch"]`, chromedp.ByQuery)); err != nil {
				t.Fatalf("open search: %v\nstderr: %s", err, p.stderr.String())
			}
		}
		if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.search-modal.is-open input[name="q"]`, chromedp.ByQuery)); err != nil {
			t.Fatalf("palette input not visible: %v\nstderr: %s", err, p.stderr.String())
		}
	}
	typeQuery := func(q string) {
		// Clear then type; wait past the 200ms input debounce for the re-render.
		if err := chromedp.Run(p.ctx,
			chromedp.Evaluate(`(()=>{const i=document.querySelector('.search-modal input[name="q"]');i.value='';})()`, nil),
			chromedp.SendKeys(`.search-modal input[name="q"]`, q, chromedp.ByQuery),
			chromedp.Sleep(450*time.Millisecond),
		); err != nil {
			t.Fatalf("type %q: %v", q, err)
		}
	}
	hitCount := func() int {
		var n int
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelectorAll('.search-hit').length`, &n))
		return n
	}

	// --- Open via the toolbar button; the input should autofocus. ---
	openViaButton()
	var focused bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`document.activeElement === document.querySelector('.search-modal input[name="q"]')`, &focused)); err != nil {
		t.Fatal(err)
	}
	if !focused {
		t.Error("palette input should autofocus on open")
	}

	// --- Content hit in a FOLDED region → reveal full file, land is-cursor. ---
	typeQuery("UNCHANGEDNEEDLE")
	if hitCount() == 0 {
		t.Fatalf("no hits for UNCHANGEDNEEDLE\nstderr: %s", p.stderr.String())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.search-hit`, chromedp.ByQuery),
		chromedp.WaitVisible(`.code .line.is-cursor`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("jump to folded content hit: %v\nstderr: %s", err, p.stderr.String())
	}
	var cursorText string
	if err := chromedp.Run(p.ctx, chromedp.Text(`.code .line.is-cursor`, &cursorText, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cursorText, "UNCHANGEDNEEDLE") {
		t.Errorf("cursor line should be the matched line, got %q", cursorText)
	}

	// --- Markdown hit → raw line view (not .md-view blocks). ---
	openViaButton()
	typeQuery("MARKDOWNNEEDLE")
	if hitCount() == 0 {
		t.Fatalf("no hits for MARKDOWNNEEDLE")
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.search-hit`, chromedp.ByQuery),
		chromedp.WaitVisible(`.code .line.is-cursor`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("jump to markdown hit: %v\nstderr: %s", err, p.stderr.String())
	}
	var hasMdView bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`!!document.querySelector('.md-view')`, &hasMdView)); err != nil {
		t.Fatal(err)
	}
	if hasMdView {
		t.Error("a revealed Markdown file must show raw .code lines, not .md-view")
	}

	// --- Scope: an UNCHANGED file is out of the default changed scope; All finds it. ---
	openViaButton()
	typeQuery("LEGACYNEEDLE")
	if hitCount() != 0 {
		t.Errorf("changed scope should not find the unchanged legacy.go, got %d hits", hitCount())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.search-modal button[name="toggleSearchScope"]`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("toggle scope: %v", err)
	}
	if hitCount() == 0 {
		t.Errorf("all-files scope should find legacy.go's LEGACYNEEDLE")
	}

	// --- Filename hit opens the file. ---
	openViaButton()
	typeQuery("legacy.go")
	var fileHitFile string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`(document.querySelector('.search-item.is-file .search-where code')||{}).textContent||""`, &fileHitFile)); err != nil {
		t.Fatal(err)
	}
	if fileHitFile != "legacy.go" {
		t.Errorf("filename hit should list legacy.go, got %q", fileHitFile)
	}

	// --- Esc closes the palette. ---
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent(""), // Escape
		chromedp.WaitNotPresent(`.search-modal.is-open`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Esc should close the palette: %v", err)
	}

	// --- Ctrl+K opens it (the documented chord). ---
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`window.dispatchEvent(new KeyboardEvent('keydown',{key:'k',ctrlKey:true,bubbles:true,cancelable:true}))`, nil),
		chromedp.WaitVisible(`.search-modal.is-open`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Ctrl+K should open the palette: %v\nstderr: %s", err, p.stderr.String())
	}
}
