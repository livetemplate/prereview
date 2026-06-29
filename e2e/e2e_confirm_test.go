//go:build browser

// End-to-end coverage for issue #58: the irreversible session terminators
// (End session / Quit) must route through a confirm <dialog> instead of firing
// on the first click, so an accidental tap can't kill the server. This drives
// the standalone Quit flow end to end:
//
//   - the dialog is CLOSED on load (no auto-open);
//   - the toolbar button OPENS it (and does NOT stop the server);
//   - Cancel CLOSES it and leaves the server running (the whole point of #58);
//   - only the dialog's own confirm button actually quits.
//
// The stream-mode End session confirm path (trigger → dialog → endSession) is
// covered by TestE2E_StreamHandoff. Both reuse the same no-JS native-<dialog>
// invoker pattern (command="show-modal" / command="close") as deleteDialog.

package e2e

import (
	"strings"
	"testing"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestE2E_QuitConfirmDialog(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1400, 900)

	var consoleLines []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			consoleLines = append(consoleLines, string(e.Type)+" "+joinArgs(e.Args))
		}
	})

	p.waitReadyAt(1400, 900)

	diag := func() string {
		return "\nconsole:\n" + strings.Join(consoleLines, "\n") + "\nserver:\n" + p.stderr.String()
	}
	boolEval := func(js string) bool {
		var v bool
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("eval %q: %v%s", js, err, diag())
		}
		return v
	}

	// 1. The dialog is rendered (standalone mode shows Quit) but CLOSED on load.
	if !boolEval(`!!document.querySelector('#confirm-quit')`) {
		t.Fatalf("confirm-quit dialog not rendered in standalone mode%s", diag())
	}
	if boolEval(`document.querySelector('#confirm-quit').open`) {
		t.Fatalf("confirm-quit dialog is open on load — it must require a trigger%s", diag())
	}
	if boolEval(`!!document.querySelector('.banner-stopping')`) {
		t.Fatalf("server-stopping banner present before any Quit%s", diag())
	}

	// 2. The toolbar Quit button OPENS the dialog — and must NOT stop the server.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`header.bar button[commandfor='confirm-quit']`, chromedp.ByQuery),
		chromedp.WaitVisible(`#confirm-quit[open]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open quit dialog: %v%s", err, diag())
	}
	if boolEval(`!!document.querySelector('.banner-stopping')`) {
		t.Fatalf("opening the dialog stopped the server — it must only confirm%s", diag())
	}

	// 3. Cancel CLOSES the dialog and the server keeps running (issue #58's point).
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`#confirm-quit button[command='close']`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`#confirm-quit[open]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("cancel quit dialog: %v%s", err, diag())
	}
	if boolEval(`!!document.querySelector('.banner-stopping')`) {
		t.Fatalf("Cancel stopped the server — the accidental-quit guard is broken%s", diag())
	}

	// 4. Re-open and CONFIRM → the real quit fires (Server-stopping banner shows).
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`header.bar button[commandfor='confirm-quit']`, chromedp.ByQuery),
		chromedp.WaitVisible(`#confirm-quit[open]`, chromedp.ByQuery),
		chromedp.Click(`#confirm-quit button[name='quit']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.banner-stopping`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("confirm quit: %v%s", err, diag())
	}

	t.Logf("quit confirm verified (closed→open→cancel-keeps-alive→confirm-quits); %d console lines",
		len(consoleLines))
}
