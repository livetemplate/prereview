//go:build browser

// End-to-end test for `prereview --skill --stream`: the real binary emits a
// continuous JSON event stream (stdout + .prereview/events.jsonl) across
// multiple handoff rounds, with monotonic seq, a resolve-pruned snapshot, and
// a terminating session_end that shuts the server down.
// Run with: go test -tags=browser -run TestE2E_Stream ./...
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/internal/review"
)

// startPrereviewStream launches the binary with --skill --stream and captures
// ALL stdout (not just READY) into a bytesBuf so the test can read the JSON
// event lines as they arrive. Returns once READY prints.
func startPrereviewStream(t *testing.T, binary, repo string) (url string, cmd *exec.Cmd, stderr, stdoutBuf *bytesBuf) {
	t.Helper()
	cmd = exec.Command(binary,
		"--base", "HEAD", "--port", "0", "--host", "127.0.0.1", "--no-update",
		"--skill", "--stream", repo)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr = newBytesBuf()
	stdoutBuf = newBytesBuf()
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}
	urlCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdoutPipe)
		for sc.Scan() {
			line := sc.Text()
			t.Logf("prereview stdout: %s", line)
			_, _ = stdoutBuf.Write([]byte(line + "\n")) // capture everything (preamble + JSON events)
			if strings.HasPrefix(line, "READY ") {
				select {
				case urlCh <- strings.TrimPrefix(line, "READY "):
				default:
				}
			}
		}
	}()
	select {
	case url := <-urlCh:
		return url, cmd, stderr, stdoutBuf
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("prereview never printed READY\nstderr: %s", stderr.String())
	}
	return "", nil, nil, nil
}

// bootChromeStream boots `--skill --stream` against the fixture repo, wires a
// headless chrome, and returns the running session, the stdout capture buffer,
// and a channel that delivers the process's exit (single-owner Wait, so the
// test can detect End-session shutdown without racing t.Cleanup).
func bootChromeStream(t *testing.T) (*runningPrereview, *bytesBuf, <-chan error) {
	t.Helper()
	chromium := findChromium(t)
	binary := filepath.Join(t.TempDir(), "prereview")
	if out, err := exec.Command("go", "build", "-o", binary, "..").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	repo := setupFixtureRepo(t)
	url, srv, stderr, stdoutBuf := startPrereviewStream(t, binary, repo)

	waitCh := make(chan error, 1)
	go func() { waitCh <- srv.Wait() }()

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.WindowSize(1200, 800),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	ctx, tcancel := context.WithTimeout(ctx, 75*time.Second)
	t.Cleanup(func() {
		tcancel()
		cancel()
		allocCancel()
		_ = srv.Process.Kill() // the Wait goroutine reaps it
	})
	return &runningPrereview{t: t, url: url, repo: repo, cmd: srv, stderr: stderr, ctx: ctx, cancel: cancel}, stdoutBuf, waitCh
}

// parseStreamEvents extracts the JSON event lines from a stdout capture,
// tolerating the plaintext READY/ALT/REPO preamble (non-`{` lines).
func parseStreamEvents(s string) []review.StreamEvent {
	var evs []review.StreamEvent
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev review.StreamEvent
		if err := json.Unmarshal([]byte(line), &ev); err == nil {
			evs = append(evs, ev)
		}
	}
	return evs
}

func handoffEvents(evs []review.StreamEvent) []review.StreamEvent {
	var out []review.StreamEvent
	for _, e := range evs {
		if e.Event == "handoff" {
			out = append(out, e)
		}
	}
	return out
}

// waitStream polls the stdout capture until pred is satisfied or it times out.
func waitStream(t *testing.T, buf *bytesBuf, pred func([]review.StreamEvent) bool, what string, diag func() string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if pred(parseStreamEvents(buf.String())) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s\n--- stdout ---\n%s%s", what, buf.String(), diag())
}

// addLineComment selects a line on the open file and saves a comment.
func addLineComment(t *testing.T, p *runningPrereview, oldNum, newNum int, body string) {
	t.Helper()
	p.clickLine(oldNum, newNum)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, body, chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment %q: %v\nstderr: %s", body, err, p.stderr.String())
	}
}

// TestE2E_StreamHandoff drives the real --stream binary through a browser:
// two handoff rounds emit JSON events with incrementing seq, resolving a
// comment prunes the next snapshot, the events also land in events.jsonl, and
// End session emits the terminator and shuts the server down.
func TestE2E_StreamHandoff(t *testing.T) {
	p, stdoutBuf, waitCh := bootChromeStream(t)

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

	// Round 1: one comment → Hand off → a handoff event carrying it.
	addLineComment(t, p, 3, 3, "first round note")
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`header.bar button[name='handOff']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.toast`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("round 1 hand off: %v%s", err, diag())
	}
	waitStream(t, stdoutBuf, func(evs []review.StreamEvent) bool {
		h := handoffEvents(evs)
		if len(h) < 1 {
			return false
		}
		c := h[len(h)-1].CommentList()
		return len(c) == 1 && strings.Contains(c[0].Body, "first round")
	}, "round 1 handoff event with the comment", diag)

	// Round 2: a second comment → Hand off → a handoff snapshot of BOTH.
	addLineComment(t, p, 0, 4, "second round note")
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`header.bar button[name='handOff']`, chromedp.ByQuery),
		chromedp.Sleep(150*time.Millisecond),
	); err != nil {
		t.Fatalf("round 2 hand off: %v%s", err, diag())
	}
	waitStream(t, stdoutBuf, func(evs []review.StreamEvent) bool {
		h := handoffEvents(evs)
		return len(h) >= 2 && len(h[len(h)-1].CommentList()) == 2
	}, "round 2 handoff event with both comments", diag)

	// Resolve the first comment (line 3) → next Hand off prunes it.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-comment button[name='toggleResolved']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Click(`header.bar button[name='handOff']`, chromedp.ByQuery),
		chromedp.Sleep(150*time.Millisecond),
	); err != nil {
		t.Fatalf("resolve + round 3 hand off: %v%s", err, diag())
	}
	waitStream(t, stdoutBuf, func(evs []review.StreamEvent) bool {
		h := handoffEvents(evs)
		if len(h) < 3 {
			return false
		}
		c := h[len(h)-1].CommentList()
		return len(c) == 1 && strings.Contains(c[0].Body, "second round")
	}, "round 3 handoff with the resolved comment pruned", diag)

	// seq is strictly increasing across rounds — the fix for idempotent DONE.
	h := handoffEvents(parseStreamEvents(stdoutBuf.String()))
	for i := 1; i < len(h); i++ {
		if h[i].Seq <= h[i-1].Seq {
			t.Errorf("handoff seqs not strictly increasing: %d then %d", h[i-1].Seq, h[i].Seq)
		}
	}

	// events.jsonl mirror: the real binary wired the file and the same handoff
	// events landed there (byte-parity itself is unit-tested).
	eventsPath := filepath.Join(p.repo, ".prereview", "events.jsonl")
	fileBytes, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v%s", err, diag())
	}
	if got := len(handoffEvents(parseStreamEvents(string(fileBytes)))); got < 3 {
		t.Errorf("events.jsonl has %d handoff events, want >= 3", got)
	}

	// End session: flush + session_end terminator, then the server shuts down.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`header.bar button[name='endSession']`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("click End session: %v%s", err, diag())
	}
	waitStream(t, stdoutBuf, func(evs []review.StreamEvent) bool {
		return len(evs) > 0 && evs[len(evs)-1].Event == "session_end"
	}, "session_end terminator", diag)

	select {
	case <-waitCh:
		// server exited — the background job completes, the second terminator.
	case <-time.After(5 * time.Second):
		t.Fatalf("server did not shut down after End session%s", diag())
	}
}
