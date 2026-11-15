//go:build browser

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/csv"
)

// TestE2E_LearningProducesSkillAndCheck: the agent proposes a learning with a
// drafted checker (the real CLI); the card shows the checker before you accept;
// accepting it writes the line into the generated Writing skill — installed for
// the agent that's set up (Claude, in the fake agent home), not for the ones that
// aren't — and the checker joins your checks and fires on the open file. Phone.
func TestE2E_LearningProducesSkillAndCheck(t *testing.T) {
	const w, h = 390, 844
	repo := setupFixtureRepoChecks(t, "notes.md") // "Plain line N." lines
	home := agentHomeFor(repo)
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil { // Claude is set up here
		t.Fatal(err)
	}
	past := seedReviewStore(t, filepath.Join(t.TempDir(), "notes"),
		csv.Row{ID: "c1", File: "plan.md", FromLine: 2, ToLine: 2, Body: "Don't call it plain, say what it is.", CreatedAt: time.Now()})
	checker := filepath.Join(t.TempDir(), "plain.yml")
	if err := os.WriteFile(checker, []byte("extends: existence\nname: Plain talk\nmessage: \"Say what it is instead of '%s'.\"\ntokens: [Plain]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := bootChromeAgainstRepo(t, repo, w, h)
	diag := captureDiag(t, p)
	p.waitReadyAt(w, h)
	id := learnPropose(t, p, "--text", "Say what a thing is, not that it's plain.", "--evidence", past+":c1", "--checker", checker)
	ctx, cancel := within(p, 40*time.Second)
	defer cancel()

	if err := chromedp.Run(ctx,
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(id)+` .lc-drafted`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the proposal and its checker: %v%s", err, diag())
	}
	if got := textOf(t, p, learnCard(id)+` .lc-drafted pre`); !strings.Contains(got, "tokens: [Plain]") {
		t.Errorf("the card doesn't show the checker it adds: %q%s", got, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(learnAct(id, "accept"), chromedp.ByQuery),
		chromedp.WaitNotPresent(learnCard(id), chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Accept: %v%s", err, diag())
	}

	skill := filepath.Join(home, ".claude", "skills", "learned-writing", "SKILL.md")
	b, err := os.ReadFile(skill)
	if err != nil || !strings.Contains(string(b), "- Say what a thing is, not that it's plain. <!-- learning "+id+" -->") {
		var tree []string
		_ = filepath.Walk(home, func(p string, fi os.FileInfo, _ error) error {
			if fi != nil {
				tree = append(tree, fmt.Sprintf("%s %d", p, fi.Size()))
			}
			return nil
		})
		t.Fatalf("the Writing skill for Claude doesn't carry the learning (%v):\n%s\ntree:\n%s%s", err, b, strings.Join(tree, "\n"), diag())
	}
	if _, err := os.Stat(filepath.Join(home, ".agents")); !os.IsNotExist(err) {
		t.Errorf("installed for an agent that isn't set up (~/.agents)")
	}
	rule := filepath.Join(checksDirFor(repo), "learned-"+id+".yml")
	if b, err := os.ReadFile(rule); err != nil || !strings.Contains(string(b), "Plain talk") {
		t.Fatalf("the checker isn't in your checks folder (%v): %s", err, b)
	}

	if err := chromedp.Run(ctx,
		chromedp.Click(`.lib-tabs form:has(input[value="skills"]) button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.lib-card[data-key="skill-learned-writing"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the Skills tab: %v%s", err, diag())
	}
	if got := textOf(t, p, `.lib-card[data-key="skill-learned-writing"] .lc-agents`); !strings.Contains(got, "Claude Code ✓") || !strings.Contains(got, "Cursor — not set up here") {
		t.Errorf("install states = %q%s", got, diag())
	}
	if got := textOf(t, p, `.lib-card[data-key="skill-learned-writing"] .lc-row`); !strings.Contains(got, "1 instruction") || !strings.Contains(got, "1 checker") {
		t.Errorf("skill counts = %q%s", got, diag())
	}

	// The checker runs: the file's bar lists it as failing.
	if err := chromedp.Run(ctx,
		chromedp.Click(`.lib-back button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.ck-strip`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("back to the file: %v%s", err, diag())
	}
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(textOf(t, p, `.ck-strip`), "Plain talk") {
		if time.Now().After(deadline) {
			t.Fatalf("the learned checker never fired on the file: bar = %q%s", textOf(t, p, `.ck-strip`), diag())
		}
		time.Sleep(250 * time.Millisecond)
	}

	var sw, cw int
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.documentElement.scrollWidth`, &sw),
		chromedp.Evaluate(`document.documentElement.clientWidth`, &cw),
	); err != nil || sw > cw {
		t.Errorf("scrolls sideways at %dpx: %d > %d (%v)%s", w, sw, cw, err, diag())
	}
}
