//go:build browser

// End-to-end coverage for issue #60: the Light/Dark/System color-mode toggle.
// Boots prereview, drives the toolbar theme toggle through its System → Light
// → Dark cycle, and asserts the <html data-mode> attribute plus the resulting
// chrome surface color so the whole cascade is exercised end-to-end:
//
//   - explicit Light/Dark force the surface regardless of the OS;
//   - System (no data-mode) follows prefers-color-scheme (emulated);
//   - an explicit Light opt-out still wins under an OS dark preference
//     (the :not([data-mode="light"]) guard in the dark block).
//
// Per project convention the failure path dumps browser console + server
// stderr. Set PREREVIEW_THEME_SHOTS=<dir> to also dump PNGs for visual review.
//
// Run with: go test -tags=browser -run Theme ./e2e/...

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Solarized surfaces the test pins (var(--surface) → header.bar background).
const (
	solarizedLightSurface = "rgb(253, 246, 227)" // base3  #fdf6e3
	solarizedDarkSurface  = "rgb(0, 43, 54)"     // base03 #002b36
)

func TestE2E_ThemeModeToggle(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1400, 900)

	var consoleLines []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		}
	})

	p.waitReadyAt(1400, 900)
	p.clickFile("edited.go")

	diag := func() string {
		return "\nconsole:\n" + strings.Join(consoleLines, "\n") +
			"\nserver:\n" + p.stderr.String()
	}

	// dataMode reads the live .theme-root data-mode attribute ("" when absent →
	// System). It lives on the wrapper (not <html>) so livetemplate morphs it
	// live on toggle — the whole point of the wrapper.
	dataMode := func() string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`(document.querySelector('.theme-root')?.getAttribute('data-mode')) || ''`, &v)); err != nil {
			t.Fatalf("read data-mode: %v%s", err, diag())
		}
		return v
	}
	// headerBg is the computed chrome surface — var(--surface) via Pico.
	headerBg := func() string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`getComputedStyle(document.querySelector('header.bar')).backgroundColor`, &v)); err != nil {
			t.Fatalf("read header bg: %v%s", err, diag())
		}
		return v
	}
	// cycleTheme clicks the toolbar toggle and waits for the re-render to land
	// the expected data-mode value (the action round-trips over the WebSocket).
	cycleTheme := func(want string) {
		t.Helper()
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`document.querySelector('header.bar button[name="cycleTheme"]').click()`, nil)); err != nil {
			t.Fatalf("click cycleTheme: %v%s", err, diag())
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if dataMode() == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("after cycleTheme, data-mode = %q, want %q%s", dataMode(), want, diag())
	}
	setOSDark := func(dark bool) {
		t.Helper()
		val := "light"
		if dark {
			val = "dark"
		}
		if err := chromedp.Run(p.ctx, emulation.SetEmulatedMedia().
			WithFeatures([]*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: val}})); err != nil {
			t.Fatalf("emulate prefers-color-scheme=%s: %v%s", val, err, diag())
		}
	}
	shot := func(name string) {
		dir := os.Getenv("PREREVIEW_THEME_SHOTS")
		if dir == "" {
			return
		}
		var buf []byte
		if err := chromedp.Run(p.ctx, chromedp.FullScreenshot(&buf, 90)); err != nil {
			t.Fatalf("screenshot %s: %v%s", name, err, diag())
		}
		if err := os.WriteFile(filepath.Join(dir, name), buf, 0o644); err != nil {
			t.Fatalf("write screenshot %s: %v", name, err)
		}
	}

	// OS preference starts light (headless default).
	setOSDark(false)

	// 1. Initial state is System: no data-mode attribute, light surface.
	if m := dataMode(); m != "" {
		t.Fatalf("initial data-mode = %q, want \"\" (System)%s", m, diag())
	}
	if bg := headerBg(); bg != solarizedLightSurface {
		t.Fatalf("System on OS-light: header bg = %q, want light %q%s", bg, solarizedLightSurface, diag())
	}
	shot("theme-system-light.png")

	// 2. System → Light: attribute set, still light.
	cycleTheme("light")
	if bg := headerBg(); bg != solarizedLightSurface {
		t.Fatalf("explicit Light: header bg = %q, want light %q%s", bg, solarizedLightSurface, diag())
	}

	// 3. Light → Dark: attribute set, surface flips to Solarized dark.
	cycleTheme("dark")
	if bg := headerBg(); bg != solarizedDarkSurface {
		t.Fatalf("explicit Dark: header bg = %q, want dark %q%s", bg, solarizedDarkSurface, diag())
	}
	shot("theme-explicit-dark.png")

	// 4. Dark → System: attribute cleared.
	cycleTheme("")

	// 5. System follows the OS: flip OS to dark, surface goes dark with no
	//    attribute set (the no-JS prefers-color-scheme path).
	setOSDark(true)
	if bg := headerBg(); bg != solarizedDarkSurface {
		t.Fatalf("System on OS-dark: header bg = %q, want dark %q%s", bg, solarizedDarkSurface, diag())
	}
	shot("theme-system-dark.png")

	// 6. Explicit Light opt-out wins under OS dark (:not([data-mode="light"])).
	cycleTheme("light")
	if bg := headerBg(); bg != solarizedLightSurface {
		t.Fatalf("explicit Light under OS-dark: header bg = %q, want light %q%s", bg, solarizedLightSurface, diag())
	}

	t.Logf("theme cascade verified across System/Light/Dark × OS-light/OS-dark; %d console lines", len(consoleLines))
}
