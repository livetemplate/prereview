//go:build browser

// End-to-end for #204's ripple group: the agent answers a comment with a change
// that reaches past its line (a deletion plus follow-on edits elsewhere), each
// suggestion naming the comment in "for". The comment's card shows the group with
// Accept all / Reject all, and each suggestion links back to the comment.
//
// Run with: go test -tags=browser -run TestE2E_Ripple ./e2e/...

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_Ripple(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 390, 844, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(390, 844)
	p.clickFile("app.go")
	addLineComment(t, p, 0, 4, "Drop this")
	id := lastComment(t, p).ID
	if id == "" {
		t.Fatalf("no comment id%s", diag())
	}

	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"d1","for":"`+id+`","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"\""},
	  {"id":"f1","for":"`+id+`","file":"app.go","from_line":3,"to_line":3,"original":"func Greet() string {","proposed":"func Greet() (s string) {"},
	  {"id":"f2","for":"`+id+`","file":"app.go","from_line":1,"to_line":1,"original":"package app","proposed":"package app // greeting removed"}
	]`)
	card := `.inline-comment[data-key="` + id + `"]`
	ctx, cancel := within(p, 20*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.WaitVisible(card+` .ripple-row`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the ripple row never showed (live update): %v%s", err, diag())
	}
	if got := textOf(t, p, card+` .ripple-count`); got != "Follow-on edits: 3 open of 3" {
		t.Errorf("ripple row = %q%s", got, diag())
	}
	if n := countSel(t, p, `.inline-suggestion .sg-for-btn`); n != 3 {
		t.Errorf("want the for-your-comment link on all 3 suggestions, got %d%s", n, diag())
	}

	// The link on the farthest suggestion jumps back to the comment.
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.querySelector('.inline-suggestion[data-key="sg-f2"]').scrollIntoView({block:'center'})`, nil),
		chromedp.Click(`.inline-suggestion[data-key="sg-f2"] .sg-for-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("tap for your comment: %v%s", err, diag())
	}
	expectInView(t, p, card, "the comment the suggestion follows from", diag)

	// Accept all: three accepts in one go, and the row says nothing's left.
	if err := chromedp.Run(ctx,
		chromedp.Click(card+` button[name="acceptRipple"]`, chromedp.ByQuery),
		chromedp.Poll(`(document.querySelector('`+card+` .ripple-count')||{}).textContent === 'Follow-on edits: 0 open of 3'`, nil, chromedp.WithPollingInterval(50*time.Millisecond)),
	); err != nil {
		t.Fatalf("Accept all: %v%s", err, diag())
	}
	if n := countSel(t, p, card+` button[name="acceptRipple"], `+card+` button[name="rejectRipple"]`); n != 0 {
		t.Errorf("nothing is open, but the row still offers %d buttons%s", n, diag())
	}
	b, err := os.ReadFile(filepath.Join(p.store, "suggestion-decisions.jsonl"))
	if err != nil {
		t.Fatalf("read decisions: %v", err)
	}
	if n := strings.Count(string(b), `"verdict":"accept"`); n != 3 {
		t.Errorf("want 3 accepts on disk, got %d:\n%s", n, b)
	}
}
