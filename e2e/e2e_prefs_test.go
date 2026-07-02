//go:build browser

// End-to-end coverage for issue #88 item 1: the durable per-user view prefs.
//
// The bug: prereview keeps view prefs (theme scheme, light/dark mode, focus,
// file-view, show-resolved) in the framework's IN-MEMORY session store. A page
// reload survives (same process), but prereview is a CLI the user relaunches
// constantly — often from a phone — and every relaunch spawns a fresh empty
// store, so the old cookie misses and every pref resets to its default. The fix
// writes these prefs to a per-user file on disk (internal/review/uiprefs.go),
// bypassing the session entirely.
//
// This test proves BOTH halves against a real browser + real binary:
//   - reload (same server) keeps the chosen scheme + mode, and
//   - RELAUNCH (kill the server, start a NEW one against the same repo/prefs
//     file) still restores them — the actual fix.
// Both server launches share one PREREVIEW_UI_PREFS_PATH because prefsIsolatedEnv
// keys it off the repo dir, exactly mirroring a relaunch against the same repo.
//
// Run with: go test -tags=browser -run ViewPrefs ./e2e/...

package e2e

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestE2E_ViewPrefsSurviveRelaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}
	chromium := findChromium(t)
	repo := setupFixtureRepo(t)
	binary := filepath.Join(t.TempDir(), "prereview")
	if out, err := exec.Command("go", "build", "-o", binary, "..").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// One browser spanning both server lifetimes.
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.WindowSize(1200, 800),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer allocCancel()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	var consoleLines []string
	chromedp.ListenTarget(ctx, func(ev any) {
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

	attr := func(sel, name string) string {
		var v string
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`(document.querySelector('`+sel+`')?.getAttribute('`+name+`')) || ''`, &v)); err != nil {
			t.Fatalf("read %s[%s]: %v\nconsole:\n%s", sel, name, err, strings.Join(consoleLines, "\n"))
		}
		return v
	}
	dataScheme := func() string { return attr(".theme-root", "data-scheme") }
	dataMode := func() string { return attr(".theme-root", "data-mode") }

	// clickAndAwait clicks a header toolbar button and polls until the given
	// probe returns want (the toggle round-trips over the WebSocket).
	clickAndAwait := func(button string, probe func() string, want, stderr string) {
		t.Helper()
		if err := chromedp.Run(ctx, chromedp.Evaluate(
			`document.querySelector('header.bar button[name="`+button+`"]').click()`, nil)); err != nil {
			t.Fatalf("click %s: %v", button, err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if probe() == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("after clicking %s, got %q want %q\nserver stderr:\n%s\nconsole:\n%s",
			button, probe(), want, stderr, strings.Join(consoleLines, "\n"))
	}

	waitReady := func(url string) {
		if err := chromedp.Run(ctx,
			chromedp.EmulateViewport(1200, 800),
			chromedp.Navigate(url),
			chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
			// Let the deferred client connect over WS + attach delegation before
			// we start clicking toolbar buttons.
			chromedp.Sleep(400*time.Millisecond),
		); err != nil {
			t.Fatalf("waitReady %s: %v\nconsole:\n%s", url, err, strings.Join(consoleLines, "\n"))
		}
	}

	// ---- Server #1: set non-default prefs, then reload (same process). ----
	url1, srv1, stderr1 := startPrereview(t, binary, repo)
	waitReady(url1)

	if got := dataScheme(); got != "solarized" {
		t.Fatalf("initial scheme = %q, want solarized (empty prefs → default)", got)
	}
	// Solarized → Gruvbox.
	clickAndAwait("cycleScheme", dataScheme, "gruvbox", stderr1.String())
	// System → Light → Dark (two clicks).
	clickAndAwait("cycleTheme", dataMode, "light", stderr1.String())
	clickAndAwait("cycleTheme", dataMode, "dark", stderr1.String())

	// Reload regression: same server, full navigation.
	waitReady(url1)
	if s, m := dataScheme(), dataMode(); s != "gruvbox" || m != "dark" {
		t.Fatalf("after reload: scheme=%q mode=%q, want gruvbox/dark\nstderr:\n%s", s, m, stderr1.String())
	}

	// ---- Relaunch: kill server #1, boot server #2 against the SAME repo. ----
	// prefsIsolatedEnv keys the prefs file off the repo dir, so server #2 shares
	// the file server #1 wrote — this is the cross-relaunch path under test.
	_ = srv1.Process.Kill()
	_, _ = srv1.Process.Wait()

	url2, srv2, stderr2 := startPrereview(t, binary, repo)
	defer func() { _ = srv2.Process.Kill(); _, _ = srv2.Process.Wait() }()
	waitReady(url2)

	if s, m := dataScheme(), dataMode(); s != "gruvbox" || m != "dark" {
		t.Fatalf("after RELAUNCH: scheme=%q mode=%q, want gruvbox/dark — durable prefs not restored from disk\nstderr:\n%s\nconsole:\n%s",
			s, m, stderr2.String(), strings.Join(consoleLines, "\n"))
	}
}
