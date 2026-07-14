//go:build browser

// A line is ONE conversation (#174 follow-up).
//
// Clicking a line that already carries a comment must OPEN that comment's thread — not drop a
// second new-comment composer on top of it, which is what it used to do: the natural gesture
// for "reply to this" created a rival comment instead.
//
// The escape hatch stays: a line that already has a comment can still take a brand-new one by
// SELECTING A PHRASE on it (kind=text). This test walks the whole arc in one browser boot.
//
// Run: go test -tags=browser -run TestE2E_LineClickOpensThread ./e2e/...

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_LineClickOpensThread(t *testing.T) {
	repo := setupFixtureTextSelectRepo(t)
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// c1: an OPEN line comment on line 3 of demo.go — the line whose "Greet" token the
	// text-select fixture is built around, so both gestures land on the same row.
	// c2: a kind=text comment on line 4. A TEXT comment is a thread on that row too, so
	// clicking line 4 must open it — if its card rendered no reply form, that click would be
	// dead, which is the very bug class #174 exists to kill.
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url,from_col,to_col,hidden,enqueued\n" +
		"c1,demo.go,3,3,new,the existing thread,2026-07-13T12:00:00Z,false,,,line,,,0,0,false,false\n" +
		`c2,demo.go,4,4,new,a thread on a phrase,2026-07-13T12:00:00Z,false,"{""text"":""return \""hello \"" + name"",""snippet"":""hello""}",,text,,,9,14,false,false` + "\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	p := bootChromeAgainstRepo(t, repo, 1400, 1000)
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("demo.go")

	row := `.line-row:has(.line[data-line="3"][data-side="new"])`

	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(row+` .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the seeded comment should render inline on line 3: %v%s", err, diag())
	}

	// ── Click the line → its thread opens. No new-comment composer.
	p.clickLine(0, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(row+` .reply-form`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("clicking a commented line must OPEN that thread's reply box — a line is one "+
			"conversation: %v%s", err, diag())
	}
	var composers int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelectorAll('.composer').length`, &composers))
	if composers != 0 {
		t.Errorf("clicking a commented line must NOT open the new-comment composer (found %d) — "+
			"that is the bug: replying to a comment created a rival comment%s", composers, diag())
	}

	// ── The reviewer had COLLAPSED the row (#174's badge). Clicking the line must bring the
	// card back, or the reply box opens on a hidden card and the click reads as dead.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(row+` .line-marks`, chromedp.ByQuery),
		chromedp.WaitVisible(row+`.row-toggled`, chromedp.ByQuery),
		chromedp.WaitNotVisible(row+` .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("badge should collapse the open comment (#174): %v%s", err, diag())
	}
	p.clickLine(0, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(row+` .inline-comment`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .reply-form`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("clicking a line whose comment is collapsed must un-collapse it and open the "+
			"thread — otherwise the click appears to do nothing: %v%s", err, diag())
	}

	// ── The escape hatch: select a phrase on that SAME line → a brand-new (kind=text) comment,
	// and the thread's reply box closes so the row doesn't render both forms at once.
	selectJS := `(() => {
		const line = document.querySelector('.code [data-line="3"] [data-line-text]');
		let target = null;
		for (const s of line.querySelectorAll('span')) {
			if (s.textContent === 'Greet') { target = s; break; }
		}
		if (!target) return 'no Greet token';
		const r = document.createRange();
		r.setStart(target.firstChild, 0);
		r.setEnd(target.firstChild, 5);
		const sel = window.getSelection();
		sel.removeAllRanges();
		sel.addRange(r);
		return 'ok';
	})()`
	var selResult, heading string
	ctx, cancel := context.WithTimeout(p.ctx, 20*time.Second)
	err := chromedp.Run(ctx,
		chromedp.Evaluate(selectJS, &selResult),
		chromedp.Sleep(400*time.Millisecond), // debounced selectionchange (150ms)
		chromedp.WaitVisible(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.Click(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer`, chromedp.ByQuery),
		chromedp.Text(`.composer strong`, &heading, chromedp.ByQuery),
	)
	cancel()
	if err != nil || selResult != "ok" {
		t.Fatalf("selecting a phrase on an ALREADY-COMMENTED line must still compose a NEW "+
			"comment — the user-facing escape hatch: %v (select=%q)%s", err, selResult, diag())
	}
	if !strings.Contains(heading, "L3:5-10") || !strings.Contains(heading, "Greet") {
		t.Errorf("the new comment must be anchored to the selected characters; composer heading "+
			"= %q, want the L3:5-10 range and the %q snippet%s", heading, "Greet", diag())
	}
	// Only ONE form on the row: the reply box must have closed when the composer armed.
	var replies int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`[...document.querySelectorAll('.reply-form')].filter(e=>e.offsetParent).length`, &replies))
	if replies != 0 {
		t.Errorf("arming the new-comment composer must close the thread's reply box — the row "+
			"cannot show a reply form and a composer at once (found %d)%s", replies, diag())
	}

	// ── A kind=text comment is a thread on its row too. Clicking line 4 must open IT — a
	// reply box must actually appear. If the text card rendered no reply form, the click
	// would set an invisible state and look broken, which is precisely the dead-affordance
	// bug #174 is about.
	// Dismiss that composer first. A click while a selection is LIVE is a range extension
	// (SelectLine's two-click range), which openThreadOnLine deliberately does not hijack —
	// so the thread-opening below is only meaningful from a clean slate.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.composer .cancel-btn`, chromedp.ByQuery),
		chromedp.Sleep(250*time.Millisecond),
	); err != nil {
		t.Fatalf("cancel the text composer: %v%s", err, diag())
	}

	row4 := `.line-row:has(.line[data-line="4"][data-side="new"])`
	tctx, tcancel := context.WithTimeout(p.ctx, 15*time.Second)
	err = chromedp.Run(tctx, chromedp.WaitVisible(row4+` .inline-comment`, chromedp.ByQuery))
	tcancel()
	if err != nil {
		t.Fatalf("precondition: the seeded kind=text comment should render on line 4: %v%s", err, diag())
	}
	p.clickLine(0, 4)
	tctx, tcancel = context.WithTimeout(p.ctx, 15*time.Second)
	err = chromedp.Run(tctx, chromedp.WaitVisible(row4+` .reply-form`, chromedp.ByQuery))
	tcancel()
	if err != nil {
		t.Fatalf("clicking a line whose comment is a TEXT (phrase) comment must open that "+
			"thread's reply box — a card with no reply form would make the click dead: %v%s",
			err, diag())
	}

	// The existing threads are untouched: opening one is a VIEW action, it writes nothing.
	rows := p.readCSV()
	if len(rows) != 3 {
		t.Errorf("clicking a line to read its thread must not write anything; CSV has %d data "+
			"row(s), want 2%s", len(rows)-1, diag())
	}
}

// The rendered-Markdown view: the reviewer clicks a BLOCK (paragraph), not a line. The rule
// has to hold there too, or the fix is half-delivered for exactly the markdown-draft review
// that motivated it — the whole point was a doc review where a paragraph already carries a
// comment.
func TestE2E_BlockClickOpensThread(t *testing.T) {
	repo := setupFixtureRepoMarkdown(t) // docs.md: prose lines 3-5 render one block PER LINE
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,docs.md,3,3,new,the existing thread,2026-07-13T12:00:00Z,false,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	p := bootChromeAgainstRepo(t, repo, 1400, 1000)
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("docs.md")

	block := `.md-block:has(.inline-comment[data-key="c1"])`
	bctx, bcancel := context.WithTimeout(p.ctx, 20*time.Second)
	err := chromedp.Run(bctx, chromedp.WaitVisible(block+` .inline-comment`, chromedp.ByQuery))
	bcancel()
	if err != nil {
		t.Fatalf("precondition: the seeded comment should render in its md block: %v%s", err, diag())
	}

	// Click the block's rendered prose → its thread opens; no new-comment composer.
	bctx, bcancel = context.WithTimeout(p.ctx, 20*time.Second)
	err = chromedp.Run(bctx,
		chromedp.Click(block+` .md-rendered`, chromedp.ByQuery),
		chromedp.WaitVisible(block+` .reply-form`, chromedp.ByQuery),
	)
	bcancel()
	if err != nil {
		t.Fatalf("clicking a rendered block that already carries a comment must OPEN that "+
			"thread — a block, like a line, is one conversation: %v%s", err, diag())
	}
	var composers int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelectorAll('.composer').length`, &composers))
	if composers != 0 {
		t.Errorf("clicking a commented block must NOT open the new-comment composer (found %d)%s",
			composers, diag())
	}

	// The escape hatch, in the rendered view: selecting a phrase INSIDE the commented block
	// must still compose a brand-new comment (kind=text, data-surface="block") — and close
	// the thread's reply box, so the block never shows a reply form and a composer at once.
	selectJS := `(() => {
		const el = document.querySelector('` + block + ` .md-rendered');
		if (!el) return 'no block';
		const node = (el.querySelector('p') || el).firstChild;
		if (!node || node.nodeType !== 3) return 'no text node';
		const r = document.createRange();
		r.setStart(node, 0);
		r.setEnd(node, Math.min(6, node.textContent.length));
		const sel = window.getSelection();
		sel.removeAllRanges();
		sel.addRange(r);
		return 'ok';
	})()`
	var selResult string
	bctx, bcancel = context.WithTimeout(p.ctx, 20*time.Second)
	err = chromedp.Run(bctx,
		chromedp.Evaluate(selectJS, &selResult),
		chromedp.Sleep(400*time.Millisecond), // debounced selectionchange (150ms)
		chromedp.WaitVisible(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.Click(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer`, chromedp.ByQuery),
	)
	bcancel()
	if err != nil || selResult != "ok" {
		t.Fatalf("selecting a phrase inside an ALREADY-COMMENTED block must still compose a "+
			"NEW comment — the escape hatch: %v (select=%q)%s", err, selResult, diag())
	}
	var replies int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`[...document.querySelectorAll('.reply-form')].filter(e=>e.offsetParent).length`, &replies))
	if replies != 0 {
		t.Errorf("arming the composer must close the block's reply box — a block cannot show a "+
			"reply form and a composer at once (found %d)%s", replies, diag())
	}

	// Nothing was written — opening a thread is a view action.
	if rows := p.readCSV(); len(rows) != 2 {
		t.Errorf("opening a block's thread must not write anything; CSV has %d data row(s), "+
			"want 1%s", len(rows)-1, diag())
	}
}
