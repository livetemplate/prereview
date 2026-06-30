//go:build browser

// End-to-end coverage for the P6 Theme axis: the curated color-scheme picker
// (Solarized → Gruvbox → Catppuccin). Boots prereview, drives the toolbar
// palette button through its cycle, and asserts the .theme-root data-scheme
// attribute plus the resulting chrome surface color so the whole cascade is
// exercised end-to-end — the scheme swap recolors chrome with no JS and no CSS
// refetch (/syntax.css already carries every scheme).
//
// It also screenshots each scheme in light + dark (System mode following an
// emulated OS preference) with alerts.md selected, so the file drawer (status
// badges + diff-stats) and the Markdown alert callouts are both in frame —
// those component colors are guarded by human screenshot review, not by
// assertions. Set PREREVIEW_THEME_SHOTS=<dir> to dump the PNGs.
//
// Run with: go test -tags=browser -run Scheme ./e2e/...

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Per-scheme chrome surface (var(--surface) → header.bar background), light and
// dark. KEEP IN SYNC with the [data-scheme] token blocks in prereview.css.
type schemeSurfaces struct {
	name        string
	lightHeader string
	darkHeader  string
	// darkInverse is --pico-primary-inverse in DARK mode — the text color on
	// the accent-filled primary button. It must FLIP per scheme: Solarized's
	// dark accent stays dark so the label is light, but Gruvbox/Catppuccin dark
	// accents are LIGHT so their label must be dark. This is the one component
	// the standalone-mode screenshots can't show (no filled primary on screen).
	darkInverse string
}

var schemeCases = []schemeSurfaces{
	{"solarized", "rgb(253, 246, 227)", "rgb(0, 43, 54)", "rgb(253, 246, 227)"}, // #fdf6e3 / #002b36 / inv #fdf6e3 (light)
	{"gruvbox", "rgb(251, 241, 199)", "rgb(40, 40, 40)", "rgb(40, 40, 40)"},     // #fbf1c7 / #282828 / inv #282828 (dark flip)
	{"catppuccin", "rgb(239, 241, 245)", "rgb(30, 30, 46)", "rgb(30, 30, 46)"},  // #eff1f5 / #1e1e2e / inv #1e1e2e (dark flip)
}

// alertsDoc is a Markdown file exercising all five GitHub alert callouts, so
// the per-scheme md-alert-accent colors (set in each scheme's dark block) are
// in frame for the screenshots.
const alertsDoc = "# Alerts\n\n" +
	"> [!NOTE]\n> A note callout.\n\n" +
	"> [!TIP]\n> A tip callout.\n\n" +
	"> [!IMPORTANT]\n> An important callout.\n\n" +
	"> [!WARNING]\n> A warning callout.\n\n" +
	"> [!CAUTION]\n> A caution callout.\n"

// setupFixtureSchemeRepo seeds a repo with varied git statuses (Modified,
// Deleted, Added) so the file drawer shows status badges + diff-stats, plus an
// alerts.md exercising every alert callout — the two component-color surfaces
// the screenshots must cover (they're guarded by human review, not assertions).
func setupFixtureSchemeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	mustWrite(t, dir, "keep.go", "package keep\n")
	mustWrite(t, dir, "edited.go", "package edited\n\nfunc Hello() string {\n\treturn \"hi\"\n}\n")
	mustWrite(t, dir, "gone.go", "package gone\n\nfunc Bye() {}\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	// Mutations producing M / D / A badges in the drawer.
	mustWrite(t, dir, "edited.go", "package edited\n\nfunc Hello() string {\n\treturn \"hello world\"\n}\n")
	runCmd(t, dir, "git", "rm", "-q", "gone.go")
	mustWrite(t, dir, "fresh.go", "package fresh\n\nfunc New() {}\n")
	mustWrite(t, dir, "alerts.md", alertsDoc)
	return dir
}

func TestE2E_SchemePicker(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureSchemeRepo(t), 1400, 900)

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
	// alerts.md puts the GitHub alert callouts in the reading pane while the
	// desktop drawer shows status badges (M/D/A) + diff-stats — both
	// component-color surfaces in one frame for the screenshots.
	p.clickFile("alerts.md")

	diag := func() string {
		return "\nconsole:\n" + strings.Join(consoleLines, "\n") +
			"\nserver:\n" + p.stderr.String()
	}

	// dataScheme reads the live .theme-root data-scheme attribute. It lives on
	// the wrapper (not <html>) so livetemplate morphs it live on cycle.
	dataScheme := func() string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`(document.querySelector('.theme-root')?.getAttribute('data-scheme')) || ''`, &v)); err != nil {
			t.Fatalf("read data-scheme: %v%s", err, diag())
		}
		return v
	}
	headerBg := func() string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`getComputedStyle(document.querySelector('header.bar')).backgroundColor`, &v)); err != nil {
			t.Fatalf("read header bg: %v%s", err, diag())
		}
		return v
	}
	// cycleScheme clicks the toolbar palette button and waits for the re-render
	// to land the expected data-scheme value (round-trips over the WebSocket).
	cycleScheme := func(want string) {
		t.Helper()
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`document.querySelector('header.bar button[name="cycleScheme"]').click()`, nil)); err != nil {
			t.Fatalf("click cycleScheme: %v%s", err, diag())
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if dataScheme() == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("after cycleScheme, data-scheme = %q, want %q%s", dataScheme(), want, diag())
	}
	dataMode := func() string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`(document.querySelector('.theme-root')?.getAttribute('data-mode')) || ''`, &v)); err != nil {
			t.Fatalf("read data-mode: %v%s", err, diag())
		}
		return v
	}
	// cycleTheme clicks the sun/moon toggle and waits for the expected data-mode.
	// Used to force an EXPLICIT [data-mode="dark"] render (vs the @media System
	// path the surface loop exercises).
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
	// computedColor reads getComputedStyle(sel).color for the first match.
	computedColor := func(sel string) string {
		var v string
		js := fmt.Sprintf(`getComputedStyle(document.querySelector(%q)).color`, sel)
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("read color of %s: %v%s", sel, err, diag())
		}
		return v
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
		// Let the compositor settle the prefers-color-scheme restyle before
		// capturing — the very first OS-dark emulation after page load can
		// otherwise paint a one-frame light flash on form elements. The header
		// assertion already proved the dark surface is computed; this only makes
		// the human-signoff PNG faithful.
		var buf []byte
		if err := chromedp.Run(p.ctx, chromedp.Sleep(300*time.Millisecond), chromedp.FullScreenshot(&buf, 90)); err != nil {
			t.Fatalf("screenshot %s: %v%s", name, err, diag())
		}
		if err := os.WriteFile(filepath.Join(dir, name), buf, 0o644); err != nil {
			t.Fatalf("write screenshot %s: %v", name, err)
		}
	}

	// Mode stays System throughout; OS preference (emulated) drives light vs dark
	// so the data-scheme cascade is what's under test, orthogonally.
	setOSDark(false)

	// 1. Default scheme is Solarized (Schemes[0]) — no pref persisted yet.
	if s := dataScheme(); s != "solarized" {
		t.Fatalf("initial data-scheme = %q, want \"solarized\"%s", s, diag())
	}

	// 2. Cycle through every scheme in registry order, asserting the chrome
	//    surface flips in both light and dark, and screenshotting each ×mode.
	for i, sc := range schemeCases {
		// The first case is the already-active Solarized default; the rest are
		// reached by clicking the palette button (which wraps after the last).
		if i > 0 {
			cycleScheme(sc.name)
		}

		setOSDark(false)
		if bg := headerBg(); bg != sc.lightHeader {
			t.Fatalf("%s light: header bg = %q, want %q%s", sc.name, bg, sc.lightHeader, diag())
		}
		shot("scheme-" + sc.name + "-light.png")

		setOSDark(true)
		if bg := headerBg(); bg != sc.darkHeader {
			t.Fatalf("%s dark: header bg = %q, want %q%s", sc.name, bg, sc.darkHeader, diag())
		}
		shot("scheme-" + sc.name + "-dark.png")
		setOSDark(false)
	}

	// 3. One more click wraps back to the first scheme — proves the cycle is
	//    closed and registry-driven, not a dead-end list.
	cycleScheme("solarized")

	// 4. Primary-button inverse FLIP. Open the file composer (it renders an
	//    accent-filled .save-btn whose label color is --pico-primary-inverse)
	//    and, in DARK mode, assert each scheme's Save label matches its expected
	//    inverse. Catches a dark-on-dark (or light-on-light) primary button that
	//    no surface assertion or standalone screenshot would surface.
	setOSDark(true)
	if err := chromedp.Run(p.ctx,
		// "Comment on file" lives in the sticky file header now, not the toolbar.
		chromedp.Evaluate(`document.querySelector('.file-head-comment button[name="openFileComment"]').click()`, nil),
		chromedp.WaitVisible(`.composer .save-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open file composer: %v%s", err, diag())
	}
	saveBtnColor := func() string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`getComputedStyle(document.querySelector('.composer .save-btn')).color`, &v)); err != nil {
			t.Fatalf("read save-btn color: %v%s", err, diag())
		}
		return v
	}
	for i, sc := range schemeCases {
		if i > 0 {
			cycleScheme(sc.name)
		}
		// The composer persists across scheme cycles (CommentMode is persisted);
		// wait for the re-render to repaint the button before reading its color.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && saveBtnColor() != sc.darkInverse {
			time.Sleep(50 * time.Millisecond)
		}
		if c := saveBtnColor(); c != sc.darkInverse {
			t.Fatalf("%s dark: primary Save label color = %q, want inverse %q "+
				"(--pico-primary-inverse must flip for a light dark-accent)%s",
				sc.name, c, sc.darkInverse, diag())
		}
	}

	// 5. Hero surface: syntax highlighting on a code DIFF, exercised through the
	//    EXPLICIT [data-mode="dark"] block. Everything above held mode = System,
	//    so only the @media(prefers-color-scheme:dark) twin of each dark block
	//    was ever matched; here the OS pref is LIGHT and the mode is forced Dark,
	//    so a dark render can come ONLY from the explicit block — covering the
	//    KEEP-IN-SYNC twin and the chroma syntax palette that leads the design.
	setOSDark(false)
	p.clickFile("edited.go") // a Go file → keyword tokens (.chroma .k)
	// System → Light → Dark: two clicks to the explicit Dark mode.
	cycleTheme("light")
	cycleTheme("dark")
	// The inverse-flip loop above left the scheme at the last case; re-seat it at
	// solarized so the loop's i==0 (no-cycle) branch starts where it expects.
	if dataScheme() != "solarized" {
		cycleScheme("solarized")
	}

	keywordColors := map[string]string{}
	for i, sc := range schemeCases {
		if i > 0 {
			cycleScheme(sc.name)
		}
		if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.content.chroma .k`, chromedp.ByQuery)); err != nil {
			t.Fatalf("%s: no highlighted keyword token rendered: %v%s", sc.name, err, diag())
		}
		// The keyword token must take a real syntax color, not fall through to the
		// inherited body text — that's the proof /syntax.css actually APPLIES (the
		// P5 bug was correct-CSS-that-didn't-apply). Compare against the plain
		// content color on the same row.
		kw := computedColor(`.content.chroma .k`)
		plain := computedColor(`.content.chroma`)
		if kw == "" || kw == plain {
			t.Fatalf("%s explicit-dark: keyword color %q == body %q — syntax CSS not applying%s",
				sc.name, kw, plain, diag())
		}
		keywordColors[sc.name] = kw
		shot("scheme-" + sc.name + "-code-dark.png")
	}
	// Scheme-scoped syntax must differ across schemes: if /syntax.css weren't
	// scoped (or didn't apply), every scheme's .k would resolve to the same color.
	if keywordColors["solarized"] == keywordColors["gruvbox"] &&
		keywordColors["gruvbox"] == keywordColors["catppuccin"] {
		t.Fatalf("keyword color identical across all schemes (%v) — syntax not scheme-scoped%s",
			keywordColors, diag())
	}

	t.Logf("scheme cascade verified: %d schemes × light/dark surface + primary-inverse flip + "+
		"explicit-dark syntax (%v); %d console lines", len(schemeCases), keywordColors, len(consoleLines))
}
