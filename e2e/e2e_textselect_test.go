//go:build browser

// End-to-end coverage for text (character-range) comments: selecting a word in
// the diff shows a floating "Comment" button, clicking it opens the composer
// scoped to that character range, and saving persists a kind=text comment with
// the exact rune offsets. Also asserts the click guard — while a text selection
// is live, a click on the line must NOT fall through to selectLine (which would
// select the whole line instead).
//
// Run with: go test -tags=browser -run TextSelect ./...

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// mustJSON unmarshals a JSON string produced by an in-page Evaluate into dst.
func mustJSON(t *testing.T, s string, dst any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), dst); err != nil {
		t.Fatalf("unmarshal %q: %v", s, err)
	}
}

// pressKey dispatches a real keydown+keyup for a named key with optional
// modifiers, so contenteditable text navigation (Shift+Arrow) performs its
// native default action — chromedp.KeyEvent can't carry modifiers.
func pressKey(key, code string, vk int64, mods input.Modifier) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		for _, typ := range []input.KeyType{input.KeyRawDown, input.KeyUp} {
			ev := input.DispatchKeyEvent(typ).
				WithModifiers(mods).
				WithKey(key).WithCode(code).
				WithWindowsVirtualKeyCode(vk).WithNativeVirtualKeyCode(vk)
			if err := ev.Do(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

// textSelectDoc is an untracked Go file, so the diff view renders every line as
// an all-add row with new-side line numbers 1..N. Line 3 is
// `func Greet(name string) string {` — we select the word "Greet" (runes
// [5,10): "func " is 5 chars).
const textSelectDoc = "package demo\n\n" +
	"func Greet(name string) string {\n" +
	"\treturn \"hello \" + name\n" +
	"}\n"

func setupFixtureTextSelectRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "keep.go", "package keep\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "demo.go", textSelectDoc)
	return dir
}

func TestE2E_TextSelectComment(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureTextSelectRepo(t), 1200, 800)

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
	p.clickFile("demo.go")

	diag := func(html string) string {
		return "\n--- server ---\n" + p.stderr.String() +
			"\n--- console ---\n" + strings.Join(consoleLines, "\n") +
			"\n--- html ---\n" + html
	}

	// Wait for the diff view, then select the "Greet" token on line 3 via a
	// native Range (this fires selectionchange, the directive's trigger).
	selectJS := `(() => {
		const host = document.querySelector('.code');
		const line = host.querySelector('[data-line="3"] [data-line-text]');
		let target = null;
		for (const s of line.querySelectorAll('span')) {
			if (s.textContent === 'Greet') { target = s; break; }
		}
		if (!target) return 'no Greet token';
		const node = target.firstChild;
		const r = document.createRange();
		r.setStart(node, 0);
		r.setEnd(node, 5);
		const sel = window.getSelection();
		sel.removeAllRanges();
		sel.addRange(r);
		return 'ok';
	})()`

	var selResult string
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.code [data-line="3"] [data-line-text]`, chromedp.ByQuery),
		chromedp.Evaluate(selectJS, &selResult),
		// Debounced selectionchange (150ms) then the floating button appears.
		chromedp.Sleep(400*time.Millisecond),
		chromedp.WaitVisible(`[data-lvt-text-select-button]`, chromedp.ByQuery),
	)
	cancel()
	if err != nil || selResult != "ok" {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`.code`, &html, chromedp.ByQuery))
		t.Fatalf("selecting word / showing Comment button failed: %v (select=%q)%s", err, selResult, diag(html))
	}

	// Guard: while the selection is live, a click on the line must be swallowed
	// so selectLine never fires — no line composer should appear.
	var composerAfterLineClick bool
	guardCtx, guardCancel := context.WithTimeout(p.ctx, 10*time.Second)
	err = chromedp.Run(guardCtx,
		chromedp.Evaluate(`document.querySelector('.code [data-line="3"]').dispatchEvent(new MouseEvent('click',{bubbles:true,cancelable:true}))`, nil),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(`!!document.querySelector('.composer')`, &composerAfterLineClick),
	)
	guardCancel()
	if err != nil {
		t.Fatalf("guard click failed: %v%s", err, diag(""))
	}
	if composerAfterLineClick {
		t.Errorf("click while text selected fell through to selectLine (line composer opened) — guard failed%s", diag(""))
	}

	// Click the floating Comment button → opens the composer scoped to the range.
	var headingText string
	openCtx, openCancel := context.WithTimeout(p.ctx, 10*time.Second)
	err = chromedp.Run(openCtx,
		chromedp.Click(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer`, chromedp.ByQuery),
		chromedp.Text(`.composer strong`, &headingText, chromedp.ByQuery),
	)
	openCancel()
	if err != nil {
		t.Fatalf("clicking Comment button / opening composer failed: %v%s", err, diag(""))
	}
	// Heading shows the char-range label and the selected snippet.
	if !strings.Contains(headingText, "L3:5-10") {
		t.Errorf("composer heading missing char-range label L3:5-10, got %q%s", headingText, diag(""))
	}
	if !strings.Contains(headingText, "Greet") {
		t.Errorf("composer heading missing selected snippet %q, got %q%s", "Greet", headingText, diag(""))
	}

	// Type a comment and save.
	var cardText string
	saveCtx, saveCancel := context.WithTimeout(p.ctx, 10*time.Second)
	err = chromedp.Run(saveCtx,
		chromedp.SendKeys(`.composer textarea[name="body"]`, "rename this", chromedp.ByQuery),
		chromedp.Click(`.composer .save-btn`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
		chromedp.Text(`.inline-comment`, &cardText, chromedp.ByQuery),
	)
	saveCancel()
	if err != nil {
		t.Fatalf("saving text comment failed: %v%s", err, diag(""))
	}
	if !strings.Contains(cardText, "L3:5-10") {
		t.Errorf("saved comment card missing char-range label L3:5-10, got %q%s", cardText, diag(""))
	}

	// Strongest check: the persisted CSV row carries kind=text + exact offsets.
	csvPath := filepath.Join(p.repo, ".prereview", "comments.csv")
	raw, readErr := os.ReadFile(csvPath)
	if readErr != nil {
		t.Fatalf("read comments.csv: %v%s", readErr, diag(""))
	}
	csv := string(raw)
	// header + one row; the row has kind=text, from_line=3, from_col=5, to_col=10.
	if !strings.Contains(csv, ",text,") {
		t.Errorf("CSV has no kind=text row:\n%s", csv)
	}
	// Columns: ...,kind,area,url,from_col,to_col → the text row ends with ...,text,,,5,10
	if !strings.Contains(csv, ",text,,,5,10") {
		t.Errorf("CSV text row missing expected offsets (…,text,,,5,10):\n%s", csv)
	}
	if !strings.Contains(csv, "rename this") {
		t.Errorf("CSV missing comment body:\n%s", csv)
	}

	// The saved span must render a persistent <mark> over exactly "Greet" on
	// line 3 (server-side MarkRanges), so the highlight survives re-render and
	// works with no client JS.
	var markText string
	markCtx, markCancel := context.WithTimeout(p.ctx, 10*time.Second)
	err = chromedp.Run(markCtx,
		chromedp.WaitVisible(`.code [data-line="3"] mark.comment-span`, chromedp.ByQuery),
		chromedp.Text(`.code [data-line="3"] mark.comment-span`, &markText, chromedp.ByQuery),
	)
	markCancel()
	if err != nil {
		t.Fatalf("saved span never rendered a <mark>: %v%s", err, diag(""))
	}
	if strings.TrimSpace(markText) != "Greet" {
		t.Errorf("comment-span mark wraps %q, want %q%s", markText, "Greet", diag(""))
	}

	for _, line := range consoleLines {
		if strings.HasPrefix(line, "error ") {
			t.Errorf("browser console error: %s", line)
		}
	}
}

// TestE2E_TextSelectKeyboard drives the keyboard caret path: focus a line,
// Shift+ArrowRight to build a character selection, Enter to commit — the
// selection must reach the same kind=text comment as mouse, and Enter must NOT
// fall through to selectLine (whole-line). Line 3 is `func Greet(...)`; three
// Shift+ArrowRights select "fun" (runes [0,3)).
func TestE2E_TextSelectKeyboard(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureTextSelectRepo(t), 1200, 800)
	p.waitReady()
	p.clickFile("demo.go")

	// ArrowDown x3 moves the server is-cursor (and thus the block caret) onto
	// line 3 ("func Greet…"): line 1 "package demo", 2 blank, 3 func. Then
	// Shift+ArrowRight x3 extends the selection from the caret (col 0) to "fun".
	down := pressKey("ArrowDown", "ArrowDown", 40, 0)
	shiftRight := pressKey("ArrowRight", "ArrowRight", 39, input.ModifierShift)
	var headingText string
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.code [data-line="3"] [data-line-text]`, chromedp.ByQuery),
		down, chromedp.Sleep(150*time.Millisecond),
		down, chromedp.Sleep(150*time.Millisecond),
		down, chromedp.Sleep(150*time.Millisecond),
		chromedp.WaitVisible(`.code [data-line="3"].is-cursor`, chromedp.ByQuery),
		shiftRight, shiftRight, shiftRight,
		chromedp.Sleep(400*time.Millisecond),
		chromedp.WaitVisible(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		// Enter commits the keyboard selection (must open the text composer, not
		// a whole-line one).
		pressKey("Enter", "Enter", 13, 0),
		chromedp.WaitVisible(`.composer`, chromedp.ByQuery),
		chromedp.Text(`.composer strong`, &headingText, chromedp.ByQuery),
	)
	cancel()
	if err != nil {
		t.Fatalf("keyboard select → composer failed: %v\nserver: %s", err, p.stderr.String())
	}
	if !strings.Contains(headingText, "L3:0-3") {
		t.Errorf("keyboard composer heading = %q, want a char range L3:0-3", headingText)
	}
	if !strings.Contains(headingText, "fun") {
		t.Errorf("keyboard composer heading missing snippet %q, got %q", "fun", headingText)
	}

	// Save and confirm the CSV carries kind=text with offsets 0..3.
	csvPath := filepath.Join(p.repo, ".prereview", "comments.csv")
	saveCtx, saveCancel := context.WithTimeout(p.ctx, 10*time.Second)
	err = chromedp.Run(saveCtx,
		chromedp.SendKeys(`.composer textarea[name="body"]`, "keyboard comment", chromedp.ByQuery),
		chromedp.Click(`.composer .save-btn`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	)
	saveCancel()
	if err != nil {
		t.Fatalf("saving keyboard text comment failed: %v\nserver: %s", err, p.stderr.String())
	}
	raw, readErr := os.ReadFile(csvPath)
	if readErr != nil {
		t.Fatalf("read comments.csv: %v", readErr)
	}
	if csv := string(raw); !strings.Contains(csv, ",text,,,0,3") {
		t.Errorf("keyboard CSV row missing offsets (…,text,,,0,3):\n%s", csv)
	}
}

// TestE2E_TextCaretVisibleAndMoves pins the user-visible caret behaviour: a
// block caret is drawn on page load (no mouse selection needed) and moves
// horizontally with Left/Right and vertically with Up/Down.
func TestE2E_TextCaretVisibleAndMoves(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureTextSelectRepo(t), 1200, 800)
	p.waitReady()
	p.clickFile("demo.go")

	caretBox := `(() => {
		const c = document.querySelector('[data-lvt-text-caret]');
		if (!c) return null;
		const r = c.getBoundingClientRect();
		return JSON.stringify({left: Math.round(r.left), top: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height)});
	})()`

	// Caret present on load with non-zero size.
	var onLoad string
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.code [data-line-text]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-lvt-text-caret]`, chromedp.ByQuery),
		chromedp.Evaluate(caretBox, &onLoad),
	)
	cancel()
	if err != nil || onLoad == "" {
		t.Fatalf("block caret not visible on load: %v (box=%q)\nserver: %s", err, onLoad, p.stderr.String())
	}
	var load struct{ Left, Top, W, H int }
	mustJSON(t, onLoad, &load)
	if load.W <= 0 || load.H <= 0 {
		t.Errorf("caret has zero size on load: %+v", load)
	}

	// ArrowRight moves the caret to the right (same line, next column).
	var afterRight string
	rctx, rcancel := context.WithTimeout(p.ctx, 10*time.Second)
	err = chromedp.Run(rctx,
		pressKey("ArrowRight", "ArrowRight", 39, 0),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Evaluate(caretBox, &afterRight),
	)
	rcancel()
	if err != nil {
		t.Fatalf("ArrowRight failed: %v", err)
	}
	var right struct{ Left, Top, W, H int }
	mustJSON(t, afterRight, &right)
	if right.Left <= load.Left {
		t.Errorf("ArrowRight did not move caret right: load.left=%d right.left=%d", load.Left, right.Left)
	}
	if right.Top != load.Top {
		t.Errorf("ArrowRight changed the caret row (top %d → %d), should stay on the same line", load.Top, right.Top)
	}

	// ArrowDown moves the caret to a lower row.
	var afterDown string
	dctx, dcancel := context.WithTimeout(p.ctx, 10*time.Second)
	err = chromedp.Run(dctx,
		pressKey("ArrowDown", "ArrowDown", 40, 0),
		chromedp.Sleep(200*time.Millisecond),
		pressKey("ArrowDown", "ArrowDown", 40, 0),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(caretBox, &afterDown),
	)
	dcancel()
	if err != nil {
		t.Fatalf("ArrowDown failed: %v", err)
	}
	var down struct{ Left, Top, W, H int }
	mustJSON(t, afterDown, &down)
	if down.Top <= load.Top {
		t.Errorf("ArrowDown did not move caret to a lower row: load.top=%d down.top=%d", load.Top, down.Top)
	}
}

// setupFixtureModifiedLineRepo commits a file then rewrites one line in the
// working tree, so the diff has a del (old side) and add (new side) row that
// SHARE a line number — the case where a side-agnostic composer/card gate would
// render twice.
func setupFixtureModifiedLineRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	// Line 4 is the one we modify; lines 1-3 stay as context so the changed
	// line keeps the same number (4) on both the old and new side.
	mustWrite(t, dir, "demo.go", "package demo\n\nfunc Greet(name string) string {\n\treturn \"hi \" + name\n}\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	// Working-tree change to line 4 → diff shows -4 (old) and +4 (new).
	mustWrite(t, dir, "demo.go", "package demo\n\nfunc Greet(name string) string {\n\treturn \"hello there \" + name\n}\n")
	return dir
}

// TestE2E_TextSelectModifiedLineSingleComposer is the regression guard for the
// "two comment boxes" bug: selecting text on the new-side row of a modified line
// must open exactly ONE composer (on that side), not one per side.
func TestE2E_TextSelectModifiedLineSingleComposer(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureModifiedLineRepo(t), 1200, 800)
	p.waitReady()
	p.clickFile("demo.go")

	// Select a few chars on the NEW-side row of line 4 (both old-4 and new-4
	// exist; we must disambiguate by data-side).
	selectJS := `(() => {
		const line = document.querySelector('.code [data-line="4"][data-side="new"] [data-line-text]');
		if (!line) return 'no new-side line 4';
		const r = document.createRange();
		r.selectNodeContents(line); // whole line's text — non-collapsed, new side
		const sel = window.getSelection();
		sel.removeAllRanges();
		sel.addRange(r);
		return 'ok';
	})()`

	var sel string
	var composerCount int
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.code [data-line="4"][data-side="new"] [data-line-text]`, chromedp.ByQuery),
		chromedp.Evaluate(selectJS, &sel),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.WaitVisible(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.Click(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer`, chromedp.ByQuery),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Evaluate(`document.querySelectorAll('.composer').length`, &composerCount),
	)
	cancel()
	if err != nil || sel != "ok" {
		t.Fatalf("select on modified line failed: %v (sel=%q)\nserver: %s", err, sel, p.stderr.String())
	}
	if composerCount != 1 {
		t.Errorf("modified line showed %d composers, want exactly 1 (side-agnostic gate regressed)", composerCount)
	}
}

// TestE2E_TextSelectRenderedMarkdown is the regression for the reported bug:
// text selection produced no Comment button in the rendered Markdown (Preview)
// view. Selecting rendered prose must show the button, open a text composer, and
// persist a kind=text comment anchored to the block's SOURCE line range with the
// selected phrase quoted (columns are 0 — rendered text has no source columns).
func TestE2E_TextSelectRenderedMarkdown(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureGFMRepo(t), 1200, 900)
	p.waitReady()
	p.clickFile("gfm.md")

	// rect of a rendered prose block (the footnote paragraph "A claim …")
	var box []float64
	rectJS := `(() => {
		for (const b of document.querySelectorAll('.md-rendered [data-from], .md-rendered')) {}
		// pick the .md-rendered block whose text contains "claim"
		const blocks = [...document.querySelectorAll('.md-rendered')];
		const el = blocks.find(b => b.textContent.includes('claim')) || blocks[0];
		if (!el) return [];
		const p = el.querySelector('p') || el;
		const r = p.getBoundingClientRect();
		return [r.left, r.top, r.width, r.height];
	})()`

	drag := chromedp.ActionFunc(func(ctx context.Context) error {
		x0 := box[0] + 4
		y := box[1] + box[3]/2
		x1 := box[0] + box[2]*0.5
		for _, s := range []struct {
			t input.MouseType
			x float64
		}{
			{input.MousePressed, x0}, {input.MouseMoved, x0 + 10}, {input.MouseMoved, (x0 + x1) / 2}, {input.MouseMoved, x1}, {input.MouseReleased, x1},
		} {
			if err := input.DispatchMouseEvent(s.t, s.x, y).WithButton(input.Left).WithClickCount(1).Do(ctx); err != nil {
				return err
			}
			time.Sleep(60 * time.Millisecond)
		}
		return nil
	})

	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.md-rendered`, chromedp.ByQuery),
		chromedp.Evaluate(rectJS, &box),
	)
	cancel()
	if err != nil || len(box) < 4 {
		t.Fatalf("no rendered block rect: %v box=%v\nserver: %s", err, box, p.stderr.String())
	}

	var headingText string
	openCtx, openCancel := context.WithTimeout(p.ctx, 12*time.Second)
	err = chromedp.Run(openCtx,
		drag,
		chromedp.Sleep(400*time.Millisecond),
		chromedp.WaitVisible(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.Click(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer`, chromedp.ByQuery),
		chromedp.Text(`.composer strong`, &headingText, chromedp.ByQuery),
	)
	openCancel()
	if err != nil {
		t.Fatalf("rendered-view select → composer failed: %v\nserver: %s", err, p.stderr.String())
	}
	// Heading shows a plain line label (no :columns) and the quoted phrase.
	if !strings.Contains(headingText, "Comment on L") {
		t.Errorf("composer heading missing line label, got %q", headingText)
	}
	if strings.Contains(headingText, ":") && strings.Contains(headingText, "-") {
		// crude guard against "L3:0-0" style column rendering
		if strings.Contains(headingText, ":0-") {
			t.Errorf("rendered comment should not show columns, got %q", headingText)
		}
	}

	var cardText, snippet string
	saveCtx, saveCancel := context.WithTimeout(p.ctx, 12*time.Second)
	err = chromedp.Run(saveCtx,
		chromedp.SendKeys(`.composer textarea[name="body"]`, "tighten this claim", chromedp.ByQuery),
		chromedp.Click(`.composer .save-btn`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
		chromedp.Text(`.inline-comment`, &cardText, chromedp.ByQuery),
		chromedp.Text(`.inline-comment .ic-snippet`, &snippet, chromedp.ByQuery),
	)
	saveCancel()
	if err != nil {
		t.Fatalf("saving rendered text comment failed: %v\nserver: %s", err, p.stderr.String())
	}
	if strings.TrimSpace(snippet) == "" {
		t.Errorf("saved card missing quoted snippet; card=%q", cardText)
	}

	// CSV: a kind=text row with 0 columns (rendered-origin) + the body.
	raw, readErr := os.ReadFile(filepath.Join(p.repo, ".prereview", "comments.csv"))
	if readErr != nil {
		t.Fatalf("read csv: %v", readErr)
	}
	csv := string(raw)
	if !strings.Contains(csv, ",text,,,0,0") {
		t.Errorf("CSV text row (rendered) should have 0 columns (…,text,,,0,0):\n%s", csv)
	}
	if !strings.Contains(csv, "tighten this claim") {
		t.Errorf("CSV missing body:\n%s", csv)
	}
}
