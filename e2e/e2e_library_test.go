//go:build browser

package e2e

import (
	"fmt"
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

// TestE2E_FirstLaunch: on a machine that hasn't answered the offer, the review
// lists the past reviews found under $HOME (here a seeded fake home), each
// ticked; "Read with my agent" queues ONE extraction request naming exactly the
// ticked stores. The agent then answers it as written — citing a thread reply by
// the request's `<target_id>@<at>` format — and the learning shows in the Library.
func TestE2E_FirstLaunch(t *testing.T) {
	const w, h = 390, 844
	repo := setupFixtureRepoChecks(t, "notes.md")
	home := scanHomeFor(repo)
	notes := seedReviewStore(t, filepath.Join(home, "notes"),
		csv.Row{ID: "c1", File: "plan.md", FromLine: 4, ToLine: 4, Body: "Drop “basically”.", CreatedAt: time.Now()})
	const replyAt = int64(1762000000000000000)
	if err := os.WriteFile(filepath.Join(notes, "reviewer-replies.jsonl"),
		[]byte(`{"target_id":"c1","body":"still filler","at":1762000000000000000}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := seedReviewStore(t, filepath.Join(home, "code", "app"),
		csv.Row{ID: "a1", File: "README.md", FromLine: 1, ToLine: 1, Body: "Say what it does first.", CreatedAt: time.Now()},
		csv.Row{ID: "a2", File: "README.md", FromLine: 2, ToLine: 2, Body: "Shorter.", CreatedAt: time.Now()})
	private := seedReviewStore(t, filepath.Join(home, "private"),
		csv.Row{ID: "p1", File: "diary.md", FromLine: 1, ToLine: 1, Body: "personal", CreatedAt: time.Now()})
	seedReviewStore(t, filepath.Join(home, "proj", "node_modules", "dep"), // skipped by the scan
		csv.Row{ID: "n1", File: "x.md", FromLine: 1, ToLine: 1, Body: "never listed", CreatedAt: time.Now()})

	p := bootChromeAgainstRepo(t, repo, w, h, "--agent")
	diag := captureDiag(t, p)
	p.waitReadyAt(w, h)
	ctx, cancel := within(p, 40*time.Second)
	defer cancel()
	item := func(store string) string { return `.boot-list li[data-key="boot-` + store + `"] button` }
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.boot`, chromedp.ByQuery),
		chromedp.WaitVisible(item(notes)+`[aria-checked="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(item(app)+`[aria-checked="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(item(private)+`[aria-checked="true"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the first-launch offer didn't list the past reviews: %v%s", err, diag())
	}
	if n := countSel(t, p, `.boot-list li`); n != 3 {
		t.Errorf("listed %d past reviews, want 3 (node_modules is skipped)%s", n, diag())
	}
	if err := chromedp.Run(ctx,
		chromedp.Click(item(private), chromedp.ByQuery),
		chromedp.WaitVisible(item(private)+`[aria-checked="false"]`, chromedp.ByQuery),
		chromedp.Click(`button[name="readPastReviews"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.boot[aria-label="Reading past reviews"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Read with my agent: %v%s", err, diag())
	}

	rows, err := csv.Read(filepath.Join(p.store, "comments.csv"))
	if err != nil {
		t.Fatal(err)
	}
	var requests []string
	for _, r := range rows {
		if strings.HasPrefix(r.Body, "Read past reviews and propose learnings") {
			requests = append(requests, r.Body)
		}
	}
	if len(requests) != 1 {
		t.Fatalf("queued %d extraction requests, want 1%s", len(requests), diag())
	}
	req := requests[0]
	if !strings.Contains(req, "`"+notes+"`") || !strings.Contains(req, "`"+app+"`") || strings.Contains(req, private) {
		t.Fatalf("the request must name exactly the ticked stores:\n%s", req)
	}
	if !strings.Contains(req, "<target_id>@<at>") || !strings.Contains(req, "prereview learn propose") {
		t.Fatalf("the request doesn't say how to cite a reply or what to run:\n%s", req)
	}

	// The agent answers as the request says.
	id := learnPropose(t, p, "--text", "Cut filler words.", "--evidence", notes+":c1",
		"--evidence", fmt.Sprintf("%s:c1@%d", notes, replyAt))
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.boot[aria-label="Reading past reviews"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library-trigger .lib-badge`, chromedp.ByQuery),
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(id)+`.is-proposed`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the agent's learning didn't show: %v%s", err, diag())
	}
	if got := textOf(t, p, learnCard(id)+` .lc-evidence`); !strings.Contains(got, "still filler") {
		t.Errorf("the reply evidence didn't resolve: %q%s", got, diag())
	}

	// The answer is recorded for the machine (applyLibrary hides the offer once
	// bootstrap.json exists; the state unit test pins that).
	if _, err := os.Stat(filepath.Join(libraryDirFor(repo), "bootstrap.json")); err != nil {
		t.Errorf("the answer wasn't recorded: %v", err)
	}
}
