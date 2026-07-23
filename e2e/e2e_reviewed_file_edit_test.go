//go:build browser

// End-to-end for the reviewed-file watch (Tier A / A1): a RAW edit to the
// reviewed file — with NO prereview command at all — surfaces the refresh nudge
// to the reviewer. This is the deterministic signal that replaces relying on the
// agent to run `status`; the agent forgetting a command can no longer hide that
// it edited the plan.
//
// Run: go test -tags=browser -run TestE2E_ReviewedFileEditNudge ./e2e/...

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestE2E_ReviewedFileEditNudge(t *testing.T) {
	// A NoGit dir review of a single plan (muster reviews the plan file the same
	// way; a dir with one file scopes identically and is simplest to boot).
	repo := t.TempDir()
	plan := filepath.Join(repo, "plan.md")
	if err := os.WriteFile(plan, []byte("# Plan\n\nOriginal problem statement.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := bootChromeAgainstRepo(t, repo, 1200, 800, "--agent")
	p.waitReady()

	// No nudge before any edit.
	var before bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('.refresh-prompt')`, &before),
	); err != nil {
		t.Fatalf("probe before edit: %v", err)
	}
	if before {
		t.Fatal("refresh nudge present before any edit")
	}

	// THE TEST: a raw os.WriteFile edit — zero prereview commands.
	if err := os.WriteFile(plan, []byte("# Plan\n\nRewritten on disk — no prereview command was run.\n\n## Approach\nAdded a section.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The nudge appears within a couple of watcher ticks, deterministically.
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.WaitVisible(`.refresh-prompt`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("refresh nudge never appeared after a raw file edit: %v\nstderr: %s", err, p.stderr.String())
	}

	// Prove it was the FILE-WATCH, not a status echo: no agent sidecar was written.
	if _, err := os.Stat(filepath.Join(repo, ".prereview", "llm-status.json")); err == nil {
		t.Error("llm-status.json exists — the nudge must be file-driven, with no agent command")
	}

	// A2: the same raw edit also checkpointed a version (the diff of what changed),
	// so the reviewer gets "what changed" deterministically — again, no command.
	// The checkpoint runs before the fan-out that produced the nudge above, so it
	// is already on disk.
	timeline := filepath.Join(repo, ".prereview", "versions", "timeline.jsonl")
	data, err := os.ReadFile(timeline)
	if err != nil {
		t.Fatalf("read version timeline: %v", err)
	}
	if !strings.Contains(string(data), `"file-edit"`) {
		t.Errorf("no file-edit version was checkpointed after the raw edit:\n%s", data)
	}
}
