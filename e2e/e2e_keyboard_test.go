//go:build browser

package e2e

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// TestE2E_KeyboardShortcuts exercises the keyboard layer end-to-end:
//   - j/k and ArrowDown/ArrowUp navigate between files;
//   - "c" opens the file composer and focus lands in the textarea (autofocus);
//   - typing shortcut letters into the composer does NOT navigate (the
//     lvt-mod:skip-when-typing guard) yet Esc still cancels mid-typing;
//   - "?" and the toolbar button open the help overlay listing every binding.
//
// Captures all four debug signals (browser console, server stderr, WebSocket
// frames, rendered HTML) per the project's e2e contract.
func TestE2E_KeyboardShortcuts(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)

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
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.waitReady()

	// step runs CDP actions under a short timeout and logs a marker, so a
	// stuck Wait* fails fast with diagnostics (and a visible last-step) instead
	// of blocking until the whole-test timeout.
	step := func(label string, actions ...chromedp.Action) {
		t.Logf("step: %s", label)
		ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
		defer cancel()
		if err := chromedp.Run(ctx, actions...); err != nil {
			t.Fatalf("%s: %v%s", label, err, diag())
		}
	}

	// currentFile reads the bar title (.title-file = CurrentDiff.Path). Uses
	// textContent (not chromedp.Text/innerText, which can return "" depending
	// on layout) so it reflects the DOM regardless of rendering quirks.
	currentFile := func() string {
		var f string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`(document.querySelector('header.bar .title-file')||{}).textContent||''`, &f))
		return strings.TrimSpace(f)
	}
	// pressAndWaitFileChange dispatches a key and polls until the selected
	// file differs from prev (the nav round-trips over WS + morphdom).
	pressAndWaitFileChange := func(key, prev string) string {
		if err := chromedp.Run(p.ctx, chromedp.KeyEvent(key)); err != nil {
			t.Fatalf("key %q: %v%s", key, err, diag())
		}
		for i := 0; i < 40; i++ {
			if cur := currentFile(); cur != "" && cur != prev {
				return cur
			}
			time.Sleep(75 * time.Millisecond)
		}
		t.Fatalf("file did not change after key %q (still %q)%s", key, prev, diag())
		return ""
	}

	f0 := currentFile()
	if f0 == "" {
		t.Fatalf("no file selected after load%s", diag())
	}

	// --- j / k navigation ---
	fJ := pressAndWaitFileChange("j", f0)
	fK := pressAndWaitFileChange("k", fJ)
	if fK != f0 {
		t.Errorf("k did not return to the original file: start=%q after j=%q after k=%q", f0, fJ, fK)
	}

	// --- ArrowDown / ArrowUp navigation (the user chose both schemes) ---
	fDown := pressAndWaitFileChange(kb.ArrowDown, f0)
	fUp := pressAndWaitFileChange(kb.ArrowUp, fDown)
	if fUp != f0 {
		t.Errorf("ArrowUp did not return to the original file: start=%q down=%q up=%q", f0, fDown, fUp)
	}

	// --- "c" opens the file composer and focus lands in the textarea ---
	beforeComposer := currentFile()
	step(`press "c" → composer opens`,
		chromedp.KeyEvent("c"),
		chromedp.WaitVisible(`.composer textarea[name="body"]`, chromedp.ByQuery),
	)
	var activeTag string
	for i := 0; i < 20; i++ { // autofocus runs in a rAF after the patch
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.activeElement && document.activeElement.tagName`, &activeTag))
		if activeTag == "TEXTAREA" {
			break
		}
		time.Sleep(75 * time.Millisecond)
	}
	if activeTag != "TEXTAREA" {
		t.Errorf("composer should autofocus the textarea, but activeElement is %q%s", activeTag, diag())
	}

	// --- typing shortcut letters into the composer must NOT navigate ---
	// Type letters that are all bound shortcuts (j, k, n, f, a, r); the guard
	// must let them land as text instead of firing navigation.
	step("type shortcut letters into composer", chromedp.KeyEvent("jknfar typed"))
	time.Sleep(300 * time.Millisecond)
	if got := currentFile(); got != beforeComposer {
		t.Errorf("typing shortcut letters navigated (guard failed): file went %q -> %q%s", beforeComposer, got, diag())
	}
	var draft string
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.composer textarea[name="body"]').value`, &draft))
	if !strings.Contains(draft, "jknfar typed") {
		t.Errorf("composer textarea should contain the typed text, got %q%s", draft, diag())
	}

	// --- Esc cancels the composer even while focus is in the textarea ---
	// (Esc is the un-guarded <body> binding, so it must fire mid-typing.)
	step("Esc closes composer mid-typing",
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`.composer textarea[name="body"]`, chromedp.ByQuery),
	)

	// --- "?" opens the help overlay listing every binding ---
	step(`press "?" → help overlay opens`,
		chromedp.KeyEvent("?"),
		chromedp.WaitVisible(`.kbd-help-modal.is-open`, chromedp.ByQuery),
	)
	var rowCount int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelectorAll('.kbd-help-modal .kbd-row').length`, &rowCount))
	if rowCount < 10 {
		t.Errorf("help overlay should list every binding, got %d rows%s", rowCount, diag())
	}

	// --- Esc closes the help overlay ---
	step("Esc closes help overlay",
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`.kbd-help-modal.is-open`, chromedp.ByQuery),
	)

	// --- the toolbar button opens the help overlay too ---
	step("toolbar button opens help overlay",
		chromedp.Click(`header.bar button[name="toggleKeyboardHelp"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.kbd-help-modal.is-open`, chromedp.ByQuery),
	)

	mu.Lock()
	for _, l := range consoleLines {
		if strings.Contains(strings.ToLower(l), "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
}

// TestE2E_KeyboardMarkdownBlock pins the Phase-3 focusability fix: a rendered
// markdown block (the .md-rendered div, which can't be a native <button>) is
// reachable by keyboard (tabindex) and activatable with Enter to open the
// composer — the keyboard equivalent of clicking the block. Captures all four
// debug signals.
func TestE2E_KeyboardMarkdownBlock(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureGFMRepo(t), 1200, 800)

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
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	// step runs CDP actions under a short timeout so a stuck Wait* fails fast
	// with diagnostics instead of blocking until the whole-test timeout.
	step := func(label string, actions ...chromedp.Action) {
		ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
		defer cancel()
		if err := chromedp.Run(ctx, actions...); err != nil {
			t.Fatalf("%s: %v%s", label, err, diag())
		}
	}

	p.waitReady()
	p.clickFile("gfm.md")

	step("markdown should render blocks",
		chromedp.WaitVisible(`.md-rendered`, chromedp.ByQuery),
	)

	// The block must be keyboard-focusable.
	var tabIndex int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.md-rendered').tabIndex`, &tabIndex))
	if tabIndex != 0 {
		t.Errorf("rendered markdown block must be focusable (tabindex 0), got %d%s", tabIndex, diag())
	}

	// Focus the first block and activate with Enter → composer opens.
	var focused bool
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`(()=>{const b=document.querySelector('.md-rendered');if(!b)return false;b.focus();return document.activeElement===b})()`,
		&focused))
	if !focused {
		t.Fatalf("could not focus the markdown block%s", diag())
	}
	step("Enter on a focused markdown block should open the composer",
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(`.composer textarea[name="body"]`, chromedp.ByQuery),
	)

	mu.Lock()
	for _, l := range consoleLines {
		if strings.Contains(strings.ToLower(l), "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
}
