//go:build browser

// End-to-end coverage for the in-button keyboard-shortcut hints (issue #89):
// each toolbar button that fires a shortcut shows the key in its label (e.g.
// Focus `.`), single-sourced from the keymap via KeyHint. The chips are hidden
// on a coarse (touch) pointer — no keyboard, no hint.
//
// Run with: go test -tags=browser -run KbdHint ./e2e/...

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

func TestE2E_KbdHintInButtons(t *testing.T) {
	repo := setupFixtureRepo(t)
	// Seed comments so the "All comments" + "Show resolved" items render.
	pdir := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url\n" +
		"c1,edited.go,3,3,new,open one,2026-06-30T12:00:00Z,false,,,line,,\n" +
		"c2,edited.go,1,1,new,done one,2026-06-30T12:00:00Z,true,,,line,,\n"
	if err := os.WriteFile(filepath.Join(pdir, "comments.csv"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	p := bootChromeAgainstRepo(t, repo, 1400, 900)
	p.waitReadyAt(1400, 900)
	p.clickFile("edited.go") // a file open surfaces Prev/Next + Comment-on-file

	diag := func() string { return "\n--- server ---\n" + p.stderr.String() }
	evalStr := func(js string) string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &v)); err != nil {
			t.Fatalf("eval %q: %v%s", js, err, diag())
		}
		return v
	}

	// A desktop (fine) pointer so the chips are shown, matching a mouse user.
	if err := chromedp.Run(p.ctx, emulation.SetEmulatedMedia().
		WithFeatures([]*emulation.MediaFeature{{Name: "pointer", Value: "fine"}, {Name: "any-pointer", Value: "fine"}})); err != nil {
		t.Fatalf("emulate fine pointer: %v", err)
	}

	// Open the View dropdown so its menu items are in the layout.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.tb-dropdown-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tb-dropdown.open .tb-dropdown-panel`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open View dropdown: %v%s", err, diag())
	}

	// The chip for a button is single-sourced from the keymap: the KeyHint for
	// the button's action == the Display key. Assert the rendered chip text
	// matches the expected key for each shortcut-bearing button.
	cases := []struct{ sel, wantKey, where string }{
		{`.tb-dropdown-panel button[name="toggleFocusMode"] .kbd-hint`, ".", "Focus"},
		{`.tb-dropdown-panel button[name="toggleCommentList"] .kbd-hint`, "a", "All comments"},
		{`.tb-dropdown-panel button[name="toggleShowResolved"] .kbd-hint`, "r", "Show resolved"},
		{`.nav-prev button .kbd-hint`, "k", "Prev"},
		{`.nav-next button .kbd-hint`, "j", "Next"},
		{`.file-head-comment button .kbd-hint`, "c", "Comment on file"},
	}
	for _, c := range cases {
		got := evalStr(fmt.Sprintf(`(()=>{const el=document.querySelector(%q);return el?el.textContent.trim():'MISSING'})()`, c.sel))
		if got != c.wantKey {
			t.Errorf("%s chip = %q, want %q (single-sourced from keymap)%s", c.where, got, c.wantKey, diag())
		}
	}

	// The chip is inside the label so it reads "Focus .", but aria-hidden keeps
	// it out of the accessible name (which stays "Focus", not "Focus period").
	if v := evalStr(`document.querySelector('.tb-dropdown-panel button[name="toggleFocusMode"] .kbd-hint').getAttribute('aria-hidden')`); v != "true" {
		t.Errorf("Focus chip aria-hidden = %q, want true%s", v, diag())
	}

	// Shown on a fine pointer…
	if d := evalStr(`getComputedStyle(document.querySelector('.tb-dropdown-panel button[name="toggleFocusMode"] .kbd-hint')).display`); d == "none" {
		t.Errorf("chip hidden on a fine pointer (display:none)%s", diag())
	}
	// …and a button WITHOUT a shortcut (cycleScheme) shows no chip.
	if evalStr(`String(!!document.querySelector('.tb-dropdown-panel button[name="cycleScheme"] .kbd-hint'))`) != "false" {
		t.Errorf("cycleScheme (no shortcut) unexpectedly has a chip%s", diag())
	}

	// Coarse-pointer hiding (@media (pointer: coarse)) is not exercised here:
	// Chrome's Emulation.setEmulatedMedia only overrides prefers-* features, not
	// `pointer` (that needs full device emulation), so a forced coarse pointer
	// wouldn't flip the query. It's a standard media rule, left to manual check
	// on a real touch device.

	// #118: the composer Save/Cancel buttons surface their existing shortcuts
	// (Mod+Enter saves, Esc cancels) as chips — those rows fire from their own
	// template bindings, so KeyHint keys them by Button, not Action. Open the
	// file composer with "c" and assert both chips (single-sourced from keymap).
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent("c"),
		chromedp.WaitVisible(`.composer .save-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open file composer: %v%s", err, diag())
	}
	for _, c := range []struct{ sel, wantKey, where string }{
		{`.composer .save-btn .kbd-hint`, "⌘/Ctrl + Enter", "Save"},
		{`.composer .cancel-btn .kbd-hint`, "Esc", "Cancel"},
	} {
		got := evalStr(fmt.Sprintf(`(()=>{const el=document.querySelector(%q);return el?el.textContent.trim():'MISSING'})()`, c.sel))
		if got != c.wantKey {
			t.Errorf("%s chip = %q, want %q (single-sourced from keymap)%s", c.where, got, c.wantKey, diag())
		}
	}

	// #118 stream-gating: the agent-queue Pause/Resume shortcut is StreamOnly, so
	// in this repo-mode (no --agent) session its hidden window binding must NOT
	// be emitted — a repo-only reviewer gets no phantom key for a queue they lack.
	// (The positive case — the binding present + "q" toggling pause — is
	// TestE2E_PauseShortcut.)
	if evalStr(`String(!!document.querySelector('.kbd-bindings [lvt-on\\:window\\:keydown="toggleAgentPause"]'))`) != "false" {
		t.Errorf("repo mode unexpectedly emits the stream-only Pause/Resume window binding%s", diag())
	}

	if strings.Contains(p.stderr.String(), "panic") {
		t.Fatalf("server logged a panic:%s", diag())
	}
}
