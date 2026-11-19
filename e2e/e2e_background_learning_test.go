//go:build browser

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/library"
)

// learnCLI runs a `prereview learn` verb the way the background learner does,
// against the same library the servers read, and returns its output.
func learnCLI(t *testing.T, p *runningPrereview, args ...string) string {
	t.Helper()
	cmd := exec.Command(p.binary, append([]string{"learn"}, args...)...)
	cmd.Env = prefsIsolatedEnv(p.repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prereview learn %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// waitJob waits for the library to hold an open learning job and returns it.
func waitJob(t *testing.T, dir string, diag func() string) library.Job {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if js := library.OpenJobs(dir); len(js) > 0 {
			return js[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no learning job was queued%s", diag())
	return library.Job{}
}

// TestE2E_BackgroundLearning: once a review holds enough of your comments and
// its agent goes idle, the review queues a learning job — not a comment on the
// file you're editing. The background learner (played here through the real
// CLI) claims it and proposes two learnings, which are accepted on arrival:
// every review sharing the library announces them, they reach your skills, and
// Undo takes them back out everywhere.
func TestE2E_BackgroundLearning(t *testing.T) {
	repoA := setupFixtureRepoChecks(t, "notes.md")
	repoB := setupFixtureRepoChecks(t, "plan.md") // a sibling temp dir: the same library
	home := agentHomeFor(repoA)
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil { // Claude is set up here
		t.Fatal(err)
	}
	var rows []csv.Row
	for i := 1; i <= 5; i++ {
		rows = append(rows, csv.Row{ID: fmt.Sprint("c", i), File: "notes.md", FromLine: i + 1, ToLine: i + 1,
			Body: "Cut the filler.", CreatedAt: time.Now()})
	}
	store := seedReviewStore(t, repoA, rows...)
	if err := library.MarkBootstrapped(libraryDirFor(repoA), "dismissed"); err != nil { // no first-launch offer
		t.Fatal(err)
	}
	a := bootChromeAgainstRepo(t, repoA, 1200, 800, "--agent")
	b := bootChromeAgainstRepo(t, repoB, 390, 844)
	diagA, diagB := captureDiag(t, a), captureDiag(t, b)
	a.waitReady()
	b.waitReadyAt(390, 844)

	// The agent attaches and is idle: the review queues the job itself.
	heartbeat(t, a, 0, 500*time.Millisecond)
	j := waitJob(t, libraryDirFor(repoA), diagA)
	if len(j.Stores) != 1 || j.Stores[0] != store || !j.AutoAccept {
		t.Fatalf("job = %+v, want one auto-accept job for %s", j, store)
	}
	ctxA, cancelA := within(a, 30*time.Second)
	defer cancelA()
	if err := chromedp.Run(ctxA, chromedp.WaitVisible(`.boot[aria-label="Reading past reviews"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the open job doesn't show as being read: %v%s", err, diagA())
	}
	disk, err := csv.Read(filepath.Join(store, "comments.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if len(disk) != len(rows) {
		t.Fatalf("comments.csv has %d rows, want the %d seeded (a job is not a comment)%s", len(disk), len(rows), diagA())
	}
	var onFile int
	if err := chromedp.Run(ctxA, chromedp.Evaluate(`document.querySelectorAll('.file-comments').length +
		[...document.querySelectorAll('.inline-comment')].filter(c => /learning job|past reviews/i.test(c.textContent)).length`, &onFile)); err != nil {
		t.Fatal(err)
	}
	if onFile != 0 {
		t.Fatalf("%d request cards on the file%s", onFile, diagA())
	}

	// The learner claims it and answers.
	out := learnCLI(t, a, "wait", "--timeout", "0")
	head, prompt, _ := strings.Cut(out, "\n\n")
	if head != "job "+j.ID || !strings.Contains(prompt, "prereview learn propose --job "+j.ID) || !strings.Contains(prompt, "accepted as you propose it") {
		t.Fatalf("`learn wait` printed:\n%s", out)
	}
	var ids []string
	for _, text := range []string{"Cut filler words.", "Lead with the point."} {
		o := learnCLI(t, a, "propose", "--job", j.ID, "--text", text, "--evidence", store+":c1")
		if !strings.HasPrefix(o, "accepted l") {
			t.Fatalf("propose: %q, want accepted", o)
		}
		ids = append(ids, strings.Fields(o)[1])
	}
	learnCLI(t, a, "finish", j.ID)

	for _, s := range []struct {
		p    *runningPrereview
		diag func() string
	}{{a, diagA}, {b, diagB}} {
		ctx, cancel := within(s.p, 20*time.Second)
		err := chromedp.Run(ctx, chromedp.WaitVisible(`.learn-toast`, chromedp.ByQuery))
		cancel()
		if err != nil {
			t.Fatalf("%s: no notice: %v%s", s.p.repo, err, s.diag())
		}
		if got := textOf(t, s.p, `.learn-toast .toast-msg`); got != "2 learnings added" {
			t.Errorf("%s: notice = %q%s", s.p.repo, got, s.diag())
		}
		if got := textOf(t, s.p, `.library-trigger .lib-badge`); got != "2" {
			t.Errorf("%s: badge = %q, want 2%s", s.p.repo, got, s.diag())
		}
	}
	if err := chromedp.Run(ctxA, chromedp.WaitNotPresent(`.boot[aria-label="Reading past reviews"]`, chromedp.ByQuery)); err != nil {
		t.Errorf("the finished job still shows as being read: %v%s", err, diagA())
	}
	skill := filepath.Join(home, ".claude", "skills", "learned-writing", "SKILL.md")
	waitFile := func(want func(string) bool, what string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if b, _ := os.ReadFile(skill); want(string(b)) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		b, _ := os.ReadFile(skill)
		t.Fatalf("%s; SKILL.md:\n%s", what, b)
	}
	waitFile(func(s string) bool {
		return strings.Contains(s, "- Cut filler words. <!-- learning "+ids[0]+" -->") && strings.Contains(s, "- Lead with the point.")
	}, "the accepted learnings didn't reach the skill")

	// Opening the Library hides the notice but doesn't count as seeing it:
	// Learnings opens on the proposed ones, where these aren't.
	if err := chromedp.Run(ctxA,
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.learn-toast`, chromedp.ByQuery),
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.learn-toast`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("opening and closing the Library: %v%s", err, diagA())
	}
	if n := countSel(t, b, `.learn-toast`); n != 1 {
		t.Fatalf("opening the Library in one review cleared the notice in another%s", diagB())
	}

	// Undo in one review: both rejected, the notice gone everywhere, the skill lines out.
	if err := chromedp.Run(ctxA,
		chromedp.Click(`.learn-toast button[name="undoLearned"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.learn-toast`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("Undo: %v%s", err, diagA())
	}
	ctxB, cancelB := within(b, 20*time.Second)
	defer cancelB()
	if err := chromedp.Run(ctxB, chromedp.WaitNotPresent(`.learn-toast`, chromedp.ByQuery)); err != nil {
		t.Fatalf("the other review kept the notice after Undo: %v%s", err, diagB())
	}
	for _, l := range library.LoadLearnings(libraryDirFor(repoA)) {
		if l.Status != library.StatusRejected {
			t.Errorf("%s (%s) is %s after Undo, want rejected", l.ID, l.Text, l.Status)
		}
	}
	waitFile(func(s string) bool { return !strings.Contains(s, "Cut filler words.") }, "Undo left the learning in the skill")
	if n := countSel(t, a, `.library-trigger .lib-badge`); n != 0 {
		t.Errorf("the badge stayed after Undo%s", diagA())
	}

	// Seen stays seen.
	if err := chromedp.Run(ctxA, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	a.waitReady()
	if n := countSel(t, a, `.learn-toast`); n != 0 {
		t.Errorf("the notice came back after a reload%s", diagA())
	}
	noHorizontalScroll(t, b, diagB)
}
