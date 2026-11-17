//go:build browser

// End-to-end for #204: shortcut chips on the line and text composers, at phone size.
// A tap adds the shortcut's text to the comment while the textarea still has focus
// (iOS doesn't move focus to a tapped button, and livetemplate won't write into a
// focused textarea), keeps the selection's kind, and the chips you tap most come
// first on the next visit.
//
// Run with: go test -tags=browser -run TestE2E_Shortcuts ./e2e/...

package e2e

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// tapChip types typed into the focused composer, then fires the chip whose label is
// label with a JS click: unlike chromedp.Click, that leaves focus in the textarea,
// as a phone tap does. It returns the textarea's value once the chip's text is in.
func tapChip(t *testing.T, p *runningPrereview, typed, label, want string, diag func() string) string {
	t.Helper()
	ctx, cancel := within(p, 15*time.Second)
	defer cancel()
	var focused bool
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.composer .sc-chips .sc-chip`, chromedp.ByQuery),
		chromedp.Focus(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, typed, chromedp.ByQuery),
		chromedp.Evaluate(`document.activeElement === document.querySelector('.composer textarea')`, &focused),
	); err != nil || !focused {
		t.Fatalf("focus and type in the composer (focused=%v): %v%s", focused, err, diag())
	}
	var value string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`[...document.querySelectorAll('.composer .sc-chip')].find(b => b.textContent.trim() === `+jsStr(label)+`).click()`, nil),
		chromedp.Poll(`document.querySelector('.composer textarea').value.includes(`+jsStr(want)+`)`, nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.Value(`.composer textarea`, &value, chromedp.ByQuery),
	); err != nil {
		var now string
		_ = chromedp.Run(p.ctx, chromedp.Value(`.composer textarea`, &now, chromedp.ByQuery))
		t.Fatalf("tap %q: the composer never got its text (now %q): %v%s", label, now, err, diag())
	}
	return value
}

// lastComment is the newest comment, read through the agent's own CLI.
func lastComment(t *testing.T, p *runningPrereview) streamComment {
	t.Helper()
	out, err := exec.Command(p.binary, "comments", "--json", "--out", p.store).Output()
	if err != nil {
		t.Fatalf("prereview comments: %v", err)
	}
	var cs []streamComment
	if err := json.Unmarshal(out, &cs); err != nil || len(cs) == 0 {
		t.Fatalf("comments --json (%v): %s", err, out)
	}
	return cs[len(cs)-1]
}

func saveComposer(t *testing.T, p *runningPrereview, diag func() string) {
	t.Helper()
	ctx, cancel := within(p, 10*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.composer button[name="addComment"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.composer`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("save: %v%s", err, diag())
	}
}

func TestE2E_Shortcuts(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureTextSelectRepo(t), 390, 844)
	diag := captureDiag(t, p)
	p.waitReadyAt(390, 844)
	p.clickFile("demo.go")

	// A line comment: typed text first, the shortcut after it, kind=line kept.
	p.clickLine(0, 3)
	got := tapChip(t, p, "my note", "Make this shorter", "Make this shorter", diag)
	if !strings.HasPrefix(got, "my note\n\nMake this shorter") {
		t.Errorf("composer = %q, want the typed text then the shortcut%s", got, diag())
	}
	if active := evalStr(t, p, `document.activeElement && document.activeElement.tagName`); active != "TEXTAREA" {
		t.Errorf("after the tap, focus is on %s, want the comment box%s", active, diag())
	}
	saveComposer(t, p, diag)
	// A line comment's kind is empty; a PickPrompt-style reset would make it "file".
	if c := lastComment(t, p); c.Kind != "" || c.ToLine != 3 || !strings.Contains(c.Body, "my note") || !strings.Contains(c.Body, "Make this shorter") {
		t.Errorf("saved comment kind=%q L%d body=%q, want a line comment on L3 with both texts%s", c.Kind, c.ToLine, c.Body, diag())
	}

	// A text comment keeps kind=text and its columns.
	ctx, cancel := within(p, 15*time.Second)
	var sel string
	err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.code [data-line="3"] [data-line-text]`, chromedp.ByQuery),
		chromedp.Evaluate(selectGreetJS, &sel),
		chromedp.WaitVisible(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.Click(`[data-lvt-text-select-button]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
	)
	cancel()
	if err != nil || sel != "ok" {
		t.Fatalf("select Greet and open its composer (%q): %v%s", sel, err, diag())
	}
	tapChip(t, p, "", "Needs evidence", "This needs evidence", diag)
	tapChip(t, p, "", "Needs evidence", "This needs evidence", diag) // a second tap: evidence now has 2 uses
	saveComposer(t, p, diag)
	if c := lastComment(t, p); c.Kind != "text" || c.FromCol != 5 || c.ToCol != 10 {
		t.Errorf("saved comment kind=%q cols=%d-%d, want kind=text 5-10%s", c.Kind, c.FromCol, c.ToCol, diag())
	}

	// Next visit: the most-tapped chip leads.
	if err := chromedp.Run(p.ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	p.waitReadyAt(390, 844)
	p.clickFile("demo.go")
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.composer .sc-chip`, chromedp.ByQuery)); err != nil {
		t.Fatalf("composer on line 4: %v%s", err, diag())
	}
	first := strings.TrimSpace(evalStr(t, p, `(document.querySelector('.composer .sc-chip')||{}).textContent||""`))
	if first != "Needs evidence" {
		t.Errorf("first chip after reload = %q, want the most used (Needs evidence)%s", first, diag())
	}
}
