//go:build browser

// End-to-end test for the agent-facing CLI subcommands against a LIVE --agent
// server: a real browser comment generates a real handoff, then `prereview
// comments --json` enumerates it, `prereview events --since` delivers the
// snapshot event, and `prereview processed` validates ids (a real id marks; a
// bogus id fails non-zero — the #1 corruption regression, through a real store).
// This is the server↔CLI loop the unit/contract tests can't reach: only the UI
// produces a genuine event.
// Run with: go test -tags=browser -run TestE2E_AgentSubcommands ./...
package e2e

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/prereview/internal/review"
)

// runCLI runs the built binary as a subprocess with a timeout, returning
// stdout, stderr and the exit code (-1 if it timed out or failed to start).
func runCLI(t *testing.T, timeout time.Duration, binary string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	exit = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	return out.String(), errb.String(), exit
}

func TestE2E_AgentSubcommands(t *testing.T) {
	p, _, _ := bootChromeStream(t)
	p.waitReady()
	p.clickFile("edited.go")
	addLineComment(t, p, 3, 3, "please rename this")

	// `prereview comments --json` — poll until the browser comment is persisted
	// and enumerated; extract its id (the supported alternative to CSV parsing).
	var id string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out, errb, exit := runCLI(t, 5*time.Second, p.binary, "comments", "--out", p.repo, "--json")
		if exit != 0 {
			t.Fatalf("comments --json exit=%d\nstderr: %s", exit, errb)
		}
		var cs []review.StreamComment
		if err := json.Unmarshal([]byte(out), &cs); err != nil {
			t.Fatalf("parse comments --json: %v\n%s", err, out)
		}
		if len(cs) == 1 && strings.Contains(cs[0].Body, "please rename") {
			id = cs[0].ID
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if id == "" {
		t.Fatalf("comments --json never returned the browser comment\nstderr: %s", p.stderr.String())
	}

	// `prereview events --since 0` — cursor past ready@0, so the reader BLOCKS
	// until the (debounced) handoff the UI generated is emitted, then returns it.
	// This exercises the live block-for-next path and is robust to the ~400ms
	// emit debounce (a bare --since -1 could return with just ready@0).
	out, _, exit := runCLI(t, 10*time.Second, p.binary, "events", "--out", p.repo, "--since", "0")
	if exit != 0 {
		t.Fatalf("events --since 0 exit=%d (should return once the snapshot is emitted)", exit)
	}
	var sawHandoff bool
	for _, ev := range parseStreamEvents(out) {
		if ev.Event == "snapshot" && len(ev.CommentList()) == 1 && strings.Contains(ev.CommentList()[0].Body, "please rename") {
			sawHandoff = true
		}
	}
	if !sawHandoff {
		t.Fatalf("events did not deliver the UI-generated handoff:\n%s", out)
	}

	// `prereview processed <id>` — a real id marks worked-on (exit 0).
	if _, errb, exit := runCLI(t, 5*time.Second, p.binary, "processed", "--out", p.repo, id); exit != 0 {
		t.Fatalf("processed <real-id> exit=%d\nstderr: %s", exit, errb)
	}

	// `prereview processed <bogus>` — the #1 regression: an unknown id fails
	// non-zero against the real store, instead of silently recording garbage.
	if _, errb, exit := runCLI(t, 5*time.Second, p.binary, "processed", "--out", p.repo, "totally-bogus-xyz"); exit == 0 {
		t.Fatalf("processed <bogus-id> should fail non-zero; got 0\nstderr: %s", errb)
	}
}
