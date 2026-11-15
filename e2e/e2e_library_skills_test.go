//go:build browser

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
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
	drafts := t.TempDir()
	mustWrite(t, drafts, "plain.yml", "extends: existence\nname: Plain talk\nmessage: \"Say what it is instead of '%s'.\"\ntokens: [Plain]\n")
	checker := filepath.Join(drafts, "plain.yml")
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
	rule := filepath.Join(learnedChecksDirFor(repo), "learned-"+id+".yml")
	if b, err := os.ReadFile(rule); err != nil || !strings.Contains(string(b), "Plain talk") {
		t.Fatalf("the checker isn't in the learned checks (%v): %s", err, b)
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

	noHorizontalScroll(t, p, diag)
}

// TestE2E_ImportSkill: a skill you made (with plugin-eval graders) in the fake
// agent home is offered on the Skills tab; Import reads it by reference — its
// llm grader becomes a judge and its not_contains regex a checker, both in your
// checks — a learning scoped to it shows as an overlay with Export patch, and
// the skill's folder is byte-identical afterwards.
func TestE2E_ImportSkill(t *testing.T) {
	repo := setupFixtureRepoChecks(t, "notes.md")
	home := agentHomeFor(repo)
	dir := filepath.Join(home, ".claude", "skills", "house-style")
	files := map[string]string{
		"SKILL.md": "---\nname: house-style\ndescription: \"Our house style\"\n---\n\nBe brief.\n",
		"evals/notes/prompt.md":              "Write release notes.\n",
		"evals/notes/graders/reader-first.md": "---\ntype: llm\n---\n\nPASS if it leads with what changed for the reader.\n",
		"evals/notes/graders/no-utilize.md":   "---\ntype: regex\npattern: utilize\nflags: i\nmatch: not_contains\n---\n",
		"evals/notes/graders/ran-tests.md":    "---\ntype: tool_used\ntool: Bash\n---\n",
	}
	for rel, body := range files {
		mustWrite(t, dir, rel, body)
	}
	past := seedReviewStore(t, filepath.Join(t.TempDir(), "notes"),
		csv.Row{ID: "c1", File: "notes.md", FromLine: 1, ToLine: 1, Body: "Lead with the change.", CreatedAt: time.Now()})
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	ctx, cancel := within(p, 40*time.Second)
	defer cancel()

	skillsTab := `.lib-tabs form:has(input[value="skills"]) button`
	if err := chromedp.Run(ctx,
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.Click(skillsTab, chromedp.ByQuery),
		chromedp.WaitVisible(`.lib-card[data-key="skill-found"] button[name="importSkill"]`, chromedp.ByQuery),
		chromedp.Click(`.lib-card[data-key="skill-found"] button[name="importSkill"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.lib-card[data-key="skill-imported-house-style"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("import: %v%s", err, diag())
	}
	card := `.lib-card[data-key="skill-imported-house-style"]`
	if got := textOf(t, p, card); !strings.Contains(got, "2 checks read from its evals/") || !strings.Contains(got, "unchanged since import") {
		t.Errorf("imported card = %q%s", got, diag())
	}
	if got := textOf(t, p, card+` .lc-drafted`); !strings.Contains(got, "1 grader not used") {
		t.Errorf("the tool_used grader isn't reported as unused: %q%s", got, diag())
	}
	rules, _ := filepath.Glob(filepath.Join(learnedChecksDirFor(repo), "learned-imported-house-style-*"))
	if len(rules) != 2 {
		t.Fatalf("checks written from its evals: %v", rules)
	}

	id := learnPropose(t, p, "--text", "Lead with what changed.", "--scope", "house-style", "--evidence", past+":c1")
	if err := chromedp.Run(ctx,
		chromedp.Click(`.lib-tabs form:has(input[value="learnings"]) button`, chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(id), chromedp.ByQuery),
		chromedp.Click(learnAct(id, "accept"), chromedp.ByQuery),
		chromedp.WaitNotPresent(learnCard(id), chromedp.ByQuery),
		chromedp.Click(skillsTab, chromedp.ByQuery),
		chromedp.WaitVisible(card+` a[href^="/_library-skill-patch"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("overlay: %v%s", err, diag())
	}
	var patch string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`fetch(document.querySelector('`+card+` a[href^="/_library-skill-patch"]').href).then(r => r.text())`, &patch,
		func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams { return p.WithAwaitPromise(true) })); err != nil {
		t.Fatalf("export patch: %v%s", err, diag())
	}
	if !strings.Contains(patch, "+## Learned preferences") || !strings.Contains(patch, "+- Lead with what changed.") || !strings.HasPrefix(patch, "--- a/SKILL.md") {
		t.Fatalf("patch:\n%s", patch)
	}
	if strings.Contains(textOf(t, p, `.lib-card[data-key="skill-learned-writing"]`), "Lead with what changed") {
		t.Error("the overlay learning leaked into the generated Writing skill")
	}

	for rel, body := range files {
		if b, err := os.ReadFile(filepath.Join(dir, rel)); err != nil || string(b) != body {
			t.Fatalf("the imported skill's %s changed: %v %q", rel, err, b)
		}
	}
	var n int
	_ = filepath.Walk(dir, func(string, os.FileInfo, error) error { n++; return nil })
	if n != len(files)+4 { // the files plus dir, evals/, evals/notes/, graders/
		t.Errorf("the imported skill's folder gained or lost files (%d entries)", n)
	}
}

// learnedChecksDirFor is where a test server's learnings write their checks
// (skills.LearnedChecksDir of its library).
func learnedChecksDirFor(repo string) string {
	return filepath.Join(libraryDirFor(repo), "skills", "checks")
}
