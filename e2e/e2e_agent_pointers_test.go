//go:build browser

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/csv"
)

// TestE2E_SkillsAgentPointers: once a learning is accepted, prereview tells the
// agent to use the skill: a block in ~/.claude/CLAUDE.md below your own text,
// and a Claude Code hook beside your own hooks in ~/.claude/settings.json. The
// Skills tab shows both; its switch removes them, leaving your text and hooks
// exactly as they were, and turning it back on restores them. All in the
// test's fake agent home.
func TestE2E_SkillsAgentPointers(t *testing.T) {
	repo := setupFixtureRepoChecks(t, "notes.md")
	home := agentHomeFor(repo)
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	const mine = "# My rules\n\n- Keep commits small.\n"
	const settings = "{\n  \"hooks\": {\n    \"Stop\": [{\"matcher\": \"*\", \"hooks\": [{\"type\": \"command\", \"command\": \"my-stop-hook\"}]}]\n  },\n  \"effortLevel\": \"high\"\n}\n"
	mustWrite(t, filepath.Join(home, ".claude"), "CLAUDE.md", mine)
	mustWrite(t, filepath.Join(home, ".claude"), "settings.json", settings)
	claudeMD, settingsPath := filepath.Join(home, ".claude", "CLAUDE.md"), filepath.Join(home, ".claude", "settings.json")
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return string(b)
	}

	past := seedReviewStore(t, filepath.Join(t.TempDir(), "notes"),
		csv.Row{ID: "c1", File: "plan.md", FromLine: 2, ToLine: 2, Body: "Cut the filler.", CreatedAt: time.Now()})
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	id := learnPropose(t, p, "--text", "Cut filler words.", "--evidence", past+":c1")
	ctx, cancel := within(p, 60*time.Second)
	defer cancel()

	pills := `.lib-pointers .lc-agents`
	if err := chromedp.Run(ctx,
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.Click(learnAct(id, "accept"), chromedp.ByQuery),
		chromedp.WaitNotPresent(learnCard(id), chromedp.ByQuery),
		chromedp.Click(`.lib-tabs form:has(input[value="skills"]) button`, chromedp.ByQuery),
		chromedp.WaitVisible(pills, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accept, then the Skills tab: %v%s", err, diag())
	}
	if got := textOf(t, p, pills); !strings.Contains(got, "Claude Code memory ✓") || !strings.Contains(got, "Claude Code checks after each edit ✓") {
		t.Errorf("pointer pills = %q%s", got, diag())
	}
	md := read(claudeMD)
	if !strings.HasPrefix(md, mine+"\n<!-- prereview:learned-skills -->") || !strings.Contains(md, "`learned-writing`") {
		t.Errorf("CLAUDE.md:\n%s", md)
	}
	if s := read(settingsPath); !strings.Contains(s, "prereview hook check") || !strings.Contains(s, "my-stop-hook") || !strings.Contains(s, `"effortLevel": "high"`) {
		t.Errorf("settings.json:\n%s", s)
	}

	toggle := `.lib-pointers button[name="toggleAgentPointers"]`
	if err := chromedp.Run(ctx,
		chromedp.Click(toggle, chromedp.ByQuery),
		chromedp.WaitVisible(toggle+`[aria-checked="false"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("switch off: %v%s", err, diag())
	}
	if got := read(claudeMD); got != mine {
		t.Errorf("switched off, CLAUDE.md = %q, want your text exactly%s", got, diag())
	}
	s := read(settingsPath)
	if strings.Contains(s, "prereview hook check") || !strings.Contains(s, "my-stop-hook") {
		t.Errorf("switched off, settings.json:\n%s", s)
	}
	if got := textOf(t, p, pills); strings.Contains(got, "✓") {
		t.Errorf("switched off, pills still show ✓: %q", got)
	}

	if err := chromedp.Run(ctx,
		chromedp.Click(toggle, chromedp.ByQuery),
		chromedp.WaitVisible(toggle+`[aria-checked="true"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("switch on: %v%s", err, diag())
	}
	if !strings.Contains(read(claudeMD), "<!-- prereview:learned-skills -->") || !strings.Contains(read(settingsPath), "prereview hook check") {
		t.Errorf("switched back on, the block or the hook didn't return%s", diag())
	}
	noHorizontalScroll(t, p, diag)
}
