//go:build browser

// Regression guards for the file-comment composer UX:
//   1. Pressing `c` opens a BLOCKING, viewport-centered modal (reachable at any
//      scroll position) — not an inline box above the diff that a mid-page scroll
//      leaves off-screen.
//   2. Focus stays in the composer while you type. The prior comment's
//      ScrollToCommentID `lvt-autofocus` used to re-fire on every debounced
//      saveDraft re-render and steal focus — both when commenting on a file and
//      when adding line comments in sequence.
//
// Run with: go test -tags=browser -run 'TestE2E_FileCommentModal|TestE2E_SequentialComments' ./e2e/...

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// composerState reports whether the given textarea holds focus and its value.
func composerState(p *runningPrereview, sel string) (bool, string) {
	var r struct {
		Focused bool
		Val     string
	}
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`(()=>{const ta=document.querySelector('`+sel+`');return{Focused:document.activeElement===ta,Val:ta?ta.value:""}})()`, &r))
	return r.Focused, r.Val
}

func TestE2E_FileCommentModalKeepsFocus(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")

	// A prior comment → sets ScrollToCommentID (the focus thief on old code).
	addLineComment(t, p, 0, 4, "prior comment")

	// Press `c` → the file-comment MODAL opens (backdrop + centered card).
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent("c"),
		chromedp.WaitVisible(`.fc-modal .composer textarea[name="body"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.fc-modal-backdrop`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("press c → file modal: %v\nstderr: %s", err, p.stderr.String())
	}

	const ta = `.fc-modal textarea[name="body"]`
	focused := false
	for i := 0; i < 20; i++ {
		if f, _ := composerState(p, ta); f {
			focused = true
			break
		}
		time.Sleep(75 * time.Millisecond)
	}
	if !focused {
		t.Fatalf("file modal textarea should autofocus\nstderr: %s", p.stderr.String())
	}

	// Type, wait for a saveDraft debounce round-trip (re-render), assert focus
	// SURVIVED and text is intact. On the old code the prior comment steals focus
	// here, so subsequent keystrokes miss the textarea.
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent("hello")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(450 * time.Millisecond) // > saveDraft debounce (150ms) + re-render
	if f, v := composerState(p, ta); !f || !strings.Contains(v, "hello") {
		t.Fatalf("after saveDraft re-render, focus must stay in the modal textarea with text intact; focused=%v val=%q", f, v)
	}
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent(" world")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(450 * time.Millisecond)
	if f, v := composerState(p, ta); !f || v != "hello world" {
		t.Fatalf("typing through re-renders must accumulate in the modal textarea; focused=%v val=%q", f, v)
	}

	// Optional visual capture (set PREREVIEW_MODAL_SHOT=<dir>).
	if dir := os.Getenv("PREREVIEW_MODAL_SHOT"); dir != "" {
		var buf []byte
		if err := chromedp.Run(p.ctx, chromedp.FullScreenshot(&buf, 90)); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "file-comment-modal.png"), buf, 0o644)
		}
	}

	// Save → the file comment persists.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.fc-modal button[name="addComment"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.fc-modal`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("save file comment: %v\nstderr: %s", err, p.stderr.String())
	}
	found := false
	for _, r := range p.readCSV() {
		if len(r) > 5 && strings.Contains(r[5], "hello world") {
			found = true
		}
	}
	if !found {
		t.Errorf("file comment 'hello world' not found in CSV: %v", p.readCSV())
	}
}

func TestE2E_SequentialCommentsKeepFocus(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")

	// Comment one → sets ScrollToCommentID.
	addLineComment(t, p, 3, 3, "comment one")

	// Open a SECOND composer on another line and type — focus must stay here, not
	// get stolen by comment one on the saveDraft re-render.
	p.clickLine(0, 4)
	const ta = `.composer textarea[name="body"]`
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(ta, chromedp.ByQuery),
		chromedp.Click(ta, chromedp.ByQuery),
		chromedp.KeyEvent("second"),
	); err != nil {
		t.Fatalf("open + type second composer: %v\nstderr: %s", err, p.stderr.String())
	}
	time.Sleep(450 * time.Millisecond) // saveDraft debounce + re-render
	if f, v := composerState(p, ta); !f || !strings.Contains(v, "second") {
		t.Fatalf("focus must stay in the second composer through the re-render; focused=%v val=%q\nstderr: %s", f, v, p.stderr.String())
	}
}
