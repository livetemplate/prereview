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

	// Second commit so HEAD~1 resolves — exercised by the base-picker test
	// and by the auto-fallback path in Mount. Adds a new file that's
	// committed-only (not in the working-tree diff vs HEAD), so existing
	// tests that count working-tree files are unaffected.
	mustWrite(t, dir, "history.go", "package history\n\nfunc Old() {}\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "history")

	// Mutations: modify edited.go, add brand-new untracked file.
	mustWrite(t, dir, "edited.go", "package edited\n\nfunc Hello() string {\n\treturn \"hello world\"\n}\n")
	mustWrite(t, dir, "fresh.go", "package fresh\n\nfunc New() {}\n")
	return dir
}

// setupFixtureRepoClean builds a fixture with two commits and a clean
// working tree — used to verify the all-files pivot (every tracked
// file shows up in the drawer regardless of `git diff HEAD` output).
func setupFixtureRepoClean(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	mustWrite(t, dir, "alpha.go", "package alpha\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	mustWrite(t, dir, "beta.go", "package beta\n\nfunc B() {}\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "add beta")
	// No working-tree mutations — `git diff HEAD` is empty.
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
	var viewerText string
	if err := chromedp.Run(timeout,
		chromedp.Click(`//button[contains(., 'fresh.go')]`, chromedp.BySearch),
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='fresh.go']`, chromedp.BySearch),
		chromedp.OuterHTML(`main.viewer`, &bodyText, chromedp.ByQuery),
		// Pull textContent so syntax-highlighting span wrappers don't
		// fragment the literal text we want to assert against.
		chromedp.Evaluate(`document.querySelector('main.viewer').textContent`, &viewerText),
	); err != nil {
		t.Fatalf("click fresh.go: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(viewerText, "package fresh") {
		t.Errorf("untracked file content missing\nviewer text: %s", viewerText)
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
	return bootChromeAgainstRepo(t, setupFixtureRepo(t), viewportW, viewportH, extraArgs...)
}

// bootChromeAgainstRepo is the underlying helper that boots prereview
// against a caller-supplied repo path. Pass the result of
// setupFixtureRepo (working-tree mutations) or setupFixtureRepoClean
// (no mutations) depending on what scenario the test exercises.
func bootChromeAgainstRepo(t *testing.T, repo string, viewportW, viewportH int, extraArgs ...string) *runningPrereview {
	t.Helper()
	chromium := findChromium(t)
	binary := filepath.Join(t.TempDir(), "prereview")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
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
		// The deferred livetemplate-client.js needs time to parse, connect
		// over WebSocket, and attach the event-delegation listener before
		// any chromedp.Click can actually fire an action. WaitVisible
		// returns as soon as the SSR HTML is parsed, which is well before
		// the client's WS handshake completes — especially when the page
		// is heavier (syntax-highlighted diff content). Sleep 1s here as a
		// defensive ceiling so every test that calls waitReady gets a
		// consistent "client is wired" baseline.
		chromedp.Sleep(1 * time.Second),
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
// matching the button's data-side attribute (data-line / data-side now
// live on the button itself — the wrapping form was removed to cut DOM
// nodes per line in half).
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
	sel := fmt.Sprintf(`.code button.line[data-line="%d"][data-side="%s"]`, displayLine, side)
	js := fmt.Sprintf(`
		(() => {
			const b = document.querySelector(%q);
			if (!b) return false;
			b.click();
			return true;
		})()`, sel)
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

	// Tap hamburger → drawer opens. The first click depends on the
	// deferred livetemplate-client.js having parsed + connected via
	// WebSocket; until then, click events have no listener and are
	// silently lost. Empirically the connect window closes within ~2s
	// on this fixture (syntax-highlighted small file) — sleep 2s as a
	// defensive ceiling before the first action, then use JS-click so
	// stacking-context quirks don't dispatch the click to a sibling.
	if err := chromedp.Run(p.ctx,
		chromedp.Sleep(2*time.Second),
		chromedp.Evaluate(`document.querySelector('.hamburger').click()`, nil),
		chromedp.WaitVisible(`#files-drawer.is-open`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("tap hamburger: %v\nstderr: %s", err, p.stderr.String())
	}

	// Tap a file → drawer closes, diff renders. Use a JS click (not
	// chromedp.Click's coordinate dispatch) so the action fires reliably
	// even if the drawer's stacking context shifts the file-btn's hit
	// region. Pick fresh.go (not the auto-selected first file) so the
	// effect is observable.
	var drawerOpenAfterFile, clickedFresh bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const b = Array.from(document.querySelectorAll('button[name="selectFile"]'))
				.find(x => x.textContent.includes('fresh.go'));
			if (!b) return false;
			b.click();
			return true;
		})()`, &clickedFresh),
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='fresh.go']`, chromedp.BySearch),
		chromedp.Evaluate(`document.querySelector('#files-drawer').classList.contains('is-open')`, &drawerOpenAfterFile),
	); err != nil {
		t.Fatalf("tap file: %v\nstderr: %s", err, p.stderr.String())
	}
	if !clickedFresh {
		t.Fatal("fresh.go button not found in DOM")
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

	// Edit: click Edit, replace body, save again. Wait for the
	// composer label to flip to "Editing comment on …" so we know the
	// EditComment action's round-trip has landed and the textarea has
	// been patched with the existing body — otherwise our JS set-value
	// can fire before the server's response and get overwritten.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='editComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`//div[contains(@class,'composer')]//strong[starts-with(normalize-space(text()), 'Editing comment on')]`, chromedp.BySearch),
		// Set value + submit atomically in a single Evaluate so the
		// framework can't slip another patch between them.
		chromedp.Evaluate(`(() => {
			const t = document.querySelector('.composer textarea');
			t.value = 'EDITED: sound the alarm';
			return t.value;
		})()`, nil),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
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

// TestE2E_NextPrevFile verifies the top-bar Next/Prev arrows cycle through
// state.Files and update the viewer accordingly.
func TestE2E_NextPrevFile(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	// Pick fresh.go (alphabetically second), wait until the viewer
	// actually shows it — `WaitVisible(... article header strong ...)`
	// alone returns as soon as ANY strong is present, which it is from
	// auto-select. We need to wait for the filename to update.
	p.clickFile("fresh.go")
	var first string
	if err := chromedp.Run(p.ctx, chromedp.Text(`main.viewer article header strong`, &first, chromedp.ByQuery)); err != nil {
		t.Fatalf("read first filename: %v", err)
	}
	if first != "fresh.go" {
		t.Fatalf("setup: viewer shows %q, expected fresh.go", first)
	}

	// Click Next; expect a different file in the viewer. Wait until the
	// filename actually changes — same async-action concern as above.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`header.bar button[name='nextFile']`, chromedp.ByQuery),
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())!='fresh.go']`, chromedp.BySearch),
	); err != nil {
		t.Fatalf("click next: %v", err)
	}
	var afterNext string
	_ = chromedp.Run(p.ctx, chromedp.Text(`main.viewer article header strong`, &afterNext, chromedp.ByQuery))
	if afterNext == first || afterNext == "" {
		t.Errorf("after Next: filename = %q (was %q); expected to advance", afterNext, first)
	}

	// Prev should bring us back to fresh.go.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`header.bar button[name='prevFile']`, chromedp.ByQuery),
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='fresh.go']`, chromedp.BySearch),
	); err != nil {
		t.Fatalf("click prev: %v", err)
	}
	var afterPrev string
	_ = chromedp.Run(p.ctx, chromedp.Text(`main.viewer article header strong`, &afterPrev, chromedp.ByQuery))
	if afterPrev != first {
		t.Errorf("after Prev: filename = %q; expected to return to %q", afterPrev, first)
	}
}

// TestE2E_EditCancelPreservesComment verifies that clicking Cancel
// during an edit leaves the original comment intact. Regression test
// for a bug where EditComment was deleting the comment immediately,
// expecting AddComment to re-add it on Save — so Cancel destroyed
// the comment instead of just discarding the edit.
func TestE2E_EditCancelPreservesComment(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "keep me", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("seed comment: %v\nstderr: %s", err, p.stderr.String())
	}
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("after add: expected 1 row + header, got %d: %v", len(rows), rows)
	}

	// Click Edit → composer opens with body pre-filled. Click Cancel.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='editComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.Click(`button[name='clearSelection']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("edit + cancel: %v\nstderr: %s", err, p.stderr.String())
	}

	// Comment should still be visible in the diff stream and still be
	// in the CSV. Composer should be closed.
	var hasInlineComment, hasComposer bool
	var commentBody string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('.inline-comment')`, &hasInlineComment),
		chromedp.Evaluate(`!!document.querySelector('.composer textarea')`, &hasComposer),
		chromedp.Evaluate(`(document.querySelector('.inline-comment .body') || {textContent:""}).textContent`, &commentBody),
	); err != nil {
		t.Fatalf("post-cancel query: %v", err)
	}
	if !hasInlineComment {
		t.Error("comment should still be visible after cancelling an edit; got deleted")
	}
	if hasComposer {
		t.Error("composer should close after cancel; still open")
	}
	if commentBody != "keep me" {
		t.Errorf("comment body after cancel = %q, want %q", commentBody, "keep me")
	}
	rows = p.readCSV()
	if len(rows) != 2 {
		t.Errorf("after cancel: CSV should still have 1 row + header, got %d: %v", len(rows), rows)
	}
}

// TestE2E_EditSurvivesReconnect simulates the iPhone-Safari pattern of
// dropping the WebSocket on tab/app switch: open Edit, force a fresh
// Navigate (which closes the WS and opens a new one), then click Save.
// The comment should still be updated in place, not appended as a new
// row. Regression test for EditingCommentID not being lvt:persist.
func TestE2E_EditSurvivesReconnect(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "original", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("seed: %v\nstderr: %s", err, p.stderr.String())
	}
	rowsBefore := p.readCSV()
	if len(rowsBefore) != 2 {
		t.Fatalf("after seed: expected 1 row + header, got %d", len(rowsBefore))
	}
	originalID := rowsBefore[1][0]

	// Open Edit, then force a reconnect by navigating to the same URL
	// fresh — that closes the WS and opens a new session. With
	// EditingCommentID persisted, the composer reopens still in edit
	// mode; Save then updates in place.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='editComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		// After Navigate the composer is re-rendered at its persisted
		// line, but the browser scrolled to top — the composer may be
		// below the fold. Scroll it into view + small settle delay so
		// SendKeys can focus the textarea reliably.
		chromedp.WaitVisible(`//div[contains(@class,'composer')]//strong[starts-with(normalize-space(text()), 'Editing comment on')]`, chromedp.BySearch),
		chromedp.Evaluate(`document.querySelector('.composer').scrollIntoView({block: "center"})`, nil),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(`(() => {
			const t = document.querySelector('.composer textarea');
			t.value = 'edited after reconnect';
			return t.value;
		})()`, nil),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("edit + reconnect + save: %v\nstderr: %s", err, p.stderr.String())
	}

	rowsAfter := p.readCSV()
	if len(rowsAfter) != 2 {
		t.Errorf("post-reconnect save should still be in-place; got %d rows: %v", len(rowsAfter), rowsAfter)
	}
	if rowsAfter[1][0] != originalID {
		t.Errorf("comment ID changed across reconnect-edit: was %q, now %q", originalID, rowsAfter[1][0])
	}
	if rowsAfter[1][5] != "edited after reconnect" {
		t.Errorf("comment body after update = %q, want %q", rowsAfter[1][5], "edited after reconnect")
	}
}

// TestE2E_EditSaveUpdatesInPlace verifies that an edit that's saved
// (Update via Save) keeps the same ID and updates the body in place —
// the audit trail (Created timestamp, position) survives.
func TestE2E_EditSaveUpdatesInPlace(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "first body", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("seed: %v\nstderr: %s", err, p.stderr.String())
	}
	rowsBefore := p.readCSV()
	if len(rowsBefore) != 2 {
		t.Fatalf("after seed: expected 1 row + header, got %d", len(rowsBefore))
	}
	originalID := rowsBefore[1][0]

	// Edit, change body, save.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='editComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.WaitVisible(`//div[contains(@class,'composer')]//strong[starts-with(normalize-space(text()), 'Editing comment on')]`, chromedp.BySearch),
		chromedp.Evaluate(`(() => {
			const t = document.querySelector('.composer textarea');
			t.value = 'updated body';
			return t.value;
		})()`, nil),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("edit + save: %v\nstderr: %s", err, p.stderr.String())
	}

	rowsAfter := p.readCSV()
	if len(rowsAfter) != 2 {
		t.Errorf("after update: CSV should still have 1 row + header, got %d", len(rowsAfter))
	}
	if rowsAfter[1][0] != originalID {
		t.Errorf("comment ID changed across edit: was %q, now %q (should be in-place update)", originalID, rowsAfter[1][0])
	}
	if rowsAfter[1][5] != "updated body" {
		t.Errorf("comment body after update = %q, want %q", rowsAfter[1][5], "updated body")
	}
}

// TestE2E_EditModeLabel verifies the composer says "Editing comment on Lx"
// after clicking Edit rather than the default "Comment on Lx".
func TestE2E_EditModeLabel(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "original body", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
		chromedp.Click(`button[name='editComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.composer`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("seed + edit: %v\nstderr: %s", err, p.stderr.String())
	}
	var label string
	if err := chromedp.Run(p.ctx, chromedp.Text(`.composer strong`, &label, chromedp.ByQuery)); err != nil {
		t.Fatalf("read composer label: %v", err)
	}
	if !strings.HasPrefix(label, "Editing") {
		t.Errorf("composer label = %q, want prefix 'Editing'", label)
	}
}

// TestE2E_FileFilter verifies the drawer search filter narrows the file list.
func TestE2E_FileFilter(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	var initialCount int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('#files-drawer button.file-btn').length`, &initialCount),
		chromedp.SendKeys(`#files-drawer input[name='filter']`, "fresh", chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond), // 200ms debounce + WS roundtrip
	); err != nil {
		t.Fatalf("type filter: %v", err)
	}
	var filteredCount int
	_ = chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('#files-drawer button.file-btn').length`, &filteredCount),
	)
	if filteredCount >= initialCount {
		t.Errorf("filter didn't reduce file count: before=%d after=%d", initialCount, filteredCount)
	}
	if filteredCount < 1 {
		t.Errorf("filter eliminated all files; expected 'fresh.go' to match")
	}
}

// TestE2E_MarkViewed verifies the viewed toggle flips and the row styles.
func TestE2E_MarkViewed(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	var hasViewedClass bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`(//button[@name='toggleViewed'])[1]`, chromedp.BySearch),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`!!document.querySelector('#files-drawer .file-row.is-viewed')`, &hasViewedClass),
	); err != nil {
		t.Fatalf("toggle viewed: %v\nstderr: %s", err, p.stderr.String())
	}
	if !hasViewedClass {
		t.Errorf("no .is-viewed row after ToggleViewed")
	}
}

// TestE2E_AllCommentsView verifies the comments pill toggles into an
// all-comments overview, and JumpToComment returns to the diff with the
// scroll-marker attribute set.
func TestE2E_AllCommentsView(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "first", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
		// Open all-comments view via the pill.
		chromedp.Click(`button[name='toggleCommentList']`, chromedp.ByQuery),
		chromedp.WaitVisible(`section.all-comments`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open all-comments: %v\nstderr: %s", err, p.stderr.String())
	}
	var jumpHasScrollMarker bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='jumpToComment']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		// JumpToComment sets ScrollToCommentID; the body picks up data-scroll-to-now
		// briefly, but the embedded scroll JS removes it on first render. Check the
		// inline comment is back on screen instead.
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('section.all-comments')`, &jumpHasScrollMarker),
	); err != nil {
		t.Fatalf("jump to comment: %v\nstderr: %s", err, p.stderr.String())
	}
	if jumpHasScrollMarker {
		t.Errorf("all-comments view still visible after jump")
	}
}

// TestE2E_ResolveComment verifies the resolve button toggles the
// Resolved flag on a comment, writes it to CSV with `resolved=true`, and
// the comment renders muted. The Resolve button becomes "Reopen" after.
func TestE2E_ResolveComment(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "needs resolving", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v", err)
	}

	// Resolve. Resolved comments are hidden by default, so the inline-comment
	// should disappear from the diff stream.
	var stillVisibleAfterResolve bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-comment button[name='toggleResolved']`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Evaluate(`!!document.querySelector('.inline-comment')`, &stillVisibleAfterResolve),
	); err != nil {
		t.Fatalf("resolve: %v\nstderr: %s", err, p.stderr.String())
	}
	if stillVisibleAfterResolve {
		t.Error("resolved comment should be hidden by default; .inline-comment still present")
	}

	// CSV row should have resolved=true (col 7).
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected 1 row + header, got %d: %v", len(rows), rows)
	}
	if rows[1][7] != "true" {
		t.Errorf("resolved column = %q, want 'true'", rows[1][7])
	}

	// Toggle "Show resolved" → the resolved comment reappears with is-resolved.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='toggleShowResolved']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment.is-resolved`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("toggle show resolved: %v\nstderr: %s", err, p.stderr.String())
	}

	// Button on the comment should now read "Reopen".
	var btnText string
	if err := chromedp.Run(p.ctx,
		chromedp.Text(`.inline-comment button[name='toggleResolved']`, &btnText, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("read button text: %v", err)
	}
	if strings.TrimSpace(btnText) != "Reopen" {
		t.Errorf("button text after resolve = %q, want 'Reopen'", btnText)
	}

	// Reopen — back to unresolved.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-comment button[name='toggleResolved']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rows = p.readCSV()
	if rows[1][7] != "false" {
		t.Errorf("resolved column after reopen = %q, want 'false'", rows[1][7])
	}
}

// TestE2E_FileViewToggle verifies the View file switch turns off the
// diff overlay: deleted lines disappear and the .code container gains
// the .file-view class. Toggling back restores diff mode. Driven via
// the desktop inline chip (1200px viewport); the mobile overflow-menu
// variant shares the same toggleFileView action so it isn't separately
// covered here.
//
// Fixture: edited.go has one del line (`return "hi"`) and one add line
// (`return "hello world"`), so we can assert "at least 1 del row
// visible in diff mode; 0 del rows visible in file mode".
func TestE2E_FileViewToggle(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")

	delRowsVisible := `Array.from(document.querySelectorAll('.line-row'))
		.filter(r => r.querySelector('button.line.del') && getComputedStyle(r).display !== 'none').length`

	// Diff mode default: .code lacks .file-view; del lines are visible.
	var fvBefore bool
	var delBefore int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.code').classList.contains('file-view')`, &fvBefore),
		chromedp.Evaluate(delRowsVisible, &delBefore),
	); err != nil {
		t.Fatalf("diff-mode query: %v", err)
	}
	if fvBefore {
		t.Error("default state should be diff mode (.code must not have .file-view class)")
	}
	if delBefore == 0 {
		t.Fatalf("fixture should have at least 1 visible del line in diff mode; got 0")
	}

	// Toggle to file view via the desktop chip in .toolbar-inline.
	var fvAfter bool
	var delAfter int
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.toolbar-inline button[name='toggleFileView']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.code.file-view`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.code').classList.contains('file-view')`, &fvAfter),
		chromedp.Evaluate(delRowsVisible, &delAfter),
	); err != nil {
		t.Fatalf("toggle to file view: %v\nstderr: %s", err, p.stderr.String())
	}
	if !fvAfter {
		t.Error(".file-view class should be applied after toggleFileView")
	}
	if delAfter != 0 {
		t.Errorf("file view should hide all del .line-rows; %d still visible", delAfter)
	}

	// Toggle back to diff mode — del lines should reappear, class removed.
	var fvFinal bool
	var delFinal int
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.toolbar-inline button[name='toggleFileView']`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('.code').classList.contains('file-view')`, &fvFinal),
		chromedp.Evaluate(delRowsVisible, &delFinal),
	); err != nil {
		t.Fatalf("toggle back: %v\nstderr: %s", err, p.stderr.String())
	}
	if fvFinal {
		t.Error("second toggle should revert to diff mode (.file-view removed)")
	}
	if delFinal == 0 {
		t.Error("del lines should reappear after toggling back to diff mode")
	}
}

// TestE2E_AutoSelectFirstFile verifies that landing on the page with
// no SelectedFile (initial connect) auto-loads the diff for the first
// file in the drawer, so the right pane is populated on first paint
// instead of showing the "Pick a file" empty state.
func TestE2E_AutoSelectFirstFile(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	// First file in the fixture (alphabetical sort): edited.go. The
	// viewer's file-head should display it, and there should be no
	// "Pick a file" empty placeholder.
	var headFilename string
	var hasEmptyPlaceholder bool
	if err := chromedp.Run(p.ctx,
		chromedp.Text(`main.viewer article header.file-head strong`, &headFilename, chromedp.ByQuery),
		chromedp.Evaluate(`!!Array.from(document.querySelectorAll('main.viewer p.empty')).find(p => p.textContent.includes('Pick a file'))`, &hasEmptyPlaceholder),
	); err != nil {
		t.Fatalf("post-mount query: %v\nstderr: %s", err, p.stderr.String())
	}
	if headFilename == "" {
		t.Error("file-head should be populated on initial load; got empty")
	}
	if hasEmptyPlaceholder {
		t.Error("'Pick a file' placeholder should not render when a file is auto-selected")
	}
	// The auto-selected file is the alphabetically-first one — for the
	// shared fixture (with the second commit + working-tree changes)
	// the working-tree files sorted alpha are: edited.go, fresh.go.
	// (history.go was committed in the second commit so it's unchanged
	// vs HEAD — it still appears in ListFiles since we show all files.)
	if headFilename != "edited.go" {
		t.Errorf("auto-selected file = %q, want %q (alphabetically first)", headFilename, "edited.go")
	}
}

// TestE2E_AllFilesOnCleanTree verifies the "diff as overlay" pivot:
// a repo with no working-tree changes still shows every tracked file
// in the drawer, not an empty "Nothing to review" page. Files without
// changes vs base render plainly when selected (every line is "ctx",
// no add/del coloring).
func TestE2E_AllFilesOnCleanTree(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoClean(t), 1200, 800)
	p.waitReady()

	// Clean-tree fixture commits alpha.go and beta.go — both should be
	// listed in the drawer even though `git diff HEAD` is empty.
	var fileBtnCount int
	var hasAlpha, hasBeta bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('button.file-btn').length`, &fileBtnCount),
		chromedp.Evaluate(`!!Array.from(document.querySelectorAll('button.file-btn')).find(b => b.textContent.includes('alpha.go'))`, &hasAlpha),
		chromedp.Evaluate(`!!Array.from(document.querySelectorAll('button.file-btn')).find(b => b.textContent.includes('beta.go'))`, &hasBeta),
	); err != nil {
		t.Fatalf("file list query: %v\nstderr: %s", err, p.stderr.String())
	}
	if fileBtnCount < 2 {
		t.Errorf("clean tree should still expose tracked files; got %d file buttons", fileBtnCount)
	}
	if !hasAlpha || !hasBeta {
		t.Errorf("expected both alpha.go and beta.go in drawer; hasAlpha=%v hasBeta=%v", hasAlpha, hasBeta)
	}

	// Selecting an unchanged file should render it with ctx-only lines
	// (no add/del classes anywhere in .code), since there's no diff to overlay.
	var addClassCount, delClassCount, ctxClassCount int
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`//button[@name='selectFile' and contains(., 'alpha.go')]`, chromedp.BySearch),
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='alpha.go']`, chromedp.BySearch),
		chromedp.Evaluate(`document.querySelectorAll('.code button.line.add').length`, &addClassCount),
		chromedp.Evaluate(`document.querySelectorAll('.code button.line.del').length`, &delClassCount),
		chromedp.Evaluate(`document.querySelectorAll('.code button.line.ctx').length`, &ctxClassCount),
	); err != nil {
		t.Fatalf("select unchanged file: %v\nstderr: %s", err, p.stderr.String())
	}
	if addClassCount != 0 || delClassCount != 0 {
		t.Errorf("unchanged file should have zero add/del lines; got add=%d del=%d", addClassCount, delClassCount)
	}
	if ctxClassCount == 0 {
		t.Errorf("unchanged file should render its content as ctx lines; got 0")
	}
}

// TestE2E_BasePickerSwap verifies the runtime base picker:
//
//  1. Selecting an option from the dropdown (#base-input) auto-submits
//     via lvt-on:change="setBase" and updates state.Base.
//  2. The custom-ref disclosure (a <details> with a freeform input)
//     accepts arbitrary refs for cases the dropdown doesn't list,
//     and invalid refs surface BaseError without mutating state.Base.
func TestE2E_BasePickerSwap(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	// Dropdown swap: select HEAD~1, expect change event to fire SetBase
	// and the select value to reflect the chosen option.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(() => {
			const s = document.querySelector('#base-input');
			s.value = "HEAD~1";
			s.dispatchEvent(new Event("change", {bubbles: true}));
		})()`, nil),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("dropdown swap: %v\nstderr: %s", err, p.stderr.String())
	}
	var baseAfterSwap string
	if err := chromedp.Run(p.ctx,
		chromedp.Value(`#base-input`, &baseAfterSwap, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("read select value: %v", err)
	}
	if baseAfterSwap != "HEAD~1" {
		t.Errorf("base after dropdown swap = %q, want %q", baseAfterSwap, "HEAD~1")
	}

	// Custom-ref disclosure: open <details>, type an invalid ref, submit.
	// BaseError surfaces, dropdown value stays at the previous successful
	// base (HEAD~1).
	var errAfterBad string
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.base-custom summary`, chromedp.ByQuery),
		chromedp.WaitVisible(`#base-custom-input`, chromedp.ByQuery),
		chromedp.SendKeys(`#base-custom-input`, "definitely-not-a-ref-xyz", chromedp.ByQuery),
		chromedp.Click(`.base-custom button[name='setBase']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.base-error`, chromedp.ByQuery),
		chromedp.Text(`.base-error`, &errAfterBad, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("invalid custom ref: %v\nstderr: %s", err, p.stderr.String())
	}
	if !strings.Contains(errAfterBad, "Unknown ref") {
		t.Errorf("invalid-ref error = %q, want substring 'Unknown ref'", errAfterBad)
	}
	var baseAfterBad string
	if err := chromedp.Run(p.ctx,
		chromedp.Value(`#base-input`, &baseAfterBad, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("read select after bad ref: %v", err)
	}
	if baseAfterBad != "HEAD~1" {
		t.Errorf("bad ref shouldn't change Base; got %q, want %q", baseAfterBad, "HEAD~1")
	}
}

// TestE2E_MobileOverflowMenu verifies that on a phone-sized viewport the
// secondary chips (All comments, Show resolved) live behind the 3-dots
// overflow menu rather than overflowing the toolbar. Tapping the
// 3-dots opens the menu; tapping an entry fires the action and closes
// the menu. Backdrop tap closes without firing anything.
//
// Why this test exists: the desktop inline-chip group caused horizontal
// overflow on mobile (the "Show resolved" toggle was clipped by the
// Hand off button). The 3-dots pattern keeps mobile chrome lean while
// desktop keeps the chips inline for one-tap access.
func TestE2E_MobileOverflowMenu(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 375, 812)
	if err := chromedp.Run(p.ctx,
		chromedp.EmulateViewport(375, 812),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`header.bar`, chromedp.ByQuery),
		// Defer-script + WS connect takes ~1-2s on the syntax-highlighted
		// page; without this the .more-trigger click below dispatches
		// before the framework has attached its event delegation listener.
		chromedp.Sleep(2 * time.Second),
	); err != nil {
		t.Fatalf("mobile boot: %v\nstderr: %s", err, p.stderr.String())
	}

	// 3-dots trigger must be visible on mobile; inline chip group must be hidden.
	var triggerVisible, inlineVisible bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.more-trigger')).display !== 'none'`, &triggerVisible),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.toolbar-inline')).display === 'none'`, &inlineVisible),
	); err != nil {
		t.Fatalf("chrome visibility query: %v", err)
	}
	if !triggerVisible {
		t.Error("3-dots overflow trigger should be visible on mobile (375px)")
	}
	if !inlineVisible {
		t.Error("inline chip group should be hidden on mobile (only visible at >=900px)")
	}

	// Open the overflow menu — the menu always contains an info row even
	// when no comments exist, so we don't need to add a comment first to
	// verify the open/close behavior. Verify menu opens, contains the
	// expected role=menu element, and that backdrop tap closes it.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.more-trigger').click()`, nil),
		chromedp.WaitVisible(`.more-menu.is-open`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open overflow menu: %v\nstderr: %s", err, p.stderr.String())
	}

	// Backdrop tap closes the menu (CloseMoreMenu action).
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.more-menu-backdrop.is-open').click()`, nil),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("close via backdrop: %v\nstderr: %s", err, p.stderr.String())
	}
	var menuStillOpen bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.more-menu').classList.contains('is-open')`, &menuStillOpen),
	); err != nil {
		t.Fatalf("menu state query: %v", err)
	}
	if menuStillOpen {
		t.Error("menu should close when backdrop is tapped")
	}
}

// TestE2E_MobileScrollBounds verifies horizontal page overflow is
// clamped (no chrome bleed past the viewport) while leaving vertical
// scroll free. body must NOT be `overflow-y: hidden` — that breaks
// touch scrolling on nested overflow:auto containers in iOS Safari.
// main.viewer is the y-scroll container and gets `overscroll-behavior:
// contain` to keep rubber-band scoped to itself.
func TestE2E_MobileScrollBounds(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 375, 812)
	if err := chromedp.Run(p.ctx,
		chromedp.EmulateViewport(375, 812),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`header.bar`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("mobile boot: %v\nstderr: %s", err, p.stderr.String())
	}

	var bodyOverflowY, bodyOverflowX, viewerOverflowY, viewerOverscroll string
	var pageScrollW, pageClientW int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`getComputedStyle(document.body).overflowY`, &bodyOverflowY),
		chromedp.Evaluate(`getComputedStyle(document.body).overflowX`, &bodyOverflowX),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('main.viewer')).overflowY`, &viewerOverflowY),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('main.viewer')).overscrollBehavior`, &viewerOverscroll),
		chromedp.Evaluate(`document.documentElement.scrollWidth`, &pageScrollW),
		chromedp.Evaluate(`document.documentElement.clientWidth`, &pageClientW),
	); err != nil {
		t.Fatalf("scroll bounds query: %v", err)
	}
	if bodyOverflowX != "hidden" {
		t.Errorf("body overflow-x = %q, want 'hidden' (clamp horizontal)", bodyOverflowX)
	}
	if bodyOverflowY == "hidden" {
		t.Errorf("body overflow-y = %q; must NOT be 'hidden' — that breaks touch scrolling on nested overflow:auto in iOS Safari", bodyOverflowY)
	}
	if viewerOverflowY != "auto" && viewerOverflowY != "scroll" {
		t.Errorf("main.viewer overflow-y = %q, want 'auto' or 'scroll' (it owns y-scroll)", viewerOverflowY)
	}
	if viewerOverscroll != "contain" {
		t.Errorf("main.viewer overscroll-behavior = %q, want 'contain' (scope rubber-band)", viewerOverscroll)
	}
	if pageScrollW > pageClientW {
		t.Errorf("page horizontal scroll extent %d > viewport width %d — chrome is overflowing", pageScrollW, pageClientW)
	}
}

// TestE2E_UndoDelete verifies the undo-toast appears after a delete and
// restores the comment when clicked.
func TestE2E_UndoDelete(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "to be deleted", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v", err)
	}

	// Delete it (via the dialog).
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('dialog[id^="confirm-delete-"]').showModal()`, nil),
		chromedp.WaitVisible(`dialog[id^='confirm-delete-'][open] button[name='deleteComment']`, chromedp.ByQuery),
		chromedp.Click(`dialog[id^='confirm-delete-'][open] button[name='deleteComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.undo-toast`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("delete + see toast: %v\nstderr: %s", err, p.stderr.String())
	}

	// CSV should be empty (header only).
	rows := p.readCSV()
	if len(rows) != 1 {
		t.Errorf("post-delete CSV: got %d rows, want 1 (header only)", len(rows))
	}

	// Click Undo.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.undo-toast button[name='undoDelete']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("click undo: %v\nstderr: %s", err, p.stderr.String())
	}

	rows = p.readCSV()
	if len(rows) != 2 {
		t.Errorf("post-undo CSV: got %d rows, want 2 (header + 1)", len(rows))
	}
	if len(rows) >= 2 && !strings.Contains(rows[1][5], "to be deleted") {
		t.Errorf("post-undo CSV body = %q, want 'to be deleted'", rows[1][5])
	}
}
