//go:build browser

// End-to-end tests for the GitHub-style file-tree sidebar: nested folders as
// native <details>/<summary>, auto-expand to changed files, durable collapse
// across server re-renders (the upstream lvt-ignore-attrs open fix), and the
// filter → flat list fallback. Captures console + server stderr for diagnosis.

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// setupFixtureRepoTree builds a repo whose changes live in nested directories,
// so the drawer renders a real folder tree (src/app/*, src/lib/*) with sibling
// unchanged paths (docs/, README) that the changed-only scope hides.
func setupFixtureRepoTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	mustWrite(t, dir, "src/app/main.go", "package app\n\nfunc Main() {}\n")
	mustWrite(t, dir, "src/app/util.go", "package app\n\nfunc Util() {}\n")
	mustWrite(t, dir, "src/lib/helper.go", "package lib\n\nfunc Help() {}\n")
	mustWrite(t, dir, "docs/guide.md", "# Guide\n")
	mustWrite(t, dir, "README.md", "# Readme\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	// Mutate two nested files and add one untracked nested file. Changed set:
	// src/app/main.go, src/app/new.go, src/lib/helper.go.
	mustWrite(t, dir, "src/app/main.go", "package app\n\nfunc Main() { println(\"hi\") }\n")
	mustWrite(t, dir, "src/lib/helper.go", "package lib\n\nfunc Help() int { return 1 }\n")
	mustWrite(t, dir, "src/app/new.go", "package app\n\nfunc New() {}\n")
	return dir
}

func TestE2E_FileTree(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoTree(t), 1200, 800)

	var consoleLines []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			consoleLines = append(consoleLines, string(e.Type)+" "+joinArgs(e.Args))
		}
	})

	p.waitReady()

	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`#files-drawer`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() +
			"\n--- console ---\n" + strings.Join(consoleLines, "\n") +
			"\n--- drawer html ---\n" + html
	}

	// 1. Tree renders with folder rows, and changed folders are auto-expanded.
	var hasTree bool
	var appOpen, libOpen bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`#files-drawer ul.file-tree`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('#files-drawer ul.file-tree')`, &hasTree),
		// src/app and src/lib both contain changed files → DefaultOpen.
		chromedp.Evaluate(detailsOpenJS("app"), &appOpen),
		chromedp.Evaluate(detailsOpenJS("lib"), &libOpen),
	); err != nil {
		t.Fatalf("tree render: %v%s", err, diag())
	}
	if !hasTree {
		t.Fatalf("expected ul.file-tree in drawer%s", diag())
	}
	if !appOpen || !libOpen {
		t.Errorf("changed folders should auto-expand: app=%v lib=%v%s", appOpen, libOpen, diag())
	}

	// 2. Nested file buttons are visible because their folders are open.
	var mainVisible bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(fileBtnVisibleJS("main.go"), &mainVisible),
	); err != nil {
		t.Fatalf("nested file query: %v%s", err, diag())
	}
	if !mainVisible {
		t.Errorf("src/app/main.go button should be visible under the auto-expanded folder%s", diag())
	}

	// 3. Collapse the "app" folder by clicking its summary; its files hide.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(clickSummaryJS("app"), nil),
		chromedp.Sleep(150*time.Millisecond),
	); err != nil {
		t.Fatalf("collapse app: %v%s", err, diag())
	}
	var appOpenAfterCollapse, mainVisibleAfterCollapse bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(detailsOpenJS("app"), &appOpenAfterCollapse),
		chromedp.Evaluate(fileBtnVisibleJS("main.go"), &mainVisibleAfterCollapse),
	); err != nil {
		t.Fatalf("post-collapse query: %v%s", err, diag())
	}
	if appOpenAfterCollapse {
		t.Errorf("app folder should be collapsed after clicking its summary%s", diag())
	}
	if mainVisibleAfterCollapse {
		t.Errorf("main.go should be hidden when its folder is collapsed%s", diag())
	}

	// 4. THE DURABILITY TEST: with app collapsed, select a file in ANOTHER
	// folder (a full server re-render). The collapsed state must survive —
	// this exercises the upstream <details lvt-ignore-attrs> open fix.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(clickFileBtnJS("helper.go"), nil),
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='src/lib/helper.go']`, chromedp.BySearch),
	); err != nil {
		t.Fatalf("select helper.go: %v%s", err, diag())
	}
	var appStillCollapsed, libStillOpen bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(detailsOpenJS("app"), &appStillCollapsed),
		chromedp.Evaluate(detailsOpenJS("lib"), &libStillOpen),
	); err != nil {
		t.Fatalf("post-rerender query: %v%s", err, diag())
	}
	if appStillCollapsed {
		t.Errorf("DURABILITY: collapsed 'app' folder re-opened after a server re-render (the upstream fix failed)%s", diag())
	}
	if !libStillOpen {
		t.Errorf("'lib' folder should remain open across the re-render%s", diag())
	}

	// 5. Filter → flat path list (GitHub "Go to file"); clearing restores the tree.
	var hasFlat, hasTreeWhileFiltering bool
	if err := chromedp.Run(p.ctx,
		chromedp.SendKeys(`#files-drawer .drawer-search input`, "helper", chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond), // debounce 200ms + round-trip
		chromedp.Evaluate(`!!document.querySelector('#files-drawer ul.file-flat')`, &hasFlat),
		chromedp.Evaluate(`!!document.querySelector('#files-drawer ul.file-tree')`, &hasTreeWhileFiltering),
	); err != nil {
		t.Fatalf("filter: %v%s", err, diag())
	}
	if !hasFlat {
		t.Errorf("filter active should render the flat ul.file-flat list%s", diag())
	}
	if hasTreeWhileFiltering {
		t.Errorf("tree should be replaced by the flat list while filtering%s", diag())
	}
	// Flat list shows the full path.
	var flatShowsPath bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`Array.from(document.querySelectorAll('#files-drawer .file-flat .file-label')).some(e => e.textContent.includes('src/lib/helper.go'))`, &flatShowsPath),
	); err != nil {
		t.Fatalf("flat path query: %v%s", err, diag())
	}
	if !flatShowsPath {
		t.Errorf("flat list should show full paths like src/lib/helper.go%s", diag())
	}

	var treeBack bool
	if err := chromedp.Run(p.ctx,
		// Clear the filter (select-all + delete) and let it round-trip.
		chromedp.Evaluate(`(()=>{const i=document.querySelector('#files-drawer .drawer-search input');i.value='';i.dispatchEvent(new Event('input',{bubbles:true}));return true})()`, nil),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Evaluate(`!!document.querySelector('#files-drawer ul.file-tree')`, &treeBack),
	); err != nil {
		t.Fatalf("clear filter: %v%s", err, diag())
	}
	if !treeBack {
		t.Errorf("clearing the filter should restore the tree%s", diag())
	}

	for _, l := range consoleLines {
		if strings.HasPrefix(l, "error ") {
			t.Errorf("browser console error: %s", l)
		}
	}
	t.Logf("captured %d console lines", len(consoleLines))
}

// TestE2E_SidebarResize drags the drawer's resize handle and asserts the width
// changes live (via the lvt-fx:resize directive updating --pr-drawer-w on :root)
// and persists across a reload (localStorage). Desktop viewport so the handle
// is shown and the drawer width tracks the variable.
func TestE2E_SidebarResize(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoTree(t), 1200, 800)
	p.waitReady()

	diag := func() string { return "\n--- server ---\n" + p.stderr.String() }

	var handleVisible bool
	var startWidth float64
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`#files-drawer .resize-handle`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('#files-drawer .resize-handle').checkVisibility()`, &handleVisible),
		chromedp.Evaluate(`document.querySelector('#files-drawer').getBoundingClientRect().width`, &startWidth),
	); err != nil {
		t.Fatalf("handle query: %v%s", err, diag())
	}
	if !handleVisible {
		t.Fatalf("resize handle should be visible on desktop%s", diag())
	}

	// Drag the handle to the right by ~120px via synthetic pointer events.
	// (The directive guards setPointerCapture in try/catch, so synthetic
	// events without a real pointer still drive the resize.)
	dragJS := `(() => {
		const h = document.querySelector('#files-drawer .resize-handle');
		const r = h.getBoundingClientRect();
		const x0 = r.left + r.width/2, y0 = r.top + r.height/2;
		const opts = (x) => ({clientX:x, clientY:y0, pointerId:1, button:0, bubbles:true});
		h.dispatchEvent(new PointerEvent('pointerdown', opts(x0)));
		h.dispatchEvent(new PointerEvent('pointermove', opts(x0+120)));
		h.dispatchEvent(new PointerEvent('pointerup', opts(x0+120)));
		return true;
	})()`
	var newWidth float64
	var storedWidth string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(dragJS, nil),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('#files-drawer').getBoundingClientRect().width`, &newWidth),
		chromedp.Evaluate(`localStorage.getItem('prereview.drawerW')`, &storedWidth),
	); err != nil {
		t.Fatalf("drag: %v%s", err, diag())
	}
	if newWidth <= startWidth+40 {
		t.Errorf("drawer should widen by ~120px: start=%.0f new=%.0f%s", startWidth, newWidth, diag())
	}
	if storedWidth == "" {
		t.Errorf("width should be persisted to localStorage after drag%s", diag())
	}

	// Reload — the directive restores the persisted width on arm.
	var reloadedWidth float64
	if err := chromedp.Run(p.ctx,
		chromedp.Reload(),
		chromedp.WaitVisible(`#files-drawer .resize-handle`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('#files-drawer').getBoundingClientRect().width`, &reloadedWidth),
	); err != nil {
		t.Fatalf("reload: %v%s", err, diag())
	}
	if reloadedWidth < newWidth-20 || reloadedWidth > newWidth+20 {
		t.Errorf("persisted width not restored on reload: dragged=%.0f reloaded=%.0f%s", newWidth, reloadedWidth, diag())
	}
}

// --- small JS snippet builders (kept inline to read top-to-bottom) ---

func joinArgs(args []*cdpruntime.RemoteObject) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if a.Value != nil {
			parts = append(parts, string(a.Value))
		} else {
			parts = append(parts, a.Description)
		}
	}
	return strings.Join(parts, " ")
}

// detailsOpenJS reports whether the folder whose summary label equals name is open.
func detailsOpenJS(name string) string {
	return `(()=>{const s=Array.from(document.querySelectorAll('#files-drawer summary.dir-row .tree-label')).find(e=>e.textContent.trim()===` + jsStr(name) + `);return !!(s&&s.closest('details').open)})()`
}

// clickSummaryJS clicks the summary of the folder whose label equals name.
func clickSummaryJS(name string) string {
	return `(()=>{const s=Array.from(document.querySelectorAll('#files-drawer summary.dir-row .tree-label')).find(e=>e.textContent.trim()===` + jsStr(name) + `);if(!s)return false;s.closest('summary').click();return true})()`
}

// fileBtnVisibleJS reports whether a file button whose label contains name is
// actually visible. A closed <details> hides its content via
// content-visibility:hidden on ::details-content, which leaves a stale layout
// rect (getBoundingClientRect lies) — Element.checkVisibility() is the
// purpose-built API that correctly reports skipped content as not visible.
func fileBtnVisibleJS(name string) string {
	return `(()=>{const b=Array.from(document.querySelectorAll('#files-drawer button.file-btn')).find(e=>e.textContent.includes(` + jsStr(name) + `));return !!(b&&b.checkVisibility())})()`
}

// clickFileBtnJS clicks the file button whose label contains name.
func clickFileBtnJS(name string) string {
	return `(()=>{const b=Array.from(document.querySelectorAll('#files-drawer button.file-btn')).find(e=>e.textContent.includes(` + jsStr(name) + `));if(!b)return false;b.click();return true})()`
}

var _ = context.Background
