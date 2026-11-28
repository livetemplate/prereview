//go:build browser

package e2e

import (
	"testing"
)

// TestE2E_LiveFillsTheScreen: in a combined session the live site takes all
// the height below the toolbar, on a phone and a desktop, and the page footer
// hides; back on the files the footer returns.
func TestE2E_LiveFillsTheScreen(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"phone", 390, 844}, {"desktop", 1200, 900}} {
		t.Run(vp.name, func(t *testing.T) {
			app := newLiveApp(t)
			p := bootChromeCombined(t, setupImportRepo(t), app.srv.URL, vp.w, vp.h, "--agent")
			diag := captureDiag(t, p)
			pickSurface(t, p, true, diag)
			waitJSDiag(t, p, `getComputedStyle(document.querySelector('.app-footer')).display === 'none'`, "the footer hides on Live", diag)
			if gap := evalInt(t, p, `Math.round(window.innerHeight - document.querySelector('.ext-stage').getBoundingClientRect().bottom)`); gap > 16 || gap < 0 {
				t.Errorf("the live site stops %dpx above the bottom of the screen, want 0-16%s", gap, diag())
			}
			pickSurface(t, p, false, diag)
			waitJSDiag(t, p, `getComputedStyle(document.querySelector('.app-footer')).display !== 'none'`, "the footer is back on the files", diag)
		})
	}
}
