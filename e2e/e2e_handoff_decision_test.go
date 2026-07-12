//go:build browser

// End-to-end coverage for issue #98 Phase 3: the decision round-trip. The LLM
// submits a suggestion (`prereview suggest`), the reviewer decides on it, and the
// decision auto-emits to the LLM in the stream's snapshot event (stdout +
// events.jsonl) — carrying the verdict + note + original/proposed. Under
// continuous enqueue (#119) there is no Hand off button: deciding a suggestion is
// a persisted mutation that auto-emits a snapshot. Only DECIDED, non-outdated
// suggestions ship; an undecided one is not emitted.
//
// Run with: go test -tags=browser -run TestE2E_HandoffDecisions ./e2e/...

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/internal/review"
)

// readEvents returns the contents of the repo's .prereview/events.jsonl (the
// durable channel the LLM tails), or "" if it isn't there yet.
func readEvents(t *testing.T, repo string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, ".prereview", "events.jsonl"))
	if err != nil {
		return ""
	}
	return string(b)
}

func TestE2E_HandoffDecisions(t *testing.T) {
	p, stdoutBuf, _ := bootChromeStreamRepo(t, setupSuggestionRepo(t))
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return fmt.Sprintf("\n--- server ---\n%s\n--- html ---\n%s", p.stderr.String(), html)
	}
	p.waitReady()
	p.clickFile("app.go")

	// The LLM proposes two edits on app.go; the reviewer will accept one and leave
	// the other undecided.
	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"acc","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi there\"","note":"tighten"},
	  {"id":"undecided","file":"app.go","from_line":1,"to_line":1,"original":"package app","proposed":"package app // greeter","note":"label"}
	]`)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-acc"] button[name="acceptSuggestion"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("suggestion action row never appeared: %v%s", err, diag())
	}

	// Accept exactly the "acc" suggestion. It collapses behind its badge (#165), so PEEK
	// line 4 to confirm the verdict.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.inline-suggestion[data-key="sg-acc"] button[name="acceptSuggestion"]').click()`, nil),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("accept: %v%s", err, diag())
	}
	p.peekRow(4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion[data-key="sg-acc"] .sg-verdict-badge.sg-accept`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accepted verdict badge: %v%s", err, diag())
	}

	// Continuous enqueue (#119): stream mode has NO Hand off button. The accept
	// above persisted the decision, which auto-emits a debounced snapshot — the
	// decision ships in that emitted snapshot event with no button click.
	waitStream(t, stdoutBuf, func(evs []review.StreamEvent) bool {
		h := handoffEvents(evs)
		if len(h) < 1 {
			return false
		}
		return len(h[len(h)-1].DecisionList()) == 1
	}, "snapshot event carrying exactly the one decided suggestion", diag)

	// Inspect the shipped decision: verdict + content, and the undecided one absent.
	h := handoffEvents(parseStreamEvents(stdoutBuf.String()))
	decisions := h[len(h)-1].DecisionList()
	if len(decisions) != 1 {
		t.Fatalf("want exactly 1 decision (the accepted one), got %d: %+v", len(decisions), decisions)
	}
	d := decisions[0]
	if d.ID != "acc" || d.Verdict != "accept" {
		t.Errorf("wrong decision shipped: %+v", d)
	}
	if d.Original != `return "hello world"` || d.Proposed != `return "hi there"` {
		t.Errorf("decision missing original/proposed content: %+v", d)
	}
	for _, dd := range decisions {
		if dd.ID == "undecided" {
			t.Error("an undecided suggestion must NOT ship in the snapshot")
		}
	}

	// events.jsonl mirrors the same decision (durable channel the LLM tails).
	if !strings.Contains(readEvents(t, p.repo), `"id":"acc"`) {
		t.Errorf("events.jsonl should mirror the shipped decision\n%s", diag())
	}
}
