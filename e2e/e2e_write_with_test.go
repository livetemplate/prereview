//go:build browser

package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
)

// lastWriteWith is the newest snapshot's write_with.
func lastWriteWith(evs []review.StreamEvent) map[string][]string {
	snaps := handoffEvents(evs)
	if len(snaps) == 0 {
		return nil
	}
	return snaps[len(snaps)-1].WriteWith
}

// TestE2E_WriteWithMidReview: a lesson about the agent's replies, accepted in the
// Library while the review runs, becomes the learned-agent-replies skill, and the
// next snapshot tells the agent to load it before it writes a reply (after
// learned-writing, which this library doesn't have). The lesson rests on the reviewer's reply on a thread, never on
// the agent's own reply.
func TestE2E_WriteWithMidReview(t *testing.T) {
	const w, h = 1200, 800
	repo := setupFixtureRepoChecks(t, "notes.txt") // the diff view: commentable lines
	past := seedReviewStore(t, filepath.Join(t.TempDir(), "notes"),
		csv.Row{ID: "c1", File: "plan.md", FromLine: 4, ToLine: 4, Body: "Drop “basically”.", CreatedAt: time.Now().Add(-time.Hour)})
	if err := os.WriteFile(filepath.Join(past, "agent-replies.jsonl"),
		[]byte(`{"target_id":"c1","author":"agent","body":"I went through the paragraph and refactored the wording so that it reads better overall.","at":1761000000000000000}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(past, "reviewer-replies.jsonl"),
		[]byte(`{"target_id":"c1","body":"Too long: just say what you changed.","at":1762000000000000000}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := bootChromeAgainstRepo(t, repo, w, h, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	events := func() string { b, _ := os.ReadFile(filepath.Join(p.store, "events.jsonl")); return string(b) }

	addLineComment(t, p, 2, 2, "first comment")
	waitEvents(t, "events.jsonl", events, func(evs []review.StreamEvent) bool { return len(handoffEvents(evs)) > 0 }, "a first snapshot", diag)
	if got := lastWriteWith(parseStreamEvents(events()))["reply"]; got != nil {
		t.Fatalf("write_with.reply = %v before any lesson, want none", got)
	}

	// The agent's own reply is context: citing it is refused.
	if out, err := learnProposeErr(p, "--text", "Keep replies short.", "--scope", "Agent replies", "--evidence", past+":c1@1761000000000000000"); err == nil {
		t.Fatalf("a lesson citing the agent's reply was accepted: %s", out)
	}
	id := learnPropose(t, p, "--text", "Say what you changed in one line.", "--scope", "Agent replies", "--glob", "reply.md",
		"--evidence", past+":c1@1762000000000000000")
	ctx, cancel := within(p, 30*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(learnCard(id)+`.is-proposed`, chromedp.ByQuery),
		chromedp.Click(learnAct(id, "accept"), chromedp.ByQuery),
		chromedp.WaitNotPresent(learnCard(id), chromedp.ByQuery),
		chromedp.Click(`.library-trigger`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.lib-tabs`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("accept the lesson: %v%s", err, diag())
	}
	if _, err := os.Stat(filepath.Join(libraryDirFor(repo), "skills", "learned-agent-replies", "SKILL.md")); err != nil {
		t.Fatalf("no learned-agent-replies skill: %v%s", err, diag())
	}

	addLineComment(t, p, 3, 3, "second comment")
	waitEvents(t, "events.jsonl", events, func(evs []review.StreamEvent) bool {
		return slices.Equal(lastWriteWith(evs)["reply"], []string{"learned-agent-replies"})
	}, "a snapshot naming learned-agent-replies for replies", diag)
}

// TestE2E_ExternalWriteWith: a live-site review (--external) has no Library of
// its own, yet its snapshots name the skills to write the agent's text with,
// read from the per-user library, including a lesson accepted while it runs.
func TestE2E_ExternalWriteWith(t *testing.T) {
	app := newLiveApp(t)
	out := t.TempDir()
	learnings := filepath.Join(libraryDirFor(out), library.LearningsFile)
	accept := func(id, scope string) {
		t.Helper()
		l := library.Learning{ID: id, Text: "One line.", Scope: scope, Status: library.StatusAccepted,
			CreatedAt: library.Now(), UpdatedAt: library.Now(), Machine: library.Machine()}
		if err := library.Append(learnings, l); err != nil {
			t.Fatal(err)
		}
	}
	accept("l1", "Agent replies")
	p := bootChromeExternalOut(t, app.srv.URL, out, 1200, 900, "--agent")
	diag := captureDiag(t, p)
	events := func() string { b, _ := os.ReadFile(filepath.Join(p.store, "events.jsonl")); return string(b) }

	annotateRegion(t, p, "CTA too low contrast", diag)
	waitEvents(t, "events.jsonl", events, func(evs []review.StreamEvent) bool {
		return slices.Equal(lastWriteWith(evs)["reply"], []string{"learned-agent-replies"})
	}, "an external snapshot naming learned-agent-replies", diag)

	accept("l2", "Suggestions") // accepted elsewhere while this review runs
	annotateRegion(t, p, "Heading too small", diag)
	waitEvents(t, "events.jsonl", events, func(evs []review.StreamEvent) bool {
		return slices.Equal(lastWriteWith(evs)["suggestion"], []string{"learned-suggestions"})
	}, "the next snapshot naming learned-suggestions", diag)
}
