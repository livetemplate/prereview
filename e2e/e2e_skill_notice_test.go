//go:build browser

// End-to-end: the #208 skill notice. A launch that refreshes one agent's
// prereview skill and finds another's edited shows both in a banner above the
// page, which covers nothing, and is gone after Dismiss for the rest of the
// launch (a reload doesn't bring it back).
// Run with: go test -tags=browser -run TestE2E_SkillNotice ./e2e/...

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/internal/skills"
)

// seedSkills puts, in the isolated agent home a server for dir will use, a
// Claude skill from an older prereview (no ownership record: it gets adopted
// and refreshed) and a Cursor rule prereview wrote that has since been edited
// by hand (left alone and reported).
func seedSkills(t *testing.T, dir string) {
	t.Helper()
	home := agentHomeFor(dir)
	mustWrite(t, home, ".claude/skills/prereview/SKILL.md", "# skill from an older prereview\n")
	rule := filepath.Join(home, ".cursor", "rules", "prereview.mdc")
	if err := skills.Own(libraryDirFor(dir), func(o *skills.Owner) error {
		return o.Put(rule, []byte("prereview's rule\n"), 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, home, ".cursor/rules/prereview.mdc", "my own rule\n")
}

// checkNotice asserts the banner's two rows, then dismisses it and checks it
// stays gone across a reload.
func checkNotice(t *testing.T, p *runningPrereview, diag func() string, ready chromedp.Action) {
	t.Helper()
	var text string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.skill-notice`, chromedp.ByQuery),
		chromedp.Text(`.skill-notice`, &text, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("no skill notice: %v%s", err, diag())
	}
	for _, want := range []string{
		"Claude Code: refreshed",
		"cursor-agent: edited there — not updated",
		"prereview --install-skill --client=cursor",
		"Reload the skill in your agent",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("notice missing %q:\n%s", want, text)
		}
	}

	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.skill-notice button[name="dismissSkillNotice"]`, chromedp.ByQuery),
		chromedp.Poll(`!document.querySelector('.skill-notice')`, nil, chromedp.WithPollingTimeout(5*time.Second)),
	); err != nil {
		t.Fatalf("dismiss didn't remove the notice: %v%s", err, diag())
	}
	var present bool
	if err := chromedp.Run(p.ctx,
		chromedp.Reload(),
		ready,
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Evaluate(`!!document.querySelector('.skill-notice')`, &present),
	); err != nil {
		t.Fatalf("reload: %v%s", err, diag())
	}
	if present {
		t.Errorf("the notice came back after a reload%s", diag())
	}
}

func TestE2E_SkillNotice(t *testing.T) {
	repo := setupFixtureRepo(t)
	seedSkills(t, repo)
	p, _, _ := bootChromeStreamRepo(t, repo)
	diag := captureDiag(t, p)
	p.waitReady()

	if got := p.stderr.String(); !strings.Contains(got, "refreshed the Claude Code integration") || !strings.Contains(got, "cursor-agent integration was not updated") {
		t.Errorf("stderr doesn't report the sync:\n%s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(agentHomeFor(repo), ".cursor", "rules", "prereview.mdc")); string(b) != "my own rule\n" {
		t.Errorf("the edited rule was overwritten: %q", b)
	}

	// Pause the queue so the sticky paused prompt shows too, then check the
	// notice covers none of the toolbar, the file view or that prompt: a display
	// check passes even when one element sits on top of another.
	var covered string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.activeElement&&document.activeElement.blur&&document.activeElement.blur()`, nil),
		chromedp.KeyEvent("q"),
		chromedp.WaitVisible(`.paused-prompt`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const n = document.querySelector('.skill-notice').getBoundingClientRect();
			return ['header.bar', 'main.viewer', '.paused-prompt'].filter(sel => {
				const b = document.querySelector(sel).getBoundingClientRect();
				return b.width > 0 && !(n.bottom <= b.top || b.bottom <= n.top || n.right <= b.left || b.right <= n.left);
			}).join(', ');
		})()`, &covered),
	); err != nil {
		t.Fatalf("pause: %v%s", err, diag())
	}
	if covered != "" {
		t.Errorf("the skill notice overlaps %s%s", covered, diag())
	}

	checkNotice(t, p, diag, chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery))
}

func TestE2E_SkillNoticeExternal(t *testing.T) {
	app := newLiveApp(t)
	out := t.TempDir()
	seedSkills(t, out)
	p := bootChromeExternalOut(t, app.srv.URL, out, 1200, 900)
	diag := captureDiag(t, p)
	if err := chromedp.Run(p.ctx, chromedp.Navigate(p.url)); err != nil {
		t.Fatalf("navigate: %v%s", err, diag())
	}
	checkNotice(t, p, diag, chromedp.WaitVisible(`.bar-external`, chromedp.ByQuery))
}
