//go:build browser

// End-to-end coverage for the #118 agent-queue Pause/Resume keyboard shortcut.
// In --stream mode the "q" key toggles the drain between Live and Batching (the
// only Queue action that previously had no shortcut), and the pause button
// surfaces the key as a chip — both single-sourced from the keymap (StreamOnly).
// The repo-mode negative (the binding is absent without --stream) lives in
// TestE2E_KbdHintInButtons.
//
// Run with: go test -tags=browser -run PauseShortcut ./e2e/...

package e2e

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestE2E_PauseShortcut(t *testing.T) {
	// --stream renders the Queue dropdown + its Pause/Resume control, and arms
	// the StreamOnly "q" binding.
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1400, 900, "--stream")

	var mu sync.Mutex
	var consoleLines, wsFrames []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		case *cdpnetwork.EventWebSocketFrameReceived:
			wsFrames = append(wsFrames, "recv "+e.Response.PayloadData)
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+e.Response.PayloadData)
		}
	})
	if err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.Enable().Do(ctx)
	})); err != nil {
		t.Fatalf("enable network: %v", err)
	}
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}
	evalStr := func(js string) string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("eval %q: %v%s", js, err, diag())
		}
		return v
	}
	// paused polls the DOM for the Queue dropdown's paused class (the server
	// echoes AgentPaused into it on every render).
	paused := func() bool {
		for i := 0; i < 40; i++ {
			if evalStr(`String(!!document.querySelector('.queue-dropdown.is-paused'))`) == "true" {
				return true
			}
			time.Sleep(75 * time.Millisecond)
		}
		return false
	}
	unpaused := func() bool {
		for i := 0; i < 40; i++ {
			if evalStr(`String(!!document.querySelector('.queue-dropdown:not(.is-paused)'))`) == "true" &&
				evalStr(`String(!!document.querySelector('.queue-dropdown.is-paused'))`) == "false" {
				return true
			}
			time.Sleep(75 * time.Millisecond)
		}
		return false
	}

	p.waitReadyAt(1400, 900)

	// The Queue dropdown renders (stream mode) and starts Live (not paused).
	if evalStr(`String(!!document.querySelector('.queue-dropdown'))`) != "true" {
		t.Fatalf("Queue dropdown missing in stream mode%s", diag())
	}
	if evalStr(`String(!!document.querySelector('.queue-dropdown.is-paused'))`) != "false" {
		t.Fatalf("queue should start Live (not paused)%s", diag())
	}
	// The StreamOnly window binding is armed.
	if evalStr(`String(!!document.querySelector('.kbd-bindings [lvt-on\\:window\\:keydown="toggleAgentPause"]'))`) != "true" {
		t.Fatalf("stream mode should arm the Pause/Resume window binding%s", diag())
	}

	// "q" pauses (Live → Batching).
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent("q")); err != nil {
		t.Fatalf(`press "q": %v%s`, err, diag())
	}
	if !paused() {
		t.Fatalf(`"q" did not pause the queue (no .queue-dropdown.is-paused)%s`, diag())
	}

	// "q" again resumes (Batching → Live).
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent("q")); err != nil {
		t.Fatalf(`press "q" again: %v%s`, err, diag())
	}
	if !unpaused() {
		t.Fatalf(`second "q" did not resume the queue (still paused)%s`, diag())
	}

	// The pause button surfaces the same key as a chip (single-sourced from the
	// keymap). textContent is read directly — independent of the panel being open
	// or the chip's pointer-media visibility.
	if got := evalStr(`(()=>{const el=document.querySelector('.queue-pause-btn .kbd-hint');return el?el.textContent.trim():'MISSING'})()`); got != "q" {
		t.Errorf(`Pause button chip = %q, want "q" (single-sourced from keymap)%s`, got, diag())
	}

	mu.Lock()
	for _, l := range consoleLines {
		if strings.Contains(strings.ToLower(l), "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
	if strings.Contains(p.stderr.String(), "panic") {
		t.Fatalf("server logged a panic:%s", diag())
	}
}
