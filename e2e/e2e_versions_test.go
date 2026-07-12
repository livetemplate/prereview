//go:build browser

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// seedVersionStore writes a deterministic version history for one path into the
// repo's .prereview/versions/ store (content-addressed blobs + timeline.jsonl),
// so the version-panel UI has real history to render without depending on the
// llm-status watcher's timing. contents[0] is the baseline; the rest are edits.
func seedVersionStore(t *testing.T, repo, path string, contents ...string) {
	t.Helper()
	vdir := filepath.Join(repo, ".prereview", "versions")
	if err := os.MkdirAll(filepath.Join(vdir, "blobs"), 0o755); err != nil {
		t.Fatalf("mkdir version store: %v", err)
	}
	var lines []string
	for i, c := range contents {
		sum := sha256.Sum256([]byte(c))
		sha := hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(vdir, "blobs", sha), []byte(c), 0o644); err != nil {
			t.Fatalf("write blob: %v", err)
		}
		trigger, summary := "llm-done", ""
		if i == 0 {
			trigger = "baseline"
		} else {
			summary = fmt.Sprintf("Agent changelog for edit %d", i) // #155: seeded changelog
		}
		lines = append(lines, fmt.Sprintf(
			`{"seq":%d,"ts":"2026-01-%02dT00:00:00Z","trigger":%q,"summary":%q,"files":[{"path":%q,"sha":%q}]}`,
			i, i+1, trigger, summary, path, sha))
	}
	if err := os.WriteFile(filepath.Join(vdir, "timeline.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write timeline: %v", err)
	}
}

// TestE2E_VersionTimeline exercises the artifact-versioning UI (#90): the
// per-file Versions panel lists the timeline, "View" opens a prior version
// read-only, and "Restore" rolls the file back on disk (decoupled from the
// send-mode — no pause/batch). Captures browser console + server stderr.
func TestE2E_VersionTimeline(t *testing.T) {
	const (
		original = "package edited\n\nfunc Hello() string {\n\treturn \"ORIGINAL-VERSION\"\n}\n"
		// Must byte-match setupFixtureRepo's working-tree edited.go so the seeded
		// "current" version lines up with what's on disk at launch.
		working = "package edited\n\nfunc Hello() string {\n\treturn \"hello world\"\n}\n"
	)
	repo := setupFixtureRepo(t)
	seedVersionStore(t, repo, "edited.go", original, working)

	p := bootChromeAgainstRepo(t, repo, 1200, 800, "--agent")

	var consoleLines []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			consoleLines = append(consoleLines, string(e.Type))
		}
	})
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), html)
	}

	p.waitReady()
	p.clickFile("edited.go")

	// The Versions panel should list 2 versions and offer a Restore on the
	// non-current one.
	var rows, restores int
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.versions-dropdown .versions-trigger`, chromedp.ByQuery),
		chromedp.Click(`.versions-dropdown .versions-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.versions-panel .version-row`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('.version-row').length`, &rows),
		chromedp.Evaluate(`document.querySelectorAll('button[name="restoreVersion"]').length`, &restores),
	); err != nil {
		t.Fatalf("open versions panel: %v%s", err, diag())
	}
	if rows != 2 {
		t.Errorf("expected 2 version rows, got %d%s", rows, diag())
	}
	if restores != 1 {
		t.Errorf("expected 1 Restore button (on the non-current version), got %d%s", restores, diag())
	}

	// View the Original (seq 0): read-only banner shows and the pane renders the
	// historical content.
	var viewerText string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(clickVersionBtnJS("viewVersion", 0), nil),
		chromedp.WaitVisible(`.version-view-banner`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('main.viewer').textContent`, &viewerText),
	); err != nil {
		t.Fatalf("view version 0: %v%s", err, diag())
	}
	if !strings.Contains(viewerText, "ORIGINAL-VERSION") {
		t.Errorf("viewing the original should show its content; viewer text: %q", viewerText)
	}

	// Back to current clears the banner.
	var bannerGone bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.version-view-banner button[name="exitVersionView"]`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`!document.querySelector('.version-view-banner')`, &bannerGone),
	); err != nil {
		t.Fatalf("exit version view: %v%s", err, diag())
	}
	if !bannerGone {
		t.Errorf("read-only banner should clear after Back to current%s", diag())
	}

	// Diff the Original vs current: the banner says "Comparing" and the pane
	// renders del+add rows (ORIGINAL-VERSION → hello world).
	var bannerText, diffHTML string
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.versions-dropdown .versions-trigger`, chromedp.ByQuery),
		// #155: the actions now live in the collapsed .version-details (revealed by the
		// row's ⋯ toggle). clickVersionBtnJS fires a JS click that works regardless, so
		// wait for presence (WaitReady), not visibility.
		chromedp.WaitReady(`button[name="diffVersion"]`, chromedp.ByQuery),
		chromedp.Evaluate(clickVersionBtnJS("diffVersion", 0), nil),
		chromedp.WaitVisible(`.version-view-banner`, chromedp.ByQuery),
		chromedp.Text(`.version-view-banner`, &bannerText, chromedp.ByQuery),
		chromedp.OuterHTML(`main.viewer`, &diffHTML, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("diff version 0: %v%s", err, diag())
	}
	if !strings.Contains(bannerText, "Comparing") {
		t.Errorf("diff banner should say 'Comparing', got %q", bannerText)
	}
	if !strings.Contains(diffHTML, "line del") || !strings.Contains(diffHTML, "line add") {
		t.Errorf("diff pane should show del+add rows; html: %s", diffHTML)
	}

	// Back to current before the destructive step.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.version-view-banner button[name="exitVersionView"]`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("exit diff view: %v%s", err, diag())
	}

	// Restore the Original: the file on disk reverts and the browser gets a
	// refresh nudge. Rollback is decoupled from the send-mode (#119) — it does
	// NOT pause/batch. Wait on the refresh prompt (PendingRefresh) as the sync
	// point instead of a paused banner.
	var pausedBanners int
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.versions-dropdown .versions-trigger`, chromedp.ByQuery),
		chromedp.WaitReady(`button[name="restoreVersion"]`, chromedp.ByQuery),
		chromedp.Evaluate(clickVersionBtnJS("restoreVersion", 0), nil),
		chromedp.WaitVisible(`.refresh-prompt`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('.paused-prompt').length`, &pausedBanners),
	); err != nil {
		t.Fatalf("restore version 0: %v%s", err, diag())
	}

	got, err := os.ReadFile(filepath.Join(repo, "edited.go"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(got) != original {
		t.Errorf("restored edited.go = %q, want the original version %q", got, original)
	}

	// Decoupled: rollback must NOT batch/pause — no banner, no paused marker.
	if pausedBanners != 0 {
		t.Errorf("rollback should not show the batching banner, saw %d%s", pausedBanners, diag())
	}
	if _, err := os.Stat(filepath.Join(repo, ".prereview", "paused")); !os.IsNotExist(err) {
		t.Errorf("rollback must not write the paused marker (send-mode decoupled), stat err = %v", err)
	}

	for _, l := range consoleLines {
		if strings.Contains(strings.ToLower(l), "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
}

// clickVersionBtnJS returns JS that clicks the version-panel button (viewVersion
// / restoreVersion) whose form carries the given seq — the robust JS-click the
// harness prefers over coordinate dispatch for dropdown-panel buttons.
// TestE2E_VersionSummary (#155): each version row is compact by default; its ⋯ toggle
// expands INLINE to reveal the computed "what changed" diff summary + the (now
// decluttered) actions — and expanding a row must NOT close the Versions dropdown
// (the nested-toggle-inside-a-toggle case).
func TestE2E_VersionSummary(t *testing.T) {
	const (
		original = "package edited\n\nfunc Hello() string {\n\treturn \"ORIGINAL-VERSION\"\n}\n"
		working  = "package edited\n\nfunc Hello() string {\n\treturn \"hello world\"\n}\n"
	)
	repo := setupFixtureRepo(t)
	seedVersionStore(t, repo, "edited.go", original, working)

	p := bootChromeAgainstRepo(t, repo, 1200, 800, "--agent")
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		return "\n--- server ---\n" + p.stderr.String() + "\n--- html ---\n" + html
	}
	p.waitReady()
	p.clickFile("edited.go")

	// Open the panel: rows are compact — the actions start hidden (in .version-details).
	var actionVisible bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.versions-dropdown .versions-trigger`, chromedp.ByQuery),
		chromedp.Click(`.versions-dropdown .versions-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.versions-panel .version-row .version-summary-toggle`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => { const b = document.querySelector('.version-row button[name="viewVersion"]'); return !!(b && b.offsetParent); })()`, &actionVisible),
	); err != nil {
		t.Fatalf("open versions panel: %v%s", err, diag())
	}
	if actionVisible {
		t.Errorf("version actions should be collapsed by default (behind the ⋯ toggle)%s", diag())
	}

	// Expand the newest row (the "Agent edit") → its agent changelog + the +add/−del
	// stat. The raw diff-line preview is gone (it didn't scale to a large diff).
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('.version-row')[0].querySelector('.version-summary-toggle').click()`, nil),
		chromedp.WaitVisible(`.version-row.expanded .version-changelog`, chromedp.ByQuery),
		chromedp.WaitVisible(`.version-row.expanded .version-summary .vs-add`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("expand version row: %v%s", err, diag())
	}

	var changelog string
	var add, rem, previewLines int
	_ = chromedp.Run(p.ctx,
		chromedp.Text(`.version-row.expanded .version-changelog`, &changelog, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.version-row.expanded .vs-add').textContent.trim()==='+1'?1:0`, &add),
		chromedp.Evaluate(`document.querySelector('.version-row.expanded .vs-rem').textContent.trim()==='−1'?1:0`, &rem),
		chromedp.Evaluate(`document.querySelectorAll('.version-row.expanded .vs-line').length`, &previewLines),
	)
	if !strings.Contains(changelog, "Agent changelog for edit 1") {
		t.Errorf("expanded row should show the agent's changelog; got %q%s", changelog, diag())
	}
	if add != 1 || rem != 1 {
		t.Errorf("the +add/−del stat fallback should read +1 −1; add=%d rem=%d%s", add, rem, diag())
	}
	if previewLines != 0 {
		t.Errorf("the raw diff-line preview must be gone (does not scale); got %d lines%s", previewLines, diag())
	}

	// The nesting de-risk: expanding a row must keep the Versions dropdown OPEN.
	var dropdownOpen bool
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.versions-dropdown').classList.contains('open')`, &dropdownOpen))
	if !dropdownOpen {
		t.Errorf("expanding a row must not close the Versions dropdown%s", diag())
	}

	// The baseline row's summary reads "Initial version" (no predecessor).
	var initialNote bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('.version-row')[1].querySelector('.version-summary-toggle').click()`, nil),
		chromedp.WaitVisible(`.version-row:last-child.expanded .version-summary .vs-note`, chromedp.ByQuery),
		chromedp.Evaluate(`/Initial/.test(document.querySelector('.version-row:last-child .vs-note').textContent)`, &initialNote),
	); err != nil {
		t.Fatalf("expand baseline row: %v%s", err, diag())
	}
	if !initialNote {
		t.Errorf("the baseline row summary should say 'Initial version'%s", diag())
	}
}

func clickVersionBtnJS(name string, seq int) string {
	return fmt.Sprintf(`(() => {
		const forms = document.querySelectorAll('.versions-panel form');
		for (const f of forms) {
			const seqInput = f.querySelector('input[name="seq"]');
			const btn = f.querySelector('button[name="%s"]');
			if (seqInput && btn && seqInput.value === "%d") { btn.click(); return true; }
		}
		return false;
	})()`, name, seq)
}
