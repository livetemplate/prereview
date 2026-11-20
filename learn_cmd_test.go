package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
)

// TestLearnWait: `learn wait` claims a queued job and prints it, prints `idle`
// at its timeout, and `stop` once the review --out names has ended; it marks a
// learner running while it waits. `learn finish` closes the job.
func TestLearnWait(t *testing.T) {
	dir := t.TempDir()
	learnWaitPoll = 10 * time.Millisecond
	t.Cleanup(func() { learnWaitPoll = time.Second })

	t.Setenv("PREREVIEW_LIBRARY_DIR", dir) // never the real per-user library
	var out bytes.Buffer
	if err := runLearn([]string{"wait", "--timeout", "30ms"}, &out); err != nil || out.String() != "idle\n" {
		t.Fatalf("no job: %q %v, want idle", out.String(), err)
	}
	if !library.LearnerAlive(dir) {
		t.Fatal("a waiting learner isn't marked running")
	}

	store := t.TempDir()
	j, _, err := library.QueueJob(dir, library.Job{Stores: []string{store}, AutoAccept: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runLearn([]string{"wait"}, &out); err != nil {
		t.Fatal(err)
	}
	head, prompt, _ := strings.Cut(out.String(), "\n\n")
	if head != "job "+j.ID || !strings.Contains(prompt, "prereview learn finish "+j.ID) || !strings.Contains(prompt, "`"+store+"`") {
		t.Fatalf("claimed job printed as:\n%s", out.String())
	}
	if got, _ := library.JobByID(dir, j.ID); got.Op != library.JobClaimed {
		t.Fatalf("job is %s after wait, want claimed", got.Op)
	}

	out.Reset()
	if err := runLearn([]string{"finish", j.ID}, &out); err != nil || len(library.OpenJobs(dir)) != 0 {
		t.Fatalf("finish: %q %v, open %+v", out.String(), err, library.OpenJobs(dir))
	}
	if err := runLearn([]string{"finish"}, &out); err == nil {
		t.Fatal("finish with no id")
	}

	// The review ends: its event log's last line is `end`.
	repo := t.TempDir()
	sd := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(sd, review.EventsFileName)
	if err := os.WriteFile(events, []byte(`{"seq":1,"event":"snapshot"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runLearn([]string{"wait", "--out", sd, "--timeout", "30ms"}, &out); err != nil || out.String() != "idle\n" {
		t.Fatalf("a running review: %q %v, want idle", out.String(), err)
	}
	if err := os.WriteFile(events, []byte(`{"seq":1,"event":"snapshot"}`+"\n"+`{"seq":2,"event":"end"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runLearn([]string{"wait", "--out", sd}, &out); err != nil || out.String() != "stop\n" {
		t.Fatalf("an ended review: %q %v, want stop", out.String(), err)
	}
}

// TestLearnPropose_RefusesAgentText: citing the agent's reply is refused with a
// message saying what to cite, and nothing is written to the library.
func TestLearnPropose_RefusesAgentText(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PREREVIEW_LIBRARY_DIR", dir) // never the real per-user library
	store := filepath.Join(t.TempDir(), ".prereview")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, library.AgentRepliesFile),
		[]byte(`{"target_id":"c1","body":"Cut it from the intro.","at":1761000000000000000}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runLearn([]string{"propose", "--text", "Keep replies short.", "--evidence", store + ":c1@1761000000000000000"}, &out)
	if err == nil || !strings.Contains(err.Error(), "the agent's reply") {
		t.Fatalf("propose citing an agent reply: %v (%q), want a refusal naming the agent's reply", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, library.LearningsFile)); !os.IsNotExist(err) {
		t.Fatalf("the refused proposal wrote the library: %v", err)
	}
}
