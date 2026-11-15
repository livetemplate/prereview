//go:build browser

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/csv"
)

// seedReviewStore writes another review's store (a .prereview with comments)
// under root, as a past review would have left it: the evidence a learning cites.
func seedReviewStore(t *testing.T, root string, rows ...csv.Row) string {
	t.Helper()
	store := filepath.Join(root, ".prereview")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := csv.NewWriter(filepath.Join(store, "comments.csv")).Write(rows); err != nil {
		t.Fatal(err)
	}
	return store
}

// learnPropose runs `prereview learn propose` the way the agent does, against the
// same library the server reads, and returns the new learning's id.
func learnPropose(t *testing.T, p *runningPrereview, args ...string) string {
	t.Helper()
	cmd := exec.Command(p.binary, append([]string{"learn", "propose"}, args...)...)
	cmd.Env = append(os.Environ(), "PREREVIEW_LIBRARY_DIR="+libraryDirFor(p.repo))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prereview learn propose: %v: %s", err, out)
	}
	m := regexp.MustCompile(`proposed (l[0-9a-f]+)`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("no id in %q", out)
	}
	return string(m[1])
}

// learnCard selects a learning's card; learnChip a status chip; learnAct a
// card's action button.
func learnCard(id string) string { return `.lib-card[data-key="learn-` + id + `"]` }
func learnChip(status string) string {
	return `.lib-filters form:has(input[name="status"][value="` + status + `"]) button`
}
func learnAct(id, act string) string {
	return learnCard(id) + ` form:has(input[name="act"][value="` + act + `"]) button`
}

// TestE2E_LibraryLearnings: the agent proposes a learning (the real CLI) and it
// shows up live in the open Library with the toolbar badge; Accept moves it to
// Accepted; a second proposal that --replaces it carries the conflict note, and
// accepting it supersedes the first; Restore swaps them back. Phone width.
func TestE2E_LibraryLearnings(t *testing.T) {
	const w, h = 390, 844
	repo := setupFixtureRepoChecks(t, "notes.md")
	past := seedReviewStore(t, filepath.Join(t.TempDir(), "notes"),
		csv.Row{ID: "c1", File: "plan.md", FromLine: 4, ToLine: 4, Body: "Drop “basically”: it adds nothing.", CreatedAt: time.Now().Add(-48 * time.Hour)},
		csv.Row{ID: "c2", File: "plan.md", FromLine: 9, ToLine: 9, Body: "Steps want numbers.", CreatedAt: time.Now().Add(-24 * time.Hour)})
	p := bootChromeAgainstRepo(t, repo, w, h, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(w, h)
	ctx, cancel := within(p, 40*time.Second)
	defer cancel()

	if err := chromedp.Run(ctx,
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.lib-tabs form:has(input[value="learnings"]) button.is-on`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library .ckp-empty`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open the Library on Learnings: %v%s", err, diag())
	}

	// Ungrounded evidence writes nothing.
	bad := exec.Command(p.binary, "learn", "propose", "--text", "x", "--evidence", past+":ghost")
	bad.Env = append(os.Environ(), "PREREVIEW_LIBRARY_DIR="+libraryDirFor(p.repo))
	if out, err := bad.CombinedOutput(); err == nil || !strings.Contains(string(out), "ghost") {
		t.Fatalf("a proposal citing a missing record was accepted: %v %s", err, out)
	}

	old := learnPropose(t, p, "--text", "Cut filler words.", "--scope", "Technical docs", "--evidence", past+":c1")
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(learnCard(old)+`.is-proposed`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library-trigger .lib-badge`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the proposal didn't show up live: %v%s", err, diag())
	}
	if got := textOf(t, p, learnCard(old)+` .lc-evidence`); !strings.Contains(got, "basically") || !strings.Contains(got, "plan.md L4") {
		t.Errorf("evidence row = %q, want the cited comment and where it is%s", got, diag())
	}
	if got := textOf(t, p, `.library-trigger .lib-badge`); got != "1" {
		t.Errorf("badge = %q, want 1%s", got, diag())
	}

	if err := chromedp.Run(ctx,
		chromedp.Click(learnAct(old, "accept"), chromedp.ByQuery),
		chromedp.WaitNotPresent(learnCard(old), chromedp.ByQuery),
		chromedp.WaitNotPresent(`.library-trigger .lib-badge`, chromedp.ByQuery),
		chromedp.Click(learnChip("accepted"), chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(old)+`.is-accepted`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Accept: %v%s", err, diag())
	}

	nu := learnPropose(t, p, "--text", "Cut filler words: basically, really, very.", "--scope", "Technical docs",
		"--evidence", past+":c1", "--evidence", past+":c2", "--replaces", old)
	if err := chromedp.Run(ctx,
		chromedp.Click(learnChip("proposed"), chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(nu)+` .lc-conflict`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the replacing proposal: %v%s", err, diag())
	}
	if got := textOf(t, p, learnCard(nu)+` .lc-conflict`); !strings.Contains(got, "Cut filler words.") {
		t.Errorf("conflict note = %q, want it to name the learning it replaces%s", got, diag())
	}

	if err := chromedp.Run(ctx,
		chromedp.Click(learnAct(nu, "accept"), chromedp.ByQuery),
		chromedp.WaitNotPresent(learnCard(nu), chromedp.ByQuery),
		chromedp.Click(learnChip("superseded"), chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(old)+`.is-superseded`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accepting the replacement didn't supersede the old one: %v%s", err, diag())
	}
	if got := textOf(t, p, learnCard(old)+` header`); !strings.Contains(got, "basically, really") {
		t.Errorf("superseded header = %q, want it to name what superseded it%s", got, diag())
	}

	if err := chromedp.Run(ctx,
		chromedp.Click(learnAct(old, "restore"), chromedp.ByQuery),
		chromedp.WaitNotPresent(learnCard(old), chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(nu)+`.is-superseded`, chromedp.ByQuery),
		chromedp.Click(learnChip("accepted"), chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(old)+`.is-accepted`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Restore: %v%s", err, diag())
	}

	// Edit text and scope on the card.
	if err := chromedp.Run(ctx,
		chromedp.Click(learnCard(old)+` button[name="editLearning"]`, chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(old)+` .lc-edit textarea`, chromedp.ByQuery),
		chromedp.SetValue(learnCard(old)+` .lc-edit textarea`, "Cut filler words, always.", chromedp.ByQuery),
		chromedp.Click(learnCard(old)+` button[name="saveLearning"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(learnCard(old)+` .lc-edit`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Edit: %v%s", err, diag())
	}
	if got := textOf(t, p, learnCard(old)+` .lc-text`); got != "Cut filler words, always." {
		t.Errorf("edited text = %q%s", got, diag())
	}

	// No sideways scroll at phone width.
	var sw, cw int
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.documentElement.scrollWidth`, &sw),
		chromedp.Evaluate(`document.documentElement.clientWidth`, &cw),
	); err != nil || sw > cw {
		t.Errorf("the Learnings tab scrolls sideways at %dpx: scrollWidth %d > %d (%v)%s", w, sw, cw, err, diag())
	}
}

// TestE2E_LibraryLiveAcrossSessions: two reviews (two servers) share the per-user
// library; accepting a learning in one updates the other without a reload.
func TestE2E_LibraryLiveAcrossSessions(t *testing.T) {
	repoA := setupFixtureRepoChecks(t, "notes.md")
	repoB := setupFixtureRepoChecks(t, "plan.md") // a sibling temp dir: the same library
	if libraryDirFor(repoA) != libraryDirFor(repoB) {
		t.Fatalf("the two reviews don't share a library: %s vs %s", libraryDirFor(repoA), libraryDirFor(repoB))
	}
	past := seedReviewStore(t, filepath.Join(t.TempDir(), "notes"),
		csv.Row{ID: "c1", File: "plan.md", FromLine: 4, ToLine: 4, Body: "Drop it.", CreatedAt: time.Now()})
	a := bootChromeAgainstRepo(t, repoA, 1200, 800)
	b := bootChromeAgainstRepo(t, repoB, 390, 844)
	diagA, diagB := captureDiag(t, a), captureDiag(t, b)
	a.waitReady()
	b.waitReadyAt(390, 844)
	id := learnPropose(t, a, "--text", "Cut filler words.", "--evidence", past+":c1")
	for _, s := range []struct {
		p    *runningPrereview
		diag func() string
	}{{a, diagA}, {b, diagB}} {
		ctx, cancel := within(s.p, 20*time.Second)
		err := chromedp.Run(ctx,
			chromedp.WaitVisible(`.library-trigger .lib-badge`, chromedp.ByQuery), // live, before opening
			chromedp.Click(`.library-trigger`, chromedp.ByQuery),
			chromedp.WaitVisible(learnCard(id)+`.is-proposed`, chromedp.ByQuery),
		)
		cancel()
		if err != nil {
			t.Fatalf("%s: the proposal didn't reach this review: %v%s", s.p.repo, err, s.diag())
		}
	}
	ctxA, cancelA := within(a, 20*time.Second)
	defer cancelA()
	if err := chromedp.Run(ctxA, chromedp.Click(learnAct(id, "accept"), chromedp.ByQuery)); err != nil {
		t.Fatalf("accept in A: %v%s", err, diagA())
	}
	ctxB, cancelB := within(b, 20*time.Second)
	defer cancelB()
	if err := chromedp.Run(ctxB,
		chromedp.WaitNotPresent(learnCard(id), chromedp.ByQuery),
		chromedp.WaitNotPresent(`.library-trigger .lib-badge`, chromedp.ByQuery),
		chromedp.Click(learnChip("accepted"), chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(id)+`.is-accepted`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("B didn't see A's accept: %v%s", err, diagB())
	}
}
