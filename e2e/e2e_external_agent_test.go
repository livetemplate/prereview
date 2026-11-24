//go:build browser

// End-to-end for #248: in a live-site review, the agent's `done` and `reply` show on
// the annotation without a reload. The sidebar card gets the "worked on" badge and the
// agent's reply, the region box on the page is drawn acted-on (dashed, ✓), a reviewer
// reply from the sidebar hands it back to the agent ("awaiting agent", box open
// again), and resolving clears it.
//
// Run with: go test -tags=browser -run TestE2E_ExternalAgentStates ./e2e/...

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_ExternalAgentStates(t *testing.T) {
	app := newLiveApp(t)
	p := bootChromeExternal(t, app.srv.URL, 1200, 900, "--agent")
	diag := captureDiag(t, p)
	run := func(actions ...chromedp.Action) error {
		ctx, cancel := within(p, 20*time.Second)
		defer cancel()
		return chromedp.Run(ctx, actions...)
	}
	agent := func(args ...string) { t.Helper(); mustCLI(t, p, args...) }

	annotateRegion(t, p, "CTA too low contrast", diag)
	var id string
	if err := run(chromedp.AttributeValue(`.pin-layer .area-overlay`, "data-id", &id, nil, chromedp.ByQuery)); err != nil || id == "" {
		t.Fatalf("read the pin's comment id (%q): %v%s", id, err, diag())
	}
	pin := `.pin-layer .area-overlay[data-id="` + id + `"]`
	card := `.ext-anno[data-key="` + id + `"]`

	// The agent marks it done and replies; the page updates live (no reload).
	agent("done", "--out", p.store, id)
	agent("reply", "--out", p.store, id, "--body", "Raised the CTA contrast to 4.8:1.")
	var title string
	if err := run(
		chromedp.WaitVisible(pin+`.is-acted`, chromedp.ByQuery),
		chromedp.AttributeValue(pin, "title", &title, nil, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a worked-on region's box must be drawn acted-on with no reload: %v%s", err, diag())
	}
	if !strings.HasPrefix(title, "Worked on — ") {
		t.Errorf("box tooltip = %q, want it to start with \"Worked on — \"", title)
	}
	// The ✓ must not catch taps meant for the box (it opens the annotation).
	var tickPE string
	if err := run(chromedp.Evaluate(`getComputedStyle(document.querySelector('`+pin+`'), '::after').pointerEvents`, &tickPE)); err != nil || tickPE != "none" {
		t.Errorf("the ✓ tick's pointer-events = %q (%v), want none", tickPE, err)
	}

	// The sidebar card shows the badge and the agent's reply.
	var thread string
	if err := run(
		chromedp.Click(`button[name="toggleAnnotations"]`, chromedp.ByQuery),
		chromedp.WaitVisible(card+` .processed-badge`, chromedp.ByQuery),
		chromedp.Text(card+` .thread`, &thread, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the sidebar card must show \"worked on\" and the agent's reply: %v%s", err, diag())
	}
	if !strings.Contains(thread, "Raised the CTA contrast") {
		t.Errorf("sidebar thread = %q, want the agent's reply", thread)
	}

	// The reviewer replies from the sidebar: it reaches the store, the card reads
	// "awaiting agent", and the box is open again (the agent's turn).
	if err := run(
		chromedp.Click(card+` button[name="openReply"]`, chromedp.ByQuery),
		chromedp.WaitVisible(card+` .reply-form textarea`, chromedp.ByQuery),
		chromedp.SendKeys(card+` .reply-form textarea`, "Still low on hover.", chromedp.ByQuery),
		chromedp.Click(card+` .reply-form button[name="postReply"]`, chromedp.ByQuery),
		chromedp.WaitVisible(card+` .awaiting-badge`, chromedp.ByQuery),
		chromedp.WaitNotPresent(pin+`.is-acted`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("a sidebar reply must reach the agent and reopen the box: %v%s", err, diag())
	}
	if b, err := os.ReadFile(filepath.Join(p.store, "reviewer-replies.jsonl")); err != nil || !strings.Contains(string(b), "Still low on hover.") {
		t.Errorf("reviewer-replies.jsonl = %q (%v), want the sidebar reply", b, err)
	}

	// The agent answers again; then the reviewer resolves, which clears the acted state.
	agent("reply", "--out", p.store, "--answered", id, "--body", "Hover now uses the same colour.")
	if err := run(
		chromedp.WaitVisible(pin+`.is-acted`, chromedp.ByQuery),
		chromedp.Click(card+` button[name="toggleResolved"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(pin+`.is-acted`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("an answered box is acted-on until resolved: %v%s", err, diag())
	}
}
