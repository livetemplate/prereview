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
	"net/http"
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
// Pass extraArgs to enable --skill mode for tests asserting Hand off behavior.
func startPrereview(t *testing.T, binary, repo string, extraArgs ...string) (string, *exec.Cmd, *bytesBuf) {
	t.Helper()
	args := append([]string{"--repo", repo, "--base", "HEAD", "--port", "0"}, extraArgs...)
	cmd := exec.Command(binary, args...)
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

	// Chromedp setup: headless chromium with desktop viewport (above the
	// 900px breakpoint so the file-drawer renders as a permanent sidebar).
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.WindowSize(1200, 800),
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
		chromedp.EmulateViewport(1200, 800),
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('#files-drawer button.file-btn').length`, &fileButtons),
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
		chromedp.WaitVisible(`.code .line`, chromedp.ByQuery),
		chromedp.OuterHTML(`main.viewer`, &bodyText, chromedp.ByQuery),
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
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='fresh.go']`, chromedp.BySearch),
		chromedp.OuterHTML(`main.viewer`, &bodyText, chromedp.ByQuery),
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

// bootChromeAgainstPrereview compiles the binary, sets up a fixture repo,
// launches prereview, and opens a chromedp session at the given viewport.
// Above 900px the template renders desktop mode (sidebar always visible);
// at/below 900px it renders mobile mode (drawer overlay).
// Pass extraArgs to enable --skill for tests asserting handoff/DONE behavior.
func bootChromeAgainstPrereview(t *testing.T, viewportW, viewportH int, extraArgs ...string) *runningPrereview {
	t.Helper()
	chromium := findChromium(t)
	binary := filepath.Join(t.TempDir(), "prereview")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	repo := setupFixtureRepo(t)
	url, srv, stderr := startPrereview(t, binary, repo, extraArgs...)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.WindowSize(viewportW, viewportH),
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

// waitReady navigates the chromedp browser to the prereview URL after
// forcing a desktop viewport via DevTools emulation (headless chromium
// otherwise pins the viewport to 800x600 regardless of --window-size).
// Pass viewportW = 0 to skip emulation and keep the default mobile viewport.
func (p *runningPrereview) waitReady() {
	p.t.Helper()
	p.waitReadyAt(1200, 800)
}

func (p *runningPrereview) waitReadyAt(viewportW, viewportH int) {
	p.t.Helper()
	actions := []chromedp.Action{
		chromedp.EmulateViewport(int64(viewportW), int64(viewportH)),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
	}
	if err := chromedp.Run(p.ctx, actions...); err != nil {
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
			fmt.Sprintf(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='%s']`, path),
			chromedp.BySearch),
	); err != nil {
		p.t.Fatalf("clickFile %s: %v\nstderr: %s", path, err, p.stderr.String())
	}
}

// clickLine selects the diff line identified by old/new line numbers.
// Pass (0, N) for an add at new=N, (N, 0) for a del at old=N, (N, N) for a
// ctx line. The function disambiguates same-numbered del/add pairs by
// matching the hidden form's side input.
func (p *runningPrereview) clickLine(oldNum, newNum int) {
	p.t.Helper()
	var displayLine int
	var side string
	switch {
	case oldNum == 0 && newNum != 0:
		displayLine, side = newNum, "new"
	case oldNum != 0 && newNum == 0:
		displayLine, side = oldNum, "old"
	case oldNum == newNum && newNum != 0:
		displayLine, side = newNum, "new" // ctx lines are always side=new in our template
	default:
		p.t.Fatalf("clickLine: ambiguous old=%d new=%d", oldNum, newNum)
	}
	js := fmt.Sprintf(`
		(() => {
			const buttons = document.querySelectorAll('.code button.line');
			for (const b of buttons) {
				const form = b.parentElement;
				const lineInput = form.querySelector('input[name="line"]');
				const sideInput = form.querySelector('input[name="side"]');
				if (lineInput && lineInput.value === "%d" && sideInput && sideInput.value === %q) {
					b.click();
					return true;
				}
			}
			return false;
		})()`, displayLine, side)
	var clicked bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &clicked)); err != nil {
		p.t.Fatalf("clickLine eval: %v", err)
	}
	if !clicked {
		p.t.Fatalf("clickLine: no button line=%d side=%s", displayLine, side)
	}
}

// readCSV returns the rows in the prereview CSV file (header included).
func (p *runningPrereview) readCSV() [][]string {
	p.t.Helper()
	csvPath := filepath.Join(p.repo, ".prereview", "comments.csv")
	data, err := os.ReadFile(csvPath)
	if err != nil {
		p.t.Fatalf("read %s: %v", csvPath, err)
	}
	r := stdcsv.NewReader(strings.NewReader(string(data)))
	rows, err := r.ReadAll()
	if err != nil {
		p.t.Fatalf("parse csv: %v", err)
	}
	return rows
}

// TestE2E_MobileDrawer covers the mobile layout: viewport <900px should
// render the file list as a closed drawer with a visible hamburger.
// Tapping hamburger opens drawer; tapping a file closes the drawer (the
// SelectFile action sets FileDrawerOpen=false) and displays the diff.
func TestE2E_MobileDrawer(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 375, 812)

	// Initial mobile load — hamburger should be visible; file buttons
	// rendered in the DOM but the drawer is offscreen (transform: translateX(-100%)).
	var hamburgerVisible bool
	var initialDrawerOpen bool
	if err := chromedp.Run(p.ctx,
		chromedp.EmulateViewport(375, 812),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.hamburger`, chromedp.ByQuery),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.hamburger')).display !== 'none'`, &hamburgerVisible),
		chromedp.Evaluate(`document.querySelector('#files-drawer').classList.contains('is-open')`, &initialDrawerOpen),
	); err != nil {
		t.Fatalf("initial mobile nav: %v\nstderr: %s", err, p.stderr.String())
	}
	if !hamburgerVisible {
		t.Error("hamburger not visible at 375x812; mobile media query failed")
	}
	if initialDrawerOpen {
		t.Error("file drawer should be closed by default on mobile load")
	}

	// Tap hamburger → drawer opens.
	var drawerOpenAfterTap bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.hamburger`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('#files-drawer').classList.contains('is-open')`, &drawerOpenAfterTap),
	); err != nil {
		t.Fatalf("tap hamburger: %v\nstderr: %s", err, p.stderr.String())
	}
	if !drawerOpenAfterTap {
		t.Error("drawer didn't open after tapping hamburger")
	}

	// Tap a file → drawer closes, diff renders.
	var drawerOpenAfterFile bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
		chromedp.Click(`//button[@name='selectFile' and contains(., 'edited.go')]`, chromedp.BySearch),
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='edited.go']`, chromedp.BySearch),
		chromedp.Evaluate(`document.querySelector('#files-drawer').classList.contains('is-open')`, &drawerOpenAfterFile),
	); err != nil {
		t.Fatalf("tap file: %v\nstderr: %s", err, p.stderr.String())
	}
	if drawerOpenAfterFile {
		t.Error("drawer should auto-close after selecting a file")
	}

	// Reopen via hamburger, verify open, then close via the X button.
	var openAfterReopen, drawerOpenAfterX bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.hamburger`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('#files-drawer').classList.contains('is-open')`, &openAfterReopen),
		chromedp.Click(`#files-drawer .close-btn`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('#files-drawer').classList.contains('is-open')`, &drawerOpenAfterX),
	); err != nil {
		t.Fatalf("close via X: %v\nstderr: %s", err, p.stderr.String())
	}
	if !openAfterReopen {
		t.Fatal("drawer didn't re-open after second hamburger tap; can't validate X close")
	}
	if drawerOpenAfterX {
		t.Error("X close-btn didn't close the drawer")
	}

	// Reopen, then close by tapping the backdrop. The backdrop has
	// `lvt-on:click="closeFiles"` so any click on it dispatches the action.
	var drawerOpenAfterBackdrop bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.hamburger`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('.drawer-backdrop.open').click()`, nil),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('#files-drawer').classList.contains('is-open')`, &drawerOpenAfterBackdrop),
	); err != nil {
		t.Fatalf("close via backdrop: %v\nstderr: %s", err, p.stderr.String())
	}
	if drawerOpenAfterBackdrop {
		t.Error("backdrop tap didn't close the drawer")
	}
}

func TestE2E_CommentLifecycle(t *testing.T) {
	// --skill so the Hand off button is rendered (this test asserts DONE).
	p := bootChromeAgainstPrereview(t, 1200, 800, "--skill")
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
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
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

	// Hand off — writes the DONE marker. The composer is gone (no selection
	// after the delete-confirm cycle), so the Hand off button at the top bar
	// is reachable. Skill mode required for the button to render.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='handOff']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.toast`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("click handOff: %v\nstderr: %s", err, p.stderr.String())
	}

	doneBytes, err := os.ReadFile(filepath.Join(p.repo, ".prereview", "DONE"))
	if err != nil {
		t.Fatalf("DONE marker missing: %v", err)
	}
	csvPath := strings.TrimSpace(string(doneBytes))
	if !strings.HasSuffix(csvPath, ".prereview/comments.csv") {
		t.Errorf("DONE points at %q, want ending with .prereview/comments.csv", csvPath)
	}
	if _, err := os.Stat(csvPath); err != nil {
		t.Errorf("CSV path from DONE doesn't exist: %v", err)
	}
}

// TestE2E_HandOffMarker verifies that in skill mode the top-bar button is
// "Hand off" and clicking it writes the DONE marker — without needing to
// add or delete any comments first.
func TestE2E_HandOffMarker(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--skill")
	p.waitReady()

	var btnText string
	if err := chromedp.Run(p.ctx,
		chromedp.Text(`header.bar button[name='handOff']`, &btnText, chromedp.ByQuery),
		chromedp.Click(`header.bar button[name='handOff']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.toast`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("hand off: %v\nstderr: %s", err, p.stderr.String())
	}
	if !strings.Contains(btnText, "Hand off") {
		t.Errorf("button text = %q, want 'Hand off'", btnText)
	}

	donePath := filepath.Join(p.repo, ".prereview", "DONE")
	doneBytes, err := os.ReadFile(donePath)
	if err != nil {
		t.Fatalf("DONE marker missing after hand off: %v", err)
	}
	csvPath := strings.TrimSpace(string(doneBytes))
	if !strings.HasSuffix(csvPath, ".prereview/comments.csv") {
		t.Errorf("DONE points at %q, want ending with .prereview/comments.csv", csvPath)
	}
}

// TestE2E_QuitShutsServer verifies that in standalone mode the top-bar
// button is "Quit" and clicking it gracefully shuts the server down —
// no DONE marker, subsequent HTTP requests fail.
func TestE2E_QuitShutsServer(t *testing.T) {
	// Default boot — no --skill.
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	var btnText string
	if err := chromedp.Run(p.ctx,
		chromedp.Text(`header.bar button[name='quit']`, &btnText, chromedp.ByQuery),
		chromedp.Click(`header.bar button[name='quit']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.banner-stopping`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("quit click: %v\nstderr: %s", err, p.stderr.String())
	}
	if !strings.Contains(btnText, "Quit") {
		t.Errorf("button text = %q, want 'Quit'", btnText)
	}

	// Server shuts down ~300ms after the click. Wait, then confirm the
	// listener has closed by attempting a fresh HTTP request.
	time.Sleep(1 * time.Second)

	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(p.url)
	if err == nil {
		resp.Body.Close()
		t.Errorf("server still responding after Quit; expected dial error")
	}

	// And no DONE marker was written — Quit is not a hand-off.
	donePath := filepath.Join(p.repo, ".prereview", "DONE")
	if _, err := os.Stat(donePath); err == nil {
		t.Errorf("DONE marker exists after Quit; should only be written by Hand off")
	}
}
