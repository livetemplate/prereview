//go:build browser

// End-to-end coverage for the toolbar "View ▾" dropdown (the overflow grouping
// that keeps the desktop bar from cropping). The dropdown uses the livetemplate
// client primitive pattern from the docs — lvt-el:toggleClass:on:click="open" to
// open + lvt-el:removeClass:on:click-away="open" to close — so there is NO server
// state and NO custom JS.
//
// The load-bearing assertion is "scheme cycles": clicking a panel item both
// closes the panel (the wrapper's toggleClass removes `.open`, display:none-ing
// the panel) AND submits the item's form. The form is display:none, not
// detached, so the submit must still fire. If it didn't, the whole client
// pattern would be unusable and we'd pivot to the server-driven .more-menu.
//
// Run with: go test -tags=browser -run ToolbarViewDropdown ./e2e/...

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

func TestE2E_ToolbarViewDropdown(t *testing.T) {
	repo := setupFixtureRepo(t)
	// Seed one file-level comment so the dropdown's "All comments (N)" item
	// renders (gated on len(.Comments) > 0), letting the panel-contents check
	// below cover it alongside the always-present items.
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,0,0,,seeded,2026-06-30T12:00:00Z,false,,,file,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	p := bootChromeAgainstRepo(t, repo, 1400, 900)

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
	isOpen := func() bool {
		return evalBool(`!!document.querySelector('.tb-dropdown')?.classList.contains('open')`)
	}
	panelShown := func() bool {
		return evalBool(`(()=>{const p=document.querySelector('.tb-dropdown-panel'); return !!p && getComputedStyle(p).display!=='none';})()`)
	}
	dataScheme := func() string {
		return evalStr(`document.querySelector('.theme-root')?.getAttribute('data-scheme') || ''`)
	}
	waitClosed := func(what string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if !isOpen() {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("%s: dropdown still open%s", what, diag())
	}

	// 1. The dropdown exists and starts closed.
	if !evalBool(`!!document.querySelector('.tb-dropdown')`) {
		t.Fatalf("no .tb-dropdown in toolbar%s", diag())
	}
	if isOpen() || panelShown() {
		t.Fatalf("dropdown should start closed (open=%v shown=%v)%s", isOpen(), panelShown(), diag())
	}

	// No crop — the whole point of the grouping. The resting toolbar must
	// (a) stay on a single row (a wrap would roughly double the ~43px bar
	// height) and (b) keep every control fully within the viewport (no button's
	// right edge past innerWidth). Hidden panel items (display:none) report a
	// zero rect, so they don't trip the right-edge check.
	assertNoCrop := func(label string) {
		t.Helper()
		if h := evalInt(`document.querySelector('header.bar').offsetHeight`); h > 70 {
			t.Fatalf("%s: toolbar wrapped (height=%dpx) — inline set overflows one row%s", label, h, diag())
		}
		if evalBool(`(()=>{const w=window.innerWidth;return [...document.querySelectorAll('header.bar button')].some(b=>b.getBoundingClientRect().right > w + 1);})()`) {
			t.Fatalf("%s: a toolbar control extends past the viewport right edge (crop)%s", label, diag())
		}
	}

	// 1b. Check the resting desktop width…
	assertNoCrop("1400px")
	// 1c. …and — critically — at ~1000px, the iPad-ish "between 900 and full-fit"
	//     band where the OLD always-inline set (file-count + Preview|Raw + Focus +
	//     scheme + mode + help + Comment on file + Quit) cropped
	//     off the right (the motivating screenshot). The grouped set must fit one
	//     row here with no clipped control; this is where the assertion has teeth.
	if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(1000, 860), chromedp.Sleep(200*time.Millisecond)); err != nil {
		t.Fatalf("resize to 1000px: %v%s", err, diag())
	}
	assertNoCrop("1000px")
	// Back to a comfortable width for the interaction steps below.
	if err := chromedp.Run(p.ctx, chromedp.EmulateViewport(1400, 900), chromedp.Sleep(150*time.Millisecond)); err != nil {
		t.Fatalf("resize back to 1400px: %v%s", err, diag())
	}

	// 2. Clicking the trigger opens it (pure client toggle, no POST).
	clickJS(`.tb-dropdown-trigger`)
	time.Sleep(150 * time.Millisecond)
	if !isOpen() || !panelShown() {
		t.Fatalf("after trigger click, expected open+shown (open=%v shown=%v)%s", isOpen(), panelShown(), diag())
	}

	// 2b. The panel holds the moved controls — including "All comments"
	//     (toggleCommentList), which renders because we seeded a comment above.
	//     The resolved-only / outdated items are covered by TestE2E_ResolveComment.
	for _, name := range []string{"toggleFocusMode", "cycleScheme", "cycleTheme", "toggleCommentList", "toggleKeyboardHelp"} {
		if !evalBool(fmt.Sprintf(`!!document.querySelector('.tb-dropdown-panel button[name=%q]')`, name)) {
			t.Fatalf("View panel missing %q item%s", name, diag())
		}
	}

	// 3. THE MAKE-OR-BREAK: click the cycleScheme item inside the panel. The same
	//    click closes the panel (toggleClass removes .open) — the item's form is
	//    now display:none. Assert (a) the scheme actually changed => the POST
	//    still fired, and (b) the dropdown closed.
	before := dataScheme()
	clickJS(`.tb-dropdown-panel button[name="cycleScheme"]`)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && dataScheme() == before {
		time.Sleep(40 * time.Millisecond)
	}
	if got := dataScheme(); got == before {
		t.Fatalf("cycleScheme did NOT fire from inside the closing panel: data-scheme stayed %q "+
			"(client lvt-el pattern unusable — pivot to server-driven .more-menu)%s", got, diag())
	}
	waitClosed("after item click")

	// 4. Click-away closes it: reopen, then click a neutral spot outside the
	//    dropdown (the filename title). The document-level click-away listener
	//    must remove .open even though no server round-trip occurs.
	clickJS(`.tb-dropdown-trigger`)
	time.Sleep(150 * time.Millisecond)
	if !isOpen() {
		t.Fatalf("reopen after morph failed%s", diag())
	}
	clickJS(`header.bar .title`)
	waitClosed("after click-away")

	t.Logf("View dropdown verified: open/close + item-action-fires-while-closing + click-away; "+
		"scheme %s, %d console lines, %d ws frames", dataScheme(), len(consoleLines), len(wsFrames))
}
