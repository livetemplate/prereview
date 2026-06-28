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
//   - j/k navigate between files (arrows are the line cursor — see
//     TestE2E_KeyboardLineCursor);
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

	// --- j / k switch files (arrows are the line cursor — see
	// TestE2E_KeyboardLineCursor) ---
	fJ := pressAndWaitFileChange("j", f0)
	fK := pressAndWaitFileChange("k", fJ)
	if fK != f0 {
		t.Errorf("k did not return to the original file: start=%q after j=%q after k=%q", f0, fJ, fK)
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

// TestE2E_KeyboardCommentLifecycle proves the core of issue #28: a reviewer can
// author AND save a comment with only the keyboard, then act on it. "c" opens
// the file composer (textarea autofocused), the body is typed, focus Tabs to
// the Save button and Enter submits, and the comment persists to CSV. It then
// resolves the saved comment from the keyboard. Records where focus lands after
// the composer closes (a known ergonomics watch-point). All four debug signals.
func TestE2E_KeyboardCommentLifecycle(t *testing.T) {
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
	step := func(label string, actions ...chromedp.Action) {
		t.Logf("step: %s", label)
		ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
		defer cancel()
		if err := chromedp.Run(ctx, actions...); err != nil {
			t.Fatalf("%s: %v%s", label, err, diag())
		}
	}

	p.waitReady()

	// Open the file composer and type a comment — all from the keyboard.
	step(`press "c" → composer opens`,
		chromedp.KeyEvent("c"),
		chromedp.WaitVisible(`.composer textarea[name="body"]`, chromedp.ByQuery),
	)
	var activeTag string
	for i := 0; i < 20; i++ {
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.activeElement && document.activeElement.tagName`, &activeTag))
		if activeTag == "TEXTAREA" {
			break
		}
		time.Sleep(75 * time.Millisecond)
	}
	if activeTag != "TEXTAREA" {
		t.Fatalf("composer should autofocus the textarea, got %q%s", activeTag, diag())
	}
	step("type comment body", chromedp.KeyEvent("looks good via keyboard"))

	// Tab to the Save button (textarea → Cancel → Save) and submit with Enter.
	var onSave bool
	for i := 0; i < 8; i++ {
		_ = chromedp.Run(p.ctx, chromedp.KeyEvent(kb.Tab))
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`document.activeElement && document.activeElement.getAttribute('name') === 'addComment'`, &onSave))
		if onSave {
			break
		}
	}
	if !onSave {
		t.Fatalf("Save button not reachable by Tab from the composer textarea%s", diag())
	}
	step("Enter on Save submits the comment",
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitNotPresent(`.composer textarea[name="body"]`, chromedp.ByQuery),
	)

	// The comment must persist (CSV is the source of truth).
	rows := p.readCSV()
	found := false
	for _, r := range rows {
		for _, c := range r {
			if strings.Contains(c, "looks good via keyboard") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("saved comment not found in CSV after keyboard save%s", diag())
	}

	// Focus-on-close: after the composer closes, focus lands ON the saved
	// comment card (not <body>), so the keyboard user is positioned at their
	// comment. autofocus runs in a rAF after the patch, so poll.
	onComment := false
	for i := 0; i < 20; i++ {
		var ok bool
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`!!(document.activeElement && document.activeElement.closest('.inline-comment'))`, &ok))
		if ok {
			onComment = true
			break
		}
		time.Sleep(75 * time.Millisecond)
	}
	if !onComment {
		var where string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.activeElement?document.activeElement.tagName:'none'`, &where))
		t.Errorf("after save, focus should land on the saved comment, but activeElement is %s%s", where, diag())
	}

	// Resolve the saved comment from the keyboard (native button: focus + Enter).
	var onResolve bool
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`(()=>{const b=document.querySelector('button[name="toggleResolved"]');if(!b)return false;b.focus();return document.activeElement===b})()`,
		&onResolve))
	if !onResolve {
		t.Fatalf("resolve button not focusable after save%s", diag())
	}
	step("Enter on Resolve", chromedp.KeyEvent(kb.Enter))

	// Resolved comments are hidden by default, so assert via CSV (the source of
	// truth) rather than the DOM.
	resolved := false
	for i := 0; i < 40 && !resolved; i++ {
		for _, r := range p.readCSV() {
			isOurs, hasTrue := false, false
			for _, c := range r {
				if strings.Contains(c, "looks good via keyboard") {
					isOurs = true
				}
				if c == "true" {
					hasTrue = true
				}
			}
			if isOurs && hasTrue {
				resolved = true
			}
		}
		if !resolved {
			time.Sleep(75 * time.Millisecond)
		}
	}
	if !resolved {
		t.Errorf("comment not marked resolved in CSV after keyboard Resolve%s", diag())
	}

	mu.Lock()
	for _, l := range consoleLines {
		if strings.Contains(strings.ToLower(l), "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
}

// TestE2E_LineCommentClosesComposerAndNavigates is a regression test for
// livetemplate/prereview#28's "can't switch files after a comment" bug: a LINE
// composer rendered inside the keyed line-list used to linger (focus-trapped)
// after save because lvt-form:preserve kept its form alive, which suppressed
// every skip-when-typing shortcut. The composer now round-trips its draft via
// saveDraft (no preserve), so it closes on save and Esc, freeing the keyboard.
func TestE2E_LineCommentClosesComposerAndNavigates(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	eval := func(js string) string {
		var s string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(js, &s))
		return strings.TrimSpace(s)
	}
	composerPresent := func() bool { return eval(`document.querySelector('.composer textarea[name="body"]')?'y':'n'`) == "y" }
	curFile := func() string { return eval(`(document.querySelector('header.bar .title-file')||{}).textContent||''`) }

	// Open a line composer, type a body in two bursts straddling the debounce
	// window, and confirm the round-trip never clobbers the typed text.
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "first half ", chromedp.ByQuery),
		chromedp.Sleep(450*time.Millisecond), // let the debounced saveDraft round-trip
		chromedp.SendKeys(`.composer textarea`, "second half", chromedp.ByQuery),
		chromedp.Sleep(450*time.Millisecond),
	); err != nil {
		t.Fatalf("type line comment: %v", err)
	}
	if v := eval(`document.querySelector('.composer textarea[name="body"]').value`); v != "first half second half" {
		t.Errorf("draft clobbered by round-trip: got %q", v)
	}

	// Save → composer must close and focus must leave the textarea.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("save line comment: %v", err)
	}
	if composerPresent() {
		t.Errorf("composer must close after saving a line comment (the #28 focus trap)")
	}
	if eval(`document.activeElement?document.activeElement.tagName:'?'`) == "TEXTAREA" {
		t.Errorf("focus must leave the textarea after save")
	}

	// Let focus settle on the saved comment (autofocus runs in a rAF) before
	// driving the keyboard, so we're not racing the focus move.
	for i := 0; i < 20; i++ {
		var onComment bool
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`!!(document.activeElement && document.activeElement.closest('.inline-comment'))`, &onComment))
		if onComment {
			break
		}
		time.Sleep(75 * time.Millisecond)
	}

	// The keyboard must work again: j switches files. Re-press each round (a
	// single press can be lost while focus/scroll is still settling).
	f0 := curFile()
	switched := false
	for i := 0; i < 20 && !switched; i++ {
		_ = chromedp.Run(p.ctx, chromedp.KeyEvent("j"))
		for j := 0; j < 6 && !switched; j++ {
			if c := curFile(); c != f0 && c != "" {
				switched = true
			} else {
				time.Sleep(75 * time.Millisecond)
			}
		}
	}
	if !switched {
		t.Errorf("j did not switch files after a line comment (still %q)", f0)
	}
}

// TestE2E_ShowResolvedFlash pins that pressing "r" with no resolved comments
// shows a flash toast instead of doing nothing (livetemplate/prereview#28 follow-up).
func TestE2E_ShowResolvedFlash(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent("r"),
		chromedp.WaitVisible(`.flash-toast`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf(`"r" with no resolved comments should show a flash: %v\nstderr: %s`, err, p.stderr.String())
	}
	var msg string
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(document.querySelector('.flash-toast .toast-msg')||{}).textContent||''`, &msg))
	if !strings.Contains(strings.ToLower(msg), "no resolved") {
		t.Errorf("flash text = %q, want a 'no resolved comments' message", msg)
	}
}

// TestE2E_DesktopFilesToggle pins that "f" collapses/expands the file-tree
// sidebar on desktop (previously a no-op there), revealing the hamburger as the
// reopen affordance while collapsed.
func TestE2E_DesktopFilesToggle(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	hidden := func(sel string) bool {
		var v bool
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`(()=>{const e=document.querySelector(`+"`"+sel+"`"+`);return !e || getComputedStyle(e).display==='none'})()`, &v))
		return v
	}
	visible := func(sel string) bool {
		var v bool
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(
			`(()=>{const e=document.querySelector(`+"`"+sel+"`"+`);return !!e && getComputedStyle(e).display!=='none'})()`, &v))
		return v
	}
	if !visible(`#files-drawer`) {
		t.Fatalf("sidebar should be visible on desktop initially")
	}
	// Collapse.
	_ = chromedp.Run(p.ctx, chromedp.KeyEvent("f"))
	collapsed := false
	for i := 0; i < 30; i++ {
		if hidden(`#files-drawer`) && visible(`.hamburger`) {
			collapsed = true
			break
		}
		time.Sleep(75 * time.Millisecond)
	}
	if !collapsed {
		t.Errorf("f should hide the desktop sidebar and reveal the hamburger")
	}
	// Expand again.
	_ = chromedp.Run(p.ctx, chromedp.KeyEvent("f"))
	back := false
	for i := 0; i < 30; i++ {
		if visible(`#files-drawer`) {
			back = true
			break
		}
		time.Sleep(75 * time.Millisecond)
	}
	if !back {
		t.Errorf("f again should restore the desktop sidebar")
	}
}

// TestE2E_KeyboardLineCursor pins the line-cursor flow: ArrowDown/ArrowUp move
// a highlighted, focused cursor through the diff lines, and Enter on the cursor
// line opens the line composer — keyboard-only line commenting.
func TestE2E_KeyboardLineCursor(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")
	eval := func(js string) string {
		var s string
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(js, &s))
		return strings.TrimSpace(s)
	}
	cursorKey := func() string {
		return eval(`(function(){const el=document.querySelector('.code button.line.is-cursor');return el?el.getAttribute('data-key'):'';})()`)
	}

	// First ArrowDown seeds the cursor on the first line and focuses it.
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent(kb.ArrowDown),
		chromedp.WaitVisible(`.code button.line.is-cursor`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("ArrowDown should create a line cursor: %v\nstderr: %s", err, p.stderr.String())
	}
	first := cursorKey()
	if first == "" {
		t.Fatalf("no cursor line after ArrowDown")
	}
	// The cursor line is focused (so Enter activates it).
	if foc := eval(`(document.activeElement && document.activeElement.classList && document.activeElement.classList.contains('is-cursor'))?'y':'n'`); foc != "y" {
		t.Errorf("cursor line should be focused, activeElement=%s", eval(`document.activeElement?document.activeElement.className:'?'`))
	}

	// ArrowDown again moves the cursor to a different line.
	moved := false
	for i := 0; i < 20 && !moved; i++ {
		_ = chromedp.Run(p.ctx, chromedp.KeyEvent(kb.ArrowDown))
		for j := 0; j < 6; j++ {
			if cursorKey() != first && cursorKey() != "" {
				moved = true
				break
			}
			time.Sleep(75 * time.Millisecond)
		}
	}
	if !moved {
		t.Errorf("ArrowDown did not move the cursor off %q", first)
	}

	// Enter on the cursor line opens the LINE composer (not the file composer).
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(`.composer textarea[name="body"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Enter on the cursor line should open the composer: %v\nstderr: %s", err, p.stderr.String())
	}
	if heading := eval(`(document.querySelector('.composer strong')||{}).textContent||''`); !strings.Contains(heading, "Comment on") {
		t.Errorf("composer heading = %q, expected a line comment", heading)
	}
}
