//go:build browser

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/device"
)

// TestE2E_FileCommentShortcuts (#275): the file header has one Comment on file
// button, no Ask for suggestions. Its box shows the four built-in whole-file
// prompts as chips, then More, which shows the fifth; a chip fills the box, and
// Save queues an ordinary whole-file comment carrying the prompt (the agent
// answers it with `prereview suggest`). On a phone the chips stay in one row
// without widening the page, and "Add your own" opens the Library's Shortcuts tab.
func TestE2E_FileCommentShortcuts(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("app.go")
	if n := evalInt(t, p, `document.querySelectorAll('.prompts-trigger, button[name="pickPrompt"]').length`); n != 0 {
		t.Fatalf("the Ask for suggestions dropdown must be gone, found %d%s", n, diag())
	}

	click := func(sel, what string) {
		t.Helper()
		ctx, cancel := within(p, 10*time.Second)
		defer cancel()
		if err := chromedp.Run(ctx, chromedp.Click(sel, chromedp.ByQuery)); err != nil {
			t.Fatalf("%s: %v%s", what, err, diag())
		}
	}
	chips := `document.querySelectorAll('.composer .sc-chips button[name="useShortcut"]').length`
	click(`button[name="openFileComment"]`, "open the file comment box")
	waitJSTrueDiag(t, p.ctx, chips+` === 4 && !!document.querySelector('.composer .sc-more')`, 5*time.Second, "four whole-file chips and More", diag)

	click(`.composer .sc-more`, "tap More")
	waitJSTrueDiag(t, p.ctx, chips+` === 5 && !document.querySelector('.composer .sc-more')`, 5*time.Second, "More shows the fifth chip", diag)

	ctx, cancel := within(p, 10*time.Second)
	err := chromedp.Run(ctx, chromedp.Evaluate(`[...document.querySelectorAll('.composer button[name="useShortcut"]')].find(b => b.textContent.trim() === 'Code review').click()`, nil))
	cancel()
	if err != nil {
		t.Fatalf("tap Code review: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, `(document.querySelector('.composer textarea') || {value: ''}).value.includes('Review this file for bugs')`, 5*time.Second,
		"the chip fills the box with the code-review prompt", diag)
	if body := evalStr(t, p, `document.querySelector('.composer textarea').value`); !strings.Contains(body, "prereview suggest") {
		t.Errorf("the prompt must keep its propose-via-suggest instruction, got %q%s", body, diag())
	}

	// On a phone the chip row scrolls sideways inside the box, never the page.
	if err := chromedp.Run(p.ctx, chromedp.Emulate(device.IPhone11), chromedp.Sleep(300*time.Millisecond)); err != nil {
		t.Fatalf("emulate phone: %v", err)
	}
	if over := evalStr(t, p, `String(document.documentElement.scrollWidth - window.innerWidth)`); over != "0" {
		t.Errorf("the chips widen the page by %spx on a phone%s", over, diag())
	}

	click(`button[name='addComment']`, "save")
	waitJSTrueDiag(t, p.ctx, `!!document.querySelector('.inline-comment')`, 5*time.Second, "the comment is saved", diag)
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected exactly one comment row, got %d: %v", len(rows)-1, rows)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	if rows[1][col["kind"]] != "file" || !strings.Contains(rows[1][col["body"]], "Review this file for bugs") {
		t.Errorf("the saved comment must be kind=file carrying the prompt: %v", rows[1])
	}

	click(`button[name="openFileComment"]`, "open the file comment box again")
	click(`.composer button[name="openShortcutLibrary"]`, "tap Add your own")
	waitJSTrueDiag(t, p.ctx, `[...document.querySelectorAll('.lib-shortcuts .sc-group')].map(h => h.textContent).join('|') === 'On a line|On the whole file'`, 5*time.Second,
		"Add your own opens the Library's two shortcut lists", diag)
	if n := evalInt(t, p, `document.querySelectorAll('.lib-shortcuts [data-key^="sc-file-"]').length`); n != 5 {
		t.Errorf("the whole-file list must hold the 5 built-in prompts, got %d%s", n, diag())
	}
}
