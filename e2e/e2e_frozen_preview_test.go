//go:build browser

package e2e

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestE2E_FrozenPreviewOnAgentEdit is the reproduction for the live-test report
// "the comment diff was applied without me clicking refresh". The frozen-view
// contract (#119 decision 3) says the browser view — diff OR rendered-Markdown
// preview — must NOT change when the agent edits the file on disk; it re-syncs
// only on an explicit Refresh. TestE2E_LLMStatusRefreshOnDone already pins this
// for the line-diff (.code) view; this pins it for the Markdown preview
// (.md-view), which is what the user was looking at (notes.md in Preview).
func TestE2E_FrozenPreviewOnAgentEdit(t *testing.T) {
	// gfm.md is an untracked Markdown file → opens in the rendered preview
	// (.md-view). --skill starts the llm-status watcher (the working→done signal).
	p := bootChromeAgainstRepo(t, setupFixtureGFMRepo(t), 1200, 800, "--skill")
	p.waitReady()
	p.clickFile("gfm.md")

	// Baseline: the rendered preview is showing the current (pre-agent) content.
	var before string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.md-view').textContent`, &before),
	); err != nil {
		t.Fatalf("read baseline preview: %v\nstderr: %s", err, p.stderr.String())
	}
	if strings.Contains(before, "AGENT-EDIT-MARKER") {
		t.Fatalf("baseline preview unexpectedly already contains the agent marker: %s", before)
	}

	// Agent starts working — wait until the server observes it, so the later done
	// write is a genuine working→done transition (not coalesced under the poll).
	writeLLMStatusFile(t, p.repo, `{"state":"working","message":"editing gfm.md"}`)
	waitJSTrue(t, p.ctx,
		`(() => { const e = document.querySelector('.toast.llm-working'); return !!e && getComputedStyle(e).display !== 'none'; })()`,
		10*time.Second, "working pill appears before edit")

	// Agent rewrites the markdown on disk with a unique marker, then finishes.
	mustWrite(t, p.repo, "gfm.md", "# GFM\n\nAGENT-EDIT-MARKER inserted by the agent.\n")
	writeLLMStatusFile(t, p.repo, `{"state":"done"}`)

	// The refresh affordance appears live (working→done).
	waitJSTrue(t, p.ctx,
		`(() => { const e = document.querySelector('.refresh-prompt'); return !!e && getComputedStyle(e).display !== 'none'; })()`,
		10*time.Second, "refresh affordance appears on done")

	// FROZEN: the preview must NOT show the agent's edit yet — it's a prompt, not
	// an auto-reload.
	var midway string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.md-view').textContent`, &midway)); err != nil {
		t.Fatalf("read midway preview: %v", err)
	}
	if strings.Contains(midway, "AGENT-EDIT-MARKER") {
		t.Errorf("FROZEN-VIEW LEAK: preview auto-updated to the agent's edit before Refresh was clicked; got: %s", midway)
	}

	// Clicking Refresh diff re-syncs the preview to the agent's edit.
	if err := chromedp.Run(p.ctx, chromedp.Click(`button[name="refreshDiff"]`, chromedp.ByQuery)); err != nil {
		t.Fatalf("click Refresh diff: %v\nstderr: %s", err, p.stderr.String())
	}
	waitJSTrue(t, p.ctx,
		`(document.querySelector('.md-view')||{}).textContent?.includes('AGENT-EDIT-MARKER')||false`,
		10*time.Second, "preview shows the agent's edit after refresh")
}

// TestE2E_FrozenViewStreamEmit reproduces the ACTUAL live-test conditions the
// --skill test above misses: --stream mode with a REAL comment on the file. In
// stream mode every comment fires the emit engine, whose re-anchoring
// (relocateAll / relocateSuggestionsAll) calls loadDiffFresh — which REFRESHES
// the shared diffCache to the file's current on-disk bytes. The hypothesis: that
// cache pollution leaks the agent's edit into the frozen line-diff view without
// a Refresh click. Uses the line view (edited.go) for reliable line-commenting.
func TestE2E_FrozenViewStreamEmit(t *testing.T) {
	// --stream implies --skill (the watcher) AND fires the emit engine per comment.
	p := bootChromeAgainstPrereview(t, 1200, 800, "--stream")
	p.waitReady()
	p.clickFile("edited.go")

	// Baseline: the fixture's edited.go returns "hello world".
	var before string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.code').textContent`, &before)); err != nil {
		t.Fatalf("read baseline diff: %v", err)
	}
	if !strings.Contains(before, "hello world") {
		t.Fatalf("pre-edit diff should contain 'hello world'; got: %s", before)
	}

	// Add a real comment — in --stream this fires scheduleEmit → emitSnapshot →
	// relocateAll → loadDiffFresh, which refreshes the shared diffCache.
	p.clickLine(0, 4)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "please rename Hello", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v\nstderr: %s", err, p.stderr.String())
	}

	// Agent starts working, then edits the file on disk (this is the emit's fresh
	// content), then marks the comment processed, then finishes — the exact
	// sequence the live agent ran. `prereview processed` + the disk edit both
	// happen while the shared cache will be refreshed by the emit.
	writeLLMStatusFile(t, p.repo, `{"state":"working","message":"renaming"}`)
	waitJSTrue(t, p.ctx,
		`(() => { const e = document.querySelector('.toast.llm-working'); return !!e && getComputedStyle(e).display !== 'none'; })()`,
		10*time.Second, "working pill appears")
	mustWrite(t, p.repo, "edited.go", "package edited\n\nfunc Greet() string {\n\treturn \"STREAM-AGENT-EDIT\"\n}\n")
	// Mark processed via the real CLI (fans a watcher event, like the live run).
	if out, err := exec.Command(p.binary, "processed", "--out", p.repo, "c1").CombinedOutput(); err != nil {
		t.Logf("processed (non-fatal): %v\n%s", err, out)
	}
	writeLLMStatusFile(t, p.repo, `{"state":"done"}`)

	// Refresh affordance appears live.
	waitJSTrue(t, p.ctx,
		`(() => { const e = document.querySelector('.refresh-prompt'); return !!e && getComputedStyle(e).display !== 'none'; })()`,
		10*time.Second, "refresh affordance appears on done")

	// FROZEN: despite the emit refreshing the shared cache, the open diff must NOT
	// show the agent's edit until Refresh is clicked.
	var midway string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.code').textContent`, &midway)); err != nil {
		t.Fatalf("read midway diff: %v", err)
	}
	if strings.Contains(midway, "STREAM-AGENT-EDIT") {
		t.Errorf("FROZEN-VIEW LEAK (stream+emit): diff auto-updated to the agent's edit before Refresh; got: %s", midway)
	}

	// The live report's actual trigger: the user added a SECOND comment AFTER the
	// agent had edited the file. Adding/saving that comment re-renders the tab —
	// if that path rebuilds CurrentDiff from the (emit-refreshed) cache, the first
	// edit leaks in without a Refresh. Add a second line comment and re-check.
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "second comment after the edit", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.Sleep(600*time.Millisecond),
	); err != nil {
		t.Fatalf("add second comment: %v\nstderr: %s", err, p.stderr.String())
	}
	var afterSecond string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.code').textContent`, &afterSecond)); err != nil {
		t.Fatalf("read diff after second comment: %v", err)
	}
	if strings.Contains(afterSecond, "STREAM-AGENT-EDIT") {
		t.Errorf("FROZEN-VIEW LEAK (2nd comment): adding a comment after the agent edit pulled the edit into the frozen view; got: %s", afterSecond)
	}
}
