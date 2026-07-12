//go:build browser

// End-to-end for #159 Phase 10 (applied ack), in the #165 state-badge model: accepting
// a suggestion collapses it to a GREEN suggestion count badge (card hidden); the agent
// then applies the edit and runs `prereview applied <id>`, and the peeked card carries
// the "Edit applied to the file" status live, with no desyncing Undo.
//
// Run: go test -tags=browser -run TestE2E_AppliedAck ./e2e/...

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestE2E_AppliedAck(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupSuggestionRepo(t), 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("app.go")

	submitSuggestions(t, p.binary, p.repo, `[
	  {"id":"s1","file":"app.go","from_line":4,"to_line":4,"original":"return \"hello world\"","proposed":"return \"hi\""}
	]`)

	// The open suggestion is visible. Accept → it COLLAPSES behind an ACCENTUATED-yellow
	// badge (is-accepted): decided, but the agent still has to apply it (#165).
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.inline-suggestion .sg-old`, chromedp.ByQuery),
		chromedp.Click(`button[name='acceptSuggestion']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.line-row:has(.inline-suggestion) .line-mark.is-accepted`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accept should collapse to an accentuated-yellow badge: %v\nstderr: %s", err, p.stderr.String())
	}
	var acceptedShown int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`[...document.querySelectorAll('.inline-suggestion.sg-accept')].filter(e=>e.offsetParent).length`, &acceptedShown))
	if acceptedShown != 0 {
		t.Errorf("an accepted suggestion should collapse (card hidden); %d visible", acceptedShown)
	}

	// The agent applies the edit and acks it → the badge goes GREEN (is-done); still collapsed.
	if out, err := exec.Command(p.binary, "applied", "--out", p.repo, "s1").CombinedOutput(); err != nil {
		t.Fatalf("prereview applied: %v\n%s", err, out)
	}
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.line-row:has(.inline-suggestion) .line-mark.is-done`, chromedp.ByQuery)); err != nil {
		t.Fatalf("suggestion badge should go green after the applied ack: %v\nstderr: %s", err, p.stderr.String())
	}
	appliedJSONL := filepath.Join(p.repo, ".prereview", "applied.jsonl")
	if b, err := os.ReadFile(appliedJSONL); err != nil || !strings.Contains(string(b), `"s1"`) {
		t.Errorf("applied ack should record s1 in applied.jsonl; err=%v content=%s", err, b)
	}
}
