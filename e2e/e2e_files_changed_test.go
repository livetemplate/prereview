//go:build browser

// #213: when a reviewed file changes under the agent — the reviewer restores a
// version, or edits the file on disk — the agent's event stream gets ONE
// files_changed event naming the files, so the agent re-reads them before its
// next edit. Asserted on the durable events.jsonl that `prereview watch` reads.
//
// Run: go test -tags=browser -run TestE2E_FilesChangedEvent ./e2e/...

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

// filesChanged filters a parsed event log down to its files_changed events.
func filesChanged(evs []review.StreamEvent) []review.StreamEvent {
	var out []review.StreamEvent
	for _, ev := range evs {
		if ev.Event == "files_changed" {
			out = append(out, ev)
		}
	}
	return out
}

func TestE2E_FilesChangedEvent(t *testing.T) {
	const (
		original = "package edited\n\nfunc Hello() string {\n\treturn \"ORIGINAL-VERSION\"\n}\n"
		working  = "package edited\n\nfunc Hello() string {\n\treturn \"hello world\"\n}\n"
		settle   = 2 * time.Second
	)
	t.Setenv("PREREVIEW_EDIT_SETTLE", settle.String())
	repo := setupFixtureRepo(t)
	seedVersionStore(t, repo, "edited.go", original, working)

	p := bootChromeAgainstRepo(t, repo, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile("edited.go")

	// (a) Restore the original through the Versions panel: the server's own write,
	// so it is never the agent's edit.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.versions-dropdown .versions-trigger`, chromedp.ByQuery),
		chromedp.Click(`.versions-dropdown .versions-trigger`, chromedp.ByQuery),
		chromedp.WaitReady(`button[name="restoreVersion"]`, chromedp.ByQuery),
		chromedp.Evaluate(clickVersionBtnJS("restoreVersion", 0), nil),
		chromedp.WaitVisible(`.refresh-prompt`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("restore version 0: %v%s", err, diag())
	}
	log := func() string { return readEvents(t, repo) }
	nChanged := func(n int) func([]review.StreamEvent) bool {
		return func(evs []review.StreamEvent) bool { return len(filesChanged(evs)) >= n }
	}
	waitEvents(t, "events.jsonl", log, nChanged(1), "a files_changed for the rollback", diag)
	evs := filesChanged(parseStreamEvents(log()))
	if ev := evs[0]; ev.Cause != review.FilesChangedRollback || !ev.AgentWatching || fmt.Sprint(ev.Files) != "[edited.go]" || ev.Version == 0 {
		t.Fatalf("rollback event %+v, want cause=rollback agent_watching files=[edited.go] version>0%s", ev, diag())
	}

	// (b) The reviewer edits the file on disk while the agent is blocked in watch.
	stampCursor(t, p, review.AgentCursor{Mode: review.AgentCursorWatch, State: review.AgentCursorWaiting, DeliveredSeq: evs[0].Seq})
	time.Sleep(time.Second) // the watcher reads the cursor on its next tick
	if err := os.WriteFile(filepath.Join(repo, "edited.go"), []byte(original+"// reviewer note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitEvents(t, "events.jsonl", log, nChanged(2), "a files_changed for the edit", diag)
	evs = filesChanged(parseStreamEvents(log()))
	if ev := evs[1]; ev.Cause != review.FilesChangedEdit || !ev.AgentWatching || fmt.Sprint(ev.Files) != "[edited.go]" || ev.Version <= evs[0].Version {
		t.Fatalf("edit event %+v, want cause=edit agent_watching files=[edited.go], a newer version%s", ev, diag())
	}

	// One event per change: nothing further arrives once things are quiet.
	time.Sleep(2 * settle)
	if n := len(filesChanged(parseStreamEvents(log()))); n != 2 {
		t.Fatalf("%d files_changed events, want exactly 2%s", n, diag())
	}
}
