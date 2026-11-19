//go:build browser

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_DraftStaysInItsRow: typing in the composer on the last added line
// updates that row only. Every added row has old line 0, so a range keyed by
// the first part of `row-L<old>-<new>` alone gives them all one key. The diff
// then compares only the last row holding that key (the composing one) and
// sends its update under the shared key, and the client applies it to the
// FIRST added row: the draft shows as stray text in the code. Desktop and
// phone: the reviewer first saw it at phone width.
func TestE2E_DraftStaysInItsRow(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 800}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			dir := t.TempDir()
			runCmd(t, dir, "git", "init", "-q", "-b", "main")
			runCmd(t, dir, "git", "config", "user.email", "test@example.com")
			runCmd(t, dir, "git", "config", "user.name", "Test")
			runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
			mustWrite(t, dir, "retry.go", "package retry\n\nfunc Charge() error {\n\treturn nil\n}\n")
			runCmd(t, dir, "git", "add", "-A")
			runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
			// Lines 4–9 are added: six rows with old line 0.
			mustWrite(t, dir, "retry.go", "package retry\n\nfunc Charge() error {\n"+
				"\tfor attempt := 0; attempt < 3; attempt++ {\n"+
				"\t\tif err := submit(); err == nil {\n"+
				"\t\t\treturn nil\n"+
				"\t\t}\n"+
				"\t}\n"+
				"\treturn errFailed\n"+
				"}\n")

			p := bootChromeAgainstRepo(t, dir, vp.w, vp.h, "--agent")
			diag := captureDiag(t, p)
			p.waitReadyAt(vp.w, vp.h)
			p.clickFile("retry.go")
			p.clickLine(0, 9) // the LAST added row: the one a repeated key resolves to
			const draft = "Surface the gateway's real error here."
			if err := chromedp.Run(p.ctx,
				chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
				// Two debounced saves, so at least one live update is a keyed
				// per-row update rather than a whole-range resend.
				chromedp.SendKeys(`.composer textarea`, draft[:8], chromedp.ByQuery),
				chromedp.Sleep(900*time.Millisecond),
				chromedp.SendKeys(`.composer textarea`, draft[8:], chromedp.ByQuery),
				chromedp.Sleep(900*time.Millisecond),
			); err != nil {
				t.Fatalf("type the draft: %v%s", err, diag())
			}

			// No row carries text outside its own elements, and no other row shows the draft.
			stray := evalStr(t, p, `[...document.querySelectorAll('.code .line-row')].flatMap(r =>
				[...r.childNodes].filter(n => n.nodeType === 3 && n.textContent.trim()).map(n => r.dataset.key + ': ' + JSON.stringify(n.textContent.slice(0, 40)))).join('\n')`)
			if stray != "" {
				t.Errorf("text outside the row's elements:\n%s%s", stray, diag())
			}
			others := evalStr(t, p, `[...document.querySelectorAll('.code .line-row')].filter(r => !r.querySelector('.composer') && r.textContent.includes('Surface the gateway')).map(r => r.dataset.key).join(', ')`)
			if others != "" {
				t.Errorf("the draft shows in other rows: %s%s", others, diag())
			}
			if got := evalStr(t, p, `document.querySelector('.composer textarea').value`); !strings.Contains(got, draft) {
				t.Errorf("composer value = %q, want the draft%s", got, diag())
			}
		})
	}
}
