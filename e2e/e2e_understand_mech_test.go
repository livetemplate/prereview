//go:build browser

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/device"
	"github.com/livetemplate/prereview/csv"
)

// mechCount is how many mechanical fold rows the diff shows.
const mechCount = `document.querySelectorAll('.code .mech-row').length`

// setupMechRepo: rename.go has every one of its 12 lines changed (a rename), and
// other.go one line, with an open comment on rename.go's new line 6.
func setupMechRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	var before, after []string
	for i := 1; i <= 12; i++ {
		before = append(before, fmt.Sprintf("// Client line %d", i))
		after = append(after, fmt.Sprintf("// Uploader line %d", i))
	}
	mustWrite(t, dir, "rename.go", strings.Join(before, "\n")+"\n")
	mustWrite(t, dir, "other.go", "package other\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "rename.go", strings.Join(after, "\n")+"\n")
	mustWrite(t, dir, "other.go", "package other // changed\n")
	seedReviewStore(t, dir, csv.Row{ID: "c1", File: "rename.go", FromLine: 6, ToLine: 6, Side: "new",
		Body: "check this one", Kind: "line", CreatedAt: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)})
	return dir
}

// TestE2E_MechanicalFold (#115 A′3): lines a mechanical concern explains fold
// behind "Show N mechanical lines", live; a commented line inside stays visible
// and splits the fold; the View-menu switch turns folds off and on; a tap opens a
// fold; and a jump into a fold leaves it open after the jump's reveal ends.
func TestE2E_MechanicalFold(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupMechRepo(t), 1200, 900, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("rename.go")

	submitExplain(t, p, `{
  "concerns": [{"id": "ren", "title": "Rename Client to Uploader", "mechanical": true}, {"id": "o", "title": "Other"}],
  "steps": [
    {"title": "Rename the type", "concern": "ren", "anchors": [{"file": "rename.go", "from_line": 1, "to_line": 12}, {"file": "rename.go", "side": "old", "from_line": 1, "to_line": 12}]},
    {"title": "Touch other", "concern": "o", "anchors": [{"file": "other.go", "from_line": 1}]}
  ]
}`)
	waitJSTrueDiag(t, p.ctx, mechCount+` > 0`, 10*time.Second, "mechanical folds appear live", diag)
	labels := evalStr(t, p, `[...document.querySelectorAll('.code .mech-row')].map(r => r.textContent.trim()).join(' | ')`)
	if !strings.Contains(labels, "mechanical lines · Rename Client to Uploader") {
		t.Errorf("fold labels = %q, want a count and the concern's title%s", labels, diag())
	}
	if !strings.Contains(evalStr(t, p, `document.querySelector('.code').textContent`), "check this one") {
		t.Errorf("the comment on line 6 must stay visible inside the fold%s", diag())
	}
	if n := evalInt(t, p, `document.querySelectorAll('.code .line[data-line="6"][data-side="new"]').length`); n != 1 {
		t.Errorf("the commented line 6 must not fold, found %d rows%s", n, diag())
	}
	if n := evalInt(t, p, `document.querySelectorAll('.code .line[data-line="9"][data-side="new"]').length`); n != 0 {
		t.Errorf("line 9 must be folded, found %d rows%s", n, diag())
	}

	// The View menu (desktop) and the ⋮ menu (its phone mirror) both offer it.
	if n := evalInt(t, p, `document.querySelectorAll("button[name='toggleFoldMechanical']").length`); n != 2 {
		t.Errorf("the switch must be in the View menu and the ⋮ menu, found %d%s", n, diag())
	}
	// The View-menu switch turns every fold off, and back on.
	toggle := `document.querySelector("button[name='toggleFoldMechanical']").click()`
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(toggle, nil)); err != nil {
		t.Fatalf("toggle folds off: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, mechCount+` === 0`, 5*time.Second, "folds off", diag)
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(toggle, nil)); err != nil {
		t.Fatalf("toggle folds on: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, mechCount+` > 0`, 5*time.Second, "folds back on", diag)

	// On a phone the fold row is a full-width, ≥44px tap target.
	if err := chromedp.Run(p.ctx, chromedp.Emulate(device.IPhone11), chromedp.Sleep(250*time.Millisecond)); err != nil {
		t.Fatalf("emulate phone (touch): %v", err)
	}
	if evalInt(t, p, `matchMedia('(pointer: coarse)').matches ? 1 : 0`) != 1 {
		t.Fatalf("(pointer: coarse) not active under device emulation; the size check would be vacuous%s", diag())
	}
	if got := evalStr(t, p, `(() => { const b = document.querySelector('.code .mech-row button').getBoundingClientRect(), c = document.querySelector('.code').getBoundingClientRect(); return (Math.abs(b.width - c.width) <= 1 && b.height >= 44) ? 'ok' : b.width + 'x' + b.height + ' in ' + c.width; })()`); got != "ok" {
		t.Errorf("phone fold row = %s, want full width and ≥44px tall%s", got, diag())
	}

	// A tap opens the fold (the whole hunk) for the visit.
	if err := chromedp.Run(p.ctx, chromedp.Click(`.code .mech-row button`, chromedp.ByQuery)); err != nil {
		t.Fatalf("tap a fold: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, mechCount+` === 0 && document.querySelectorAll('.code .line[data-line="9"][data-side="new"]').length === 1`, 5*time.Second, "the tapped fold opens", diag)
}

// TestE2E_MechanicalFoldJump: a jump into a folded line opens its fold, and the
// fold stays open after the jump's whole-file reveal ends (a file switch).
func TestE2E_MechanicalFoldJump(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupMechRepo(t), 1200, 900, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("rename.go")
	submitExplain(t, p, `{
  "concerns": [{"id": "ren", "title": "Rename", "mechanical": true}],
  "steps": [
    {"title": "Rename the type", "concern": "ren", "anchors": [{"file": "rename.go", "from_line": 1, "to_line": 12}]},
    {"title": "The middle", "concern": "ren", "anchors": [{"file": "rename.go", "from_line": 9}]}
  ]
}`)
	waitJSTrueDiag(t, p.ctx, mechCount+` > 0`, 10*time.Second, "folds appear", diag)

	p.openUnderstandTab("walkthrough")
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`document.querySelector('form:has(input[name="line"][value="9"]) button[name="jumpToAnchor"]').click()`, nil)); err != nil {
		t.Fatalf("jump to line 9: %v%s", err, diag())
	}
	waitJSTrueDiag(t, p.ctx, `!!document.querySelector('.code .line.is-cursor[data-line="9"]')`, 5*time.Second, "the cursor lands on line 9", diag)
	p.clickFile("other.go")
	p.clickFile("rename.go")
	waitJSTrueDiag(t, p.ctx, `!!document.querySelector('.code .line[data-line="9"][data-side="new"]')`, 5*time.Second, "after the reveal ends, line 9's fold stays open", diag)
	if n := evalInt(t, p, mechCount); n != 0 {
		t.Errorf("rename.go's hunk was opened by the jump, but %d fold rows remain%s", n, diag())
	}
}
