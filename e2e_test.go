//go:build browser

// End-to-end test for prereview. Run with: go test -tags=browser ./...
//
// Requires a chromium/chrome binary on PATH (or /run/current-system/sw/bin/chromium).
// Boots a fixture git repo, launches the prereview binary, navigates Chrome
// to the printed URL, and asserts the diff renders correctly. Captures
// browser console logs and the server's stderr so failures can be diagnosed
// without re-running the test manually.

package main

import (
	"bufio"
	"context"
	stdcsv "encoding/csv"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func findChromium(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"/run/current-system/sw/bin/chromium",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/usr/bin/google-chrome",
		"/usr/bin/chrome",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if path, err := exec.LookPath("chromium"); err == nil {
		return path
	}
	if path, err := exec.LookPath("google-chrome"); err == nil {
		return path
	}
	t.Skip("no chromium/chrome binary found")
	return ""
}

func setupFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	mustWrite(t, dir, "keep.go", "package keep\n")
	mustWrite(t, dir, "edited.go", "package edited\n\nfunc Hello() string {\n\treturn \"hi\"\n}\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	// Mutations: modify edited.go, add brand-new untracked file.
	mustWrite(t, dir, "edited.go", "package edited\n\nfunc Hello() string {\n\treturn \"hello world\"\n}\n")
	mustWrite(t, dir, "fresh.go", "package fresh\n\nfunc New() {}\n")
	return dir
}

func runCmd(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func mustWrite(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// startPrereview launches the binary against repo and returns the READY URL,
// the running cmd, and a captured stderr buffer. Caller must kill the cmd.
func startPrereview(t *testing.T, binary, repo string) (string, *exec.Cmd, *bytesBuf) {
	t.Helper()
	cmd := exec.Command(binary, "--repo", repo, "--base", "HEAD", "--port", "0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr := newBytesBuf()
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	// Read READY <url> from first line of stdout.
	urlCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			t.Logf("prereview stdout: %s", line)
			if strings.HasPrefix(line, "READY ") {
				urlCh <- strings.TrimPrefix(line, "READY ")
				// keep draining so the pipe doesn't fill.
				go io.Copy(io.Discard, stdout)
				return
			}
		}
		if err := sc.Err(); err != nil {
			errCh <- err
		}
	}()

	select {
	case url := <-urlCh:
		return url, cmd, stderr
	case err := <-errCh:
		t.Fatalf("scan stdout: %v\nstderr: %s", err, stderr.String())
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("prereview never printed READY\nstderr: %s", stderr.String())
	}
	return "", nil, nil
}

// bytesBuf is an io.Writer collecting bytes with a mutex for safe concurrent
// writes and reads. Avoids bytes.Buffer's lack of synchronization when one
// goroutine reads while another writes.
type bytesBuf struct {
	mu  sync.Mutex
	buf []byte
}

func newBytesBuf() *bytesBuf { return &bytesBuf{} }

func (b *bytesBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *bytesBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func TestE2E_FileListAndDiff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}
	chromium := findChromium(t)

	// Build the binary into a temp path so we don't depend on `make build`.
	binary := filepath.Join(t.TempDir(), "prereview")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	repo := setupFixtureRepo(t)
	url, srv, stderr := startPrereview(t, binary, repo)
	defer func() {
		_ = srv.Process.Kill()
		_, _ = srv.Process.Wait()
	}()

	// Chromedp setup: headless chromium, capture console.
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	var consoleLines []string
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		}
	})

	timeout, tCancel := context.WithTimeout(ctx, 30*time.Second)
	defer tCancel()

	var fileButtons int
	var bodyText string
	if err := chromedp.Run(timeout,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`aside.files button`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('aside.files button').length`, &fileButtons),
		chromedp.OuterHTML(`body`, &bodyText, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("initial nav: %v\nserver stderr: %s\nconsole: %s", err, stderr.String(), strings.Join(consoleLines, "\n"))
	}

	if fileButtons < 2 {
		t.Errorf("expected at least 2 file buttons (edited.go + fresh.go), got %d\nbody: %s", fileButtons, bodyText)
	}
	if !strings.Contains(bodyText, "edited.go") {
		t.Errorf("file list missing edited.go\nbody: %s", bodyText)
	}
	if !strings.Contains(bodyText, "fresh.go") {
		t.Errorf("file list missing untracked fresh.go\nbody: %s", bodyText)
	}

	// Click the edited.go button — should be the second one (after fresh.go alphabetically).
	if err := chromedp.Run(timeout,
		chromedp.Click(`//button[contains(., 'edited.go')]`, chromedp.BySearch),
		chromedp.WaitVisible(`pre.code .line`, chromedp.ByQuery),
		chromedp.OuterHTML(`section.viewer`, &bodyText, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("click edited.go: %v\nserver stderr: %s\nconsole: %s", err, stderr.String(), strings.Join(consoleLines, "\n"))
	}

	// We expect both a deletion (old line) and an addition (new line) in the diff.
	if !strings.Contains(bodyText, "line del") {
		t.Errorf("diff missing del line class\nviewer: %s", bodyText)
	}
	if !strings.Contains(bodyText, "line add") {
		t.Errorf("diff missing add line class\nviewer: %s", bodyText)
	}
	if !strings.Contains(bodyText, "hello world") {
		t.Errorf("diff missing the new content\nviewer: %s", bodyText)
	}

	// Click the fresh (untracked) file — its diff must be all-adds. The
	// previous file already had an add-line, so we can't wait on a generic
	// class selector; wait until the viewer header text mentions fresh.go.
	if err := chromedp.Run(timeout,
		chromedp.Click(`//button[contains(., 'fresh.go')]`, chromedp.BySearch),
		chromedp.WaitVisible(`//section[contains(@class,'viewer')]//strong[normalize-space(text())='fresh.go']`, chromedp.BySearch),
		chromedp.OuterHTML(`section.viewer`, &bodyText, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("click fresh.go: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(bodyText, "package fresh") {
		t.Errorf("untracked file content missing\nviewer: %s", bodyText)
	}
	// Untracked file → every line should be Kind "add", so no "line del" or "line ctx" should appear.
	if strings.Contains(bodyText, "line del") {
		t.Errorf("untracked file shouldn't have del lines\nviewer: %s", bodyText)
	}

	// Console must be free of errors. Warnings are OK.
	for _, line := range consoleLines {
		if strings.HasPrefix(line, "error ") {
			t.Errorf("browser console error: %s", line)
		}
	}
	t.Logf("captured %d console lines", len(consoleLines))
}

// avoid unused-imports if compilation is skipped.
var _ = fmt.Sprintf

// buildAndStart compiles the binary, sets up a fixture repo with both a
// modified and an untracked file, launches the binary, and returns
// everything the comment-lifecycle tests need.
type runningPrereview struct {
	t      *testing.T
	url    string
	repo   string
	cmd    *exec.Cmd
	stderr *bytesBuf
	ctx    context.Context
	cancel context.CancelFunc
}

func bootChromeAgainstPrereview(t *testing.T) *runningPrereview {
	t.Helper()
	chromium := findChromium(t)
	binary := filepath.Join(t.TempDir(), "prereview")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	repo := setupFixtureRepo(t)
	url, srv, stderr := startPrereview(t, binary, repo)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	ctx, cancel := chromedp.NewContext(allocCtx)

	t.Cleanup(func() {
		cancel()
		allocCancel()
		_ = srv.Process.Kill()
		_, _ = srv.Process.Wait()
	})

	return &runningPrereview{
		t: t, url: url, repo: repo, cmd: srv, stderr: stderr,
		ctx: ctx, cancel: cancel,
	}
}

// waitReady gives the page time to render the initial file list. Always
// follow with chromedp.Run for the actual test actions.
func (p *runningPrereview) waitReady() {
	p.t.Helper()
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`aside.files button`, chromedp.ByQuery),
	); err != nil {
		p.t.Fatalf("initial nav: %v\nstderr: %s", err, p.stderr.String())
	}
}

// clickFile clicks the file-tab button by path.
func (p *runningPrereview) clickFile(path string) {
	p.t.Helper()
	xpath := fmt.Sprintf(`//button[@name='selectFile' and contains(., '%s')]`, path)
	if err := chromedp.Run(p.ctx,
		chromedp.Click(xpath, chromedp.BySearch),
		chromedp.WaitVisible(
			fmt.Sprintf(`//section[contains(@class,'viewer')]//strong[normalize-space(text())='%s']`, path),
			chromedp.BySearch),
	); err != nil {
		p.t.Fatalf("clickFile %s: %v\nstderr: %s", path, err, p.stderr.String())
	}
}

// clickLine selects the diff line whose gutter span exactly matches the
// "<old> <new>" pattern. Pass 0 for the absent side.
func (p *runningPrereview) clickLine(oldNum, newNum int) {
	p.t.Helper()
	old := "·"
	if oldNum != 0 {
		old = fmt.Sprintf("%d", oldNum)
	}
	newS := "·"
	if newNum != 0 {
		newS = fmt.Sprintf("%d", newNum)
	}
	gutter := fmt.Sprintf("%s %s", old, newS)
	// JS click is more reliable than chromedp.Click inside complex layouts.
	js := fmt.Sprintf(`
		(() => {
			const spans = document.querySelectorAll('pre.code .gutter');
			for (const s of spans) {
				if (s.textContent.trim() === %q) {
					s.closest('button').click();
					return true;
				}
			}
			return false;
		})()`, gutter)
	var clicked bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &clicked)); err != nil {
		p.t.Fatalf("clickLine eval: %v", err)
	}
	if !clicked {
		p.t.Fatalf("clickLine: no gutter matching %q", gutter)
	}
}

// readCSV returns the rows in the prereview CSV file (header included).
func (p *runningPrereview) readCSV() [][]string {
	p.t.Helper()
	entries, err := os.ReadDir(filepath.Join(p.repo, ".prereview"))
	if err != nil {
		p.t.Fatalf("readdir .prereview: %v", err)
	}
	var csvPath string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "comments-") && strings.HasSuffix(e.Name(), ".csv") {
			csvPath = filepath.Join(p.repo, ".prereview", e.Name())
		}
	}
	if csvPath == "" {
		p.t.Fatalf(".prereview/comments-*.csv not found; entries: %v", entries)
	}
	data, err := os.ReadFile(csvPath)
	if err != nil {
		p.t.Fatalf("read csv: %v", err)
	}
	r := stdcsv.NewReader(strings.NewReader(string(data)))
	rows, err := r.ReadAll()
	if err != nil {
		p.t.Fatalf("parse csv: %v", err)
	}
	return rows
}

func TestE2E_CommentLifecycle(t *testing.T) {
	p := bootChromeAgainstPrereview(t)
	p.waitReady()

	// Switch to edited.go (so we have ctx/del/add lines to comment on).
	p.clickFile("edited.go")

	// Two-click range: anchor at NEW line 3 (the func signature), end at
	// NEW line 4 (the new "hello world" return). Same side, so the range
	// stays as L3-L4 with side="new".
	p.clickLine(3, 3)
	p.clickLine(0, 4)

	// Type comment + submit.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "this hello world might be too friendly", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`aside.comments .comment-card`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v\nstderr: %s", err, p.stderr.String())
	}

	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d:\n%v", len(rows), rows)
	}
	row := rows[1]
	if row[1] != "edited.go" {
		t.Errorf("file = %q, want edited.go", row[1])
	}
	if row[2] != "3" || row[3] != "4" {
		t.Errorf("from/to lines = %q/%q, want 3/4", row[2], row[3])
	}
	if row[4] != "new" {
		t.Errorf("side = %q, want new", row[4])
	}
	if !strings.Contains(row[5], "too friendly") {
		t.Errorf("body = %q, missing comment text", row[5])
	}

	// Edit: click Edit, change the body, save again.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='editComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.composer textarea').value = ''`, nil),
		chromedp.SendKeys(`.composer textarea`, "EDITED: sound the alarm", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond), // give the WS update room to land
	); err != nil {
		t.Fatalf("edit comment: %v\nstderr: %s", err, p.stderr.String())
	}

	rows = p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("post-edit: expected header + 1 row, got %d:\n%v", len(rows), rows)
	}
	if !strings.Contains(rows[1][5], "EDITED") {
		t.Errorf("post-edit body = %q, expected EDITED prefix", rows[1][5])
	}

	// Delete via the confirm dialog. The `<dialog>` starts closed; clicking
	// the "Delete" trigger button uses command/commandfor to open it, but
	// chromedp.Click is finicky with command-attribute buttons in headless
	// chromium. Open the dialog via JS and submit the form inside it.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`
			const dlg = document.querySelector('dialog[id^="confirm-delete-"]');
			dlg.showModal();
		`, nil),
		chromedp.WaitVisible(`dialog[id^='confirm-delete-'][open] button[name='deleteComment']`, chromedp.ByQuery),
		chromedp.Click(`dialog[id^='confirm-delete-'][open] button[name='deleteComment']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("delete comment: %v\nstderr: %s", err, p.stderr.String())
	}

	rows = p.readCSV()
	if len(rows) != 1 {
		t.Errorf("post-delete: expected header-only, got %d rows:\n%v", len(rows), rows)
	}

	// Done — writes the marker.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='done']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.banner-done`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("click done: %v\nstderr: %s", err, p.stderr.String())
	}

	doneBytes, err := os.ReadFile(filepath.Join(p.repo, ".prereview", "DONE"))
	if err != nil {
		t.Fatalf("DONE marker missing: %v", err)
	}
	csvPath := strings.TrimSpace(string(doneBytes))
	if !strings.Contains(csvPath, ".prereview/comments-") || !strings.HasSuffix(csvPath, ".csv") {
		t.Errorf("DONE points at %q, want a .prereview/comments-*.csv", csvPath)
	}
	if _, err := os.Stat(csvPath); err != nil {
		t.Errorf("CSV path from DONE doesn't exist: %v", err)
	}
}
