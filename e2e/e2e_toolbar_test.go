//go:build browser

// End-to-end coverage for the desktop toolbar buttons and the file header's
// view controls, which replaced the "View ▾" dropdown.
//
// Run with: go test -tags=browser -run Toolbar ./e2e/...

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestE2E_ToolbarButtons(t *testing.T) {
	repo := setupFixtureRepo(t)
	// Seed one file-level comment so the Annotations button has a count.
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,0,0,,seeded,2026-06-30T12:00:00Z,false,,,file,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	// --agent: the widest toolbar (Queue joins it), which is what must fit.
	p := bootChromeAgainstRepo(t, repo, 1400, 900, "--agent")

	var consoleLines, wsFrames []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
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

	p.waitReadyAt(1400, 900)
	// A file open => .CurrentDiff is set, which most toolbar controls gate on.
	p.clickFile("edited.go")

	diag := func() string {
		return "\n--- server ---\n" + p.stderr.String() +
			"\n--- console ---\n" + strings.Join(consoleLines, "\n") +
			"\n--- ws ---\n" + strings.Join(wsFrames, "\n")
	}
	evalBool := func(js string) bool {
		var v bool
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("eval %q: %v%s", js, err, diag())
		}
		return v
	}
	evalStr := func(js string) string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("eval %q: %v%s", js, err, diag())
		}
		return v
	}
	evalInt := func(js string) int {
		var v int
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("eval %q: %v%s", js, err, diag())
		}
		return v
	}
	clickJS := func(sel string) {
		t.Helper()
		js := fmt.Sprintf(`(()=>{const el=document.querySelector(%q); if(!el) return false; el.click(); return true;})()`, sel)
		if !evalBool(js) {
			t.Fatalf("click %s: element not found%s", sel, diag())
		}
	}
	dataScheme := func() string {
		return evalStr(`document.querySelector('.theme-root')?.getAttribute('data-scheme') || ''`)
	}

	// 1. No View dropdown: the review-wide controls are toolbar buttons.
	if evalBool(`!!document.querySelector('[aria-label="View options"]')`) {
		t.Fatalf("the View dropdown should be gone%s", diag())
	}
	for _, name := range []string{"openSearch", "toggleFocusMode", "toggleCommentList", "toggleUnderstand", "cycleScheme", "cycleTheme", "toggleKeyboardHelp"} {
		if !evalBool(fmt.Sprintf(`!!document.querySelector('header.bar button[name=%q]')`, name)) {
			t.Errorf("toolbar missing %q%s", name, diag())
		}
	}
	if !evalBool(`!!document.querySelector('header.bar a[href="/_usage"]')`) {
		t.Errorf("toolbar missing the Guide link%s", diag())
	}
	// The file counts moved to the sidebar.
	if evalBool(`document.querySelector('header.bar').textContent.includes('viewed')`) {
		t.Errorf("the toolbar still carries the file counts%s", diag())
	}
	if got := evalStr(`(()=>{const e=document.querySelector('#files-drawer .drawer-stats');return e&&e.offsetParent!==null?e.textContent:''})()`); !strings.Contains(got, "of") || !strings.Contains(got, "viewed") {
		t.Errorf("sidebar counts on screen = %q, want 'N of M viewed'%s", got, diag())
	}

	// 2. One row, nothing cropped, at full desktop width and at the two
	//    breakpoints where words, then keys, drop.
	assertNoCrop := func(label string) {
		t.Helper()
		if h := evalInt(`document.querySelector('header.bar').offsetHeight`); h > 70 {
			t.Fatalf("%s: toolbar wrapped (height=%dpx)%s", label, h, diag())
		}
		if evalBool(`(()=>{const w=window.innerWidth;return [...document.querySelectorAll('header.bar button, header.bar a')].some(b=>b.getBoundingClientRect().right > w + 1);})()`) {
			t.Fatalf("%s: a toolbar control extends past the right edge%s", label, diag())
		}
	}
	assertNoCrop("1400px")
	for _, w := range []int64{1280, 1000} {
		if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(w, 860), chromedp.Sleep(200*time.Millisecond)); err != nil {
			t.Fatalf("resize to %d: %v%s", w, err, diag())
		}
		assertNoCrop(fmt.Sprintf("%dpx", w))
	}
	if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(1400, 900), chromedp.Sleep(150*time.Millisecond)); err != nil {
		t.Fatalf("resize back: %v%s", err, diag())
	}

	// 3. A toolbar button acts: cycleScheme changes the scheme.
	before := dataScheme()
	clickJS(`header.bar button[name="cycleScheme"]`)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && dataScheme() == before {
		time.Sleep(40 * time.Millisecond)
	}
	if dataScheme() == before {
		t.Fatalf("cycleScheme did not change data-scheme from %q%s", before, diag())
	}

	// 4. With an agent, Understand is enabled: it is how you ask for a
	//    walkthrough. (TestE2E_ToolbarUnderstandDisabled covers no agent.)
	if evalBool(`document.querySelector('header.bar button[name="toggleUnderstand"]').disabled`) {
		t.Errorf("Understand should be enabled when an agent can make a walkthrough%s", diag())
	}

	// 5. The file header holds this file's view controls, all six always shown;
	//    on a Go file, Preview | Raw and Annotate can't apply, so they're disabled
	//    with a reason, and Diff is on.
	for _, sel := range []string{
		`.file-head-view input[value="rendered"] ~ button`, `.file-head-view input[value="raw"] ~ button`,
		`.file-head-view button[name="toggleFileView"]`, `.file-head-view button[name="toggleHideWhitespace"]`,
		`.file-head-view button[name="toggleFoldMechanical"]`, `.file-head-view .region-toggle button`,
		`.file-head-view button[name="toggleBoxes"]`,
	} {
		if !evalBool(fmt.Sprintf(`!!document.querySelector(%q)`, sel)) {
			t.Errorf("file header missing %s%s", sel, diag())
		}
	}
	for _, sel := range []string{`.file-head-view input[value="raw"] ~ button`, `.file-head-view .region-toggle button`} {
		if !evalBool(fmt.Sprintf(`(()=>{const b=document.querySelector(%q);const r=b&&b.getBoundingClientRect();const hit=r&&document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return !!b && b.disabled && b.closest('form').title.length>0 && hit===b.closest('form')})()`, sel)) {
			t.Errorf("%s should be disabled on a Go file, with the hover reaching its form's title%s", sel, diag())
		}
	}
	if got := evalStr(`document.querySelector('.file-head-view button[name="toggleFileView"]').getAttribute('aria-pressed')`); got != "true" {
		t.Errorf("Diff aria-pressed = %q, want true by default%s", got, diag())
	}
	clickJS(`.file-head-view button[name="toggleFileView"]`)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && evalStr(`document.querySelector('.file-head-view button[name="toggleFileView"]').getAttribute('aria-pressed')`) != "false" {
		time.Sleep(40 * time.Millisecond)
	}
	if got := evalStr(`document.querySelector('.file-head-view button[name="toggleFileView"]').getAttribute('aria-pressed')`); got != "false" {
		t.Errorf("Diff should turn off on click, aria-pressed = %q%s", got, diag())
	}

	t.Logf("toolbar verified: %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
}

// With no walkthrough, quiz or agent, Understand stays in place, disabled, with
// a title saying why.
func TestE2E_ToolbarUnderstandDisabled(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1400, 900)
	p.waitReadyAt(1400, 900)
	p.clickFile("edited.go")
	var disabled, hoverReachesTitle bool
	var title string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('header.bar button[name="toggleUnderstand"]').disabled`, &disabled),
		chromedp.Evaluate(`document.querySelector('header.bar button[name="toggleUnderstand"]').closest('form').title`, &title),
		// A disabled button gets no hover in Safari or Firefox, so the pointer
		// must land on the form that carries the tooltip.
		chromedp.Evaluate(`(()=>{const b=document.querySelector('header.bar button[name="toggleUnderstand"]');const r=b.getBoundingClientRect();return document.elementFromPoint(r.x+r.width/2,r.y+r.height/2)===b.closest('form')})()`, &hoverReachesTitle),
	); err != nil {
		t.Fatalf("read Understand: %v\nstderr: %s", err, p.stderr.String())
	}
	if !disabled || !strings.Contains(title, "no agent") || !hoverReachesTitle {
		t.Errorf("Understand disabled=%v title=%q hoverReachesTitle=%v, want disabled with a reason on hover\nstderr: %s", disabled, title, hoverReachesTitle, p.stderr.String())
	}
}

// TestE2E_ToolbarOneRowWithCounts: at every desktop breakpoint the toolbar
// stays on one row when its buttons carry counts: Annotations with open and
// resolved ("1 +1"), and the Queue with what's queued. The docs capture caught
// the Queue wrapping to a second row at 1280px.
func TestE2E_ToolbarOneRowWithCounts(t *testing.T) {
	repo := setupFixtureRepo(t)
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,0,0,,settled,2026-06-30T12:00:00Z,true,,,file,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	p := bootChromeAgainstRepo(t, repo, 1280, 860, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(1280, 860)
	p.clickFile("edited.go")
	addLineComment(t, p, 0, 4, "a queued comment")
	waitJSDiag(t, p, `/\d/.test(document.querySelector('header.bar .queue-trigger').textContent)`, "the Queue shows a count", diag)
	waitJSDiag(t, p, `/\+\s*1/.test(document.querySelector('header.bar button[name="toggleCommentList"]').textContent)`, "Annotations shows open and resolved", diag)
	// Every desktop breakpoint, from where words drop to where they all show.
	for _, w := range []int64{900, 1219, 1220, 1359, 1360, 1399, 1400, 1439, 1440, 1599, 1600, 1920} {
		if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(w, 860), chromedp.Sleep(150*time.Millisecond)); err != nil {
			t.Fatalf("resize to %d: %v%s", w, err, diag())
		}
		if h := evalInt(t, p, `document.querySelector('header.bar').offsetHeight`); h > 70 {
			t.Errorf("%dpx: toolbar wrapped with counts (height=%dpx)", w, h)
		}
		if evalInt(t, p, `(()=>{const w=window.innerWidth;return [...document.querySelectorAll('header.bar button, header.bar a')].filter(b=>b.getBoundingClientRect().right > w + 1).length})()`) != 0 {
			t.Errorf("%dpx: a toolbar control extends past the right edge", w)
		}
	}
}
