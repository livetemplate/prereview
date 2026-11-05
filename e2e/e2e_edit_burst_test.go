//go:build browser

// #198: an agent that edits a reviewed file several times in a row is ONE unit of
// work, so the reviewer gets ONE refresh nudge, offered once the edits settle,
// not one per write. While the burst runs, the refresh slot shows a non-actionable
// "editing" notice instead of a Refresh button that would show a half-done state.
//
// Run: go test -tags=browser -run TestE2E_ReviewedFileEditBurstNudgesOnce ./e2e/...

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/internal/review"
)

const (
	refreshBtnVisibleJS = `(() => { const b = document.querySelector('.refresh-prompt button[name="refreshDiff"]'); return !!b && !!b.offsetParent; })()`
	editingNoticeJS     = `(() => { const e = document.querySelector('.edits-settling-prompt'); return !!e && !!e.offsetParent; })()`
)

// fileEditCheckpoints counts the review's file-edit versions via the store itself.
func fileEditCheckpoints(t *testing.T, repo string) int {
	t.Helper()
	vs, err := review.NewVersionStore(review.VersionsDir(filepath.Join(repo, ".prereview", "comments.csv")))
	if err != nil {
		t.Fatal(err)
	}
	cps, err := vs.Checkpoints()
	if err != nil {
		t.Fatalf("read version timeline: %v", err)
	}
	n := 0
	for _, cp := range cps {
		if cp.Trigger == review.VersionTriggerFileEdit {
			n++
		}
	}
	return n
}

func TestE2E_ReviewedFileEditBurstNudgesOnce(t *testing.T) {
	const settle = 2 * time.Second
	t.Setenv("PREREVIEW_EDIT_SETTLE", settle.String())

	repo := t.TempDir()
	plan := filepath.Join(repo, "plan.md")
	if err := os.WriteFile(plan, []byte("# Plan\n\nOriginal.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := bootChromeAgainstRepo(t, repo, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	before := fileEditCheckpoints(t, repo)

	probe := func(js string) bool {
		var ok bool
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &ok)); err != nil {
			t.Fatalf("probe %s: %v%s", js, err, diag())
		}
		return ok
	}

	// The burst: five raw writes 500ms apart, no agent status at all (the shape of
	// the session in the issue). Sample the page throughout.
	sawNotice := false
	for i := 1; i <= 5; i++ {
		body := fmt.Sprintf("# Plan\n\nOriginal.\n\n## Edit %d\nStep %d of the agent's work.\n", i, i)
		if err := os.WriteFile(plan, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		for end := time.Now().Add(500 * time.Millisecond); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
			if probe(refreshBtnVisibleJS) {
				t.Fatalf("a Refresh button was offered mid-burst (after write %d)%s", i, diag())
			}
			sawNotice = sawNotice || probe(editingNoticeJS)
		}
	}
	if !sawNotice {
		t.Fatalf("the editing notice never showed during the burst%s", diag())
	}

	// Once the edits settle: exactly one Refresh bar, and the notice is gone.
	waitJSTrue(t, p.ctx, refreshBtnVisibleJS, 10*time.Second, "one refresh nudge after the burst settles")
	if probe(editingNoticeJS) {
		t.Fatalf("editing notice still showing alongside the Refresh bar%s", diag())
	}
	if bars := evalInt(t, p, `document.querySelectorAll('.refresh-prompt').length`); bars != 1 {
		t.Fatalf("%d refresh bars, want 1%s", bars, diag())
	}

	// Refresh, then stay quiet for twice the settle window: no second nudge.
	if err := chromedp.Run(p.ctx, chromedp.Click(`.refresh-prompt button[name="refreshDiff"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("click refresh: %v%s", err, diag())
	}
	waitJSTrue(t, p.ctx, `!document.querySelector('.refresh-prompt')`, 5*time.Second, "refresh clears the nudge")
	time.Sleep(2 * settle)
	if probe(`!!document.querySelector('.refresh-prompt')`) || probe(editingNoticeJS) {
		t.Fatalf("a nudge came back after refresh with no further edits%s", diag())
	}

	// One version for the whole burst.
	if got := fileEditCheckpoints(t, repo) - before; got != 1 {
		t.Fatalf("%d new file-edit versions for one burst, want 1%s", got, diag())
	}
}
