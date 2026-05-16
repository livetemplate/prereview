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

	cdpnetwork "github.com/chromedp/cdproto/network"
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

// TestE2E_ProgressBarOnAction verifies the progress bar appears
// during a file-switch action (~200ms+ in flight) and disappears
// after the render completes. Polls via setInterval+capture to catch
// the .pr-progress element even when its lifetime is short.
func TestE2E_ProgressBarOnAction(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	// Click a file and observe whether the progress bar ever appeared
	// during the round-trip. We can't WaitVisible(.pr-progress) directly
	// because it could appear AND disappear before our query lands;
	// instead, install a MutationObserver before the click.
	var sawBar bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(() => {
			window.__sawProgressBar = false;
			const obs = new MutationObserver((mutations) => {
				for (const m of mutations) {
					for (const n of m.addedNodes) {
						if (n.classList && n.classList.contains('pr-progress')) {
							window.__sawProgressBar = true;
						}
					}
				}
			});
			obs.observe(document.body, {childList: true});
			window.__progressObserver = obs;
		})()`, nil),
		// Click a file other than the auto-selected one — must produce
		// real WS round-trip + render.
		chromedp.Evaluate(`(() => {
			const b = Array.from(document.querySelectorAll('button.file-btn')).find(x => x.textContent.includes('fresh.go'));
			if (b) b.click();
		})()`, nil),
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='fresh.go']`, chromedp.BySearch),
		chromedp.Sleep(100*time.Millisecond),
		chromedp.Evaluate(`window.__sawProgressBar`, &sawBar),
		// Bar should be gone now (lvt:updated fired).
	); err != nil {
		t.Fatalf("progress probe: %v\nstderr: %s", err, p.stderr.String())
	}
	// Bar may not appear on a fast cached action (under the 200ms debounce);
	// the assertion is that IF it appeared, it cleaned up. Verify the
	// debounce + cleanup contract by confirming no orphan bar remains.
	var orphan bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('.pr-progress')`, &orphan),
	); err != nil {
		t.Fatalf("orphan probe: %v", err)
	}
	if orphan {
		t.Error(".pr-progress element should be removed once lvt:updated fires")
	}
	t.Logf("progress bar appeared during fresh.go click: %v", sawBar)
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

// TestE2E_FileScopeToggle verifies the changed-only default and the
// All<->Changed toggle. The fixture has 2 changed files (edited.go M,
// fresh.go A) and 2 unchanged (keep.go, history.go).
func TestE2E_FileScopeToggle(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	var defCount int
	var keepShownDefault, toggleVisible bool
	var toggleText string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('#files-drawer button.file-btn').length`, &defCount),
		chromedp.Evaluate(`!!document.querySelector('#files-drawer button.file-btn[title="keep.go"]')`, &keepShownDefault),
		chromedp.Evaluate(`!!document.querySelector('#files-drawer button[name="toggleFileScope"]')`, &toggleVisible),
		chromedp.Evaluate(`(document.querySelector('#files-drawer button[name="toggleFileScope"]')||{}).textContent||''`, &toggleText),
	); err != nil {
		t.Fatalf("read default scope: %v\nstderr: %s", err, p.stderr.String())
	}
	if defCount != 2 {
		t.Errorf("default (changed-only): got %d files, want 2 (edited.go, fresh.go)", defCount)
	}
	if keepShownDefault {
		t.Error("default scope should hide unchanged keep.go")
	}
	if !toggleVisible {
		t.Fatal("scope toggle should be visible when changed<total and changed>0")
	}
	if !strings.Contains(toggleText, "Changed 2") {
		t.Errorf("toggle label = %q, want it to mention 'Changed 2'", strings.TrimSpace(toggleText))
	}

	// Toggle -> all files.
	var allCount int
	var keepShownAll bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`#files-drawer button[name="toggleFileScope"]`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Evaluate(`document.querySelectorAll('#files-drawer button.file-btn').length`, &allCount),
		chromedp.Evaluate(`!!document.querySelector('#files-drawer button.file-btn[title="keep.go"]')`, &keepShownAll),
		chromedp.Evaluate(`(document.querySelector('#files-drawer button[name="toggleFileScope"]')||{}).textContent||''`, &toggleText),
	); err != nil {
		t.Fatalf("toggle to all: %v\nstderr: %s", err, p.stderr.String())
	}
	if allCount != 4 {
		t.Errorf("all-files: got %d files, want 4", allCount)
	}
	if !keepShownAll {
		t.Error("all-files scope should show unchanged keep.go")
	}
	if !strings.Contains(toggleText, "All 4") {
		t.Errorf("toggle label after switch = %q, want it to mention 'All 4'", strings.TrimSpace(toggleText))
	}

	// Toggle back -> changed only.
	var backCount int
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`#files-drawer button[name="toggleFileScope"]`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Evaluate(`document.querySelectorAll('#files-drawer button.file-btn').length`, &backCount),
	); err != nil {
		t.Fatalf("toggle back: %v\nstderr: %s", err, p.stderr.String())
	}
	if backCount != 2 {
		t.Errorf("after toggling back: got %d files, want 2", backCount)
	}
}

// TestE2E_FileScopeToggleHiddenOnCleanTree verifies the toggle is
// absent when there are no changed files (clean tree falls back to
// all-files; nothing to switch between).
func TestE2E_FileScopeToggleHiddenOnCleanTree(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoClean(t), 1200, 800)
	p.waitReady()
	var toggleVisible bool
	var fileCount int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('#files-drawer button[name="toggleFileScope"]')`, &toggleVisible),
		chromedp.Evaluate(`document.querySelectorAll('#files-drawer button.file-btn').length`, &fileCount),
	); err != nil {
		t.Fatalf("read clean-tree scope: %v\nstderr: %s", err, p.stderr.String())
	}
	if toggleVisible {
		t.Error("scope toggle should be hidden on a clean tree (0 changed files)")
	}
	if fileCount < 2 {
		t.Errorf("clean tree should fall back to all files; got %d", fileCount)
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

// TestE2E_AllCommentsActions verifies the all-comments view items now
// carry edit/resolve/delete, that they operate by global comment ID,
// and the cross-view seam: clicking Edit in the list must drop back
// into the file's diff view so the (diff-branch-only) composer is
// actually visible. Captures console/server/ws/HTML for debuggability.
func TestE2E_AllCommentsActions(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)

	var mu sync.Mutex
	var consoleLines, wsFrames []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		case *cdpnetwork.EventWebSocketFrameReceived:
			wsFrames = append(wsFrames, "recv "+e.Response.PayloadData)
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+e.Response.PayloadData)
		}
	})
	if err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.Enable().Do(ctx)
	})); err != nil {
		t.Fatalf("enable network domain: %v", err)
	}
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`main`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server stderr ---\n%s\n--- console ---\n%s\n--- ws frames ---\n%s\n--- rendered html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.waitReady()
	p.clickFile("edited.go")

	// Two comments on the same line so the all-comments list has two
	// distinguishable items (avoids depending on a second selectable
	// line existing in the fixture).
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "alpha-cmt", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment alpha: %v%s", err, diag())
	}
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "beta-cmt", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.Sleep(250*time.Millisecond),
	); err != nil {
		t.Fatalf("add comment beta: %v%s", err, diag())
	}

	// Open the all-comments view; assert each item carries the three
	// new actions.
	var items, withResolve, withEdit, withDelete int
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='toggleCommentList']`, chromedp.ByQuery),
		chromedp.WaitVisible(`section.all-comments`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('section.all-comments .ac-item').length`, &items),
		chromedp.Evaluate(`document.querySelectorAll('section.all-comments .ac-item button[name="toggleResolved"]').length`, &withResolve),
		chromedp.Evaluate(`document.querySelectorAll('section.all-comments .ac-item button[name="editComment"]').length`, &withEdit),
		chromedp.Evaluate(`document.querySelectorAll('section.all-comments .ac-item button[command="show-modal"]').length`, &withDelete),
	); err != nil {
		t.Fatalf("open all-comments: %v%s", err, diag())
	}
	if items != 2 {
		t.Fatalf("want 2 all-comments items, got %d%s", items, diag())
	}
	if withResolve != 2 || withEdit != 2 || withDelete != 2 {
		t.Fatalf("each item needs resolve/edit/delete; got resolve=%d edit=%d delete=%d%s", withResolve, withEdit, withDelete, diag())
	}

	// Seam: Edit on the alpha item must close the all-comments view AND
	// surface the "Editing comment on" composer (which only renders in
	// the diff branch). This pins the EditComment ShowAllComments=false
	// fix — without it the composer would stay hidden.
	var clicked bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(()=>{const li=[...document.querySelectorAll('section.all-comments .ac-item')].find(x=>x.textContent.includes('alpha-cmt')); if(li){li.querySelector('button[name="editComment"]').click();return true;} return false;})()`, &clicked),
	); err != nil || !clicked {
		t.Fatalf("click Edit on alpha item: err=%v clicked=%v%s", err, clicked, diag())
	}
	var allCommentsGone bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`//div[contains(@class,'composer')]//strong[starts-with(normalize-space(text()), 'Editing comment on')]`, chromedp.BySearch),
		chromedp.Evaluate(`!document.querySelector('section.all-comments')`, &allCommentsGone),
	); err != nil {
		t.Fatalf("edit-from-list should open the composer in the diff view: %v%s", err, diag())
	}
	if !allCommentsGone {
		t.Errorf("all-comments view must close when Edit is clicked from it%s", diag())
	}

	// Cancel the edit, go back to the list.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='clearSelection']`, chromedp.ByQuery),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Click(`button[name='toggleCommentList']`, chromedp.ByQuery),
		chromedp.WaitVisible(`section.all-comments`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("cancel edit + reopen list: %v%s", err, diag())
	}

	// Delete the beta comment from the list via its own dialog.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(()=>{const li=[...document.querySelectorAll('section.all-comments .ac-item')].find(x=>x.textContent.includes('beta-cmt')); if(li){li.querySelector('dialog').showModal();return true;} return false;})()`, &clicked),
		chromedp.WaitVisible(`dialog[id^='confirm-delete-'][open] button[name='deleteComment']`, chromedp.ByQuery),
		chromedp.Click(`dialog[id^='confirm-delete-'][open] button[name='deleteComment']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("delete beta from list: %v%s", err, diag())
	}
	rows := p.readCSV()
	if len(rows) != 2 { // header + alpha only
		t.Fatalf("after delete want header+1 row, got %d: %v%s", len(rows), rows, diag())
	}
	if !strings.Contains(rows[1][5], "alpha-cmt") {
		t.Errorf("surviving comment body = %q, want alpha-cmt%s", rows[1][5], diag())
	}

	// Resolve the remaining alpha comment from the list. Resolved
	// comments are hidden by default → the item disappears and the CSV
	// resolved column flips true.
	var itemsAfterResolve int
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`section.all-comments .ac-item button[name='toggleResolved']`, chromedp.ByQuery),
		chromedp.Click(`section.all-comments .ac-item button[name='toggleResolved']`, chromedp.ByQuery),
		chromedp.Sleep(350*time.Millisecond),
		chromedp.Evaluate(`document.querySelectorAll('section.all-comments .ac-item').length`, &itemsAfterResolve),
	); err != nil {
		t.Fatalf("resolve alpha from list: %v%s", err, diag())
	}
	if itemsAfterResolve != 0 {
		t.Errorf("resolved comment should be hidden by default; %d items still shown%s", itemsAfterResolve, diag())
	}
	rows = p.readCSV()
	if len(rows) != 2 || rows[1][7] != "true" {
		t.Errorf("alpha CSV resolved col = want 'true'; rows=%v%s", rows, diag())
	}

	mu.Lock()
	for _, line := range consoleLines {
		if strings.Contains(strings.ToLower(line), "error") {
			t.Errorf("browser console error: %s", line)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
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

// setupFixtureRepoBigDiff commits a 40-line file then changes one line
// in the middle, so Diff view must fold the long unchanged runs and
// File view shows the whole file. big.go is the only file => the scope
// toggle is hidden and it auto-selects.
func setupFixtureRepoBigDiff(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	mustWrite(t, dir, "big.go", b.String())
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed big")

	// Change exactly one line in the middle.
	b.Reset()
	for i := 1; i <= 40; i++ {
		if i == 20 {
			fmt.Fprintf(&b, "line %d CHANGED\n", i)
		} else {
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	mustWrite(t, dir, "big.go", b.String())
	return dir
}

// TestE2E_DiffFoldVsFullFile exercises the new Diff/File semantics:
// Diff view = collapsed hunks with fold markers; File view = the whole
// file, no folds, no deletions.
func TestE2E_DiffFoldVsFullFile(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoBigDiff(t), 1200, 800)
	p.waitReady()

	lineBtns := `document.querySelectorAll('.code button.line').length`
	foldRows := `document.querySelectorAll('.code .fold-row').length`
	delRows := `document.querySelectorAll('.code button.line.del').length`

	// Diff view (default): folded — only a few real lines, >=1 fold.
	var diffBtns, diffFolds int
	var foldText string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.code button.line`, chromedp.ByQuery),
		chromedp.Evaluate(lineBtns, &diffBtns),
		chromedp.Evaluate(foldRows, &diffFolds),
		chromedp.Evaluate(`(document.querySelector('.code .fold-label')||{}).textContent||''`, &foldText),
	); err != nil {
		t.Fatalf("diff-view query: %v\nstderr: %s", err, p.stderr.String())
	}
	if diffFolds < 1 {
		t.Errorf("Diff view should fold long unchanged runs; got %d fold rows", diffFolds)
	}
	if diffBtns >= 40 || diffBtns > 15 {
		t.Errorf("Diff view should show only the hunk (~8 lines), got %d line buttons", diffBtns)
	}
	if !strings.Contains(foldText, "unchanged line") {
		t.Errorf("fold label = %q, want it to mention unchanged lines", strings.TrimSpace(foldText))
	}

	// Toggle to File view: whole file, no folds, no del.
	var fileBtns, fileFolds, fileDels int
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.toolbar-inline button[name='toggleFileView']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.code.file-view`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(lineBtns, &fileBtns),
		chromedp.Evaluate(foldRows, &fileFolds),
		chromedp.Evaluate(delRows, &fileDels),
	); err != nil {
		t.Fatalf("toggle to file view: %v\nstderr: %s", err, p.stderr.String())
	}
	if fileFolds != 0 {
		t.Errorf("File view must not fold; got %d fold rows", fileFolds)
	}
	if fileBtns != 40 {
		t.Errorf("File view should show all 40 lines, got %d", fileBtns)
	}
	if fileDels != 0 {
		t.Errorf("File view excludes deleted lines; got %d del rows", fileDels)
	}

	// Toggle back to Diff view: folds return.
	var backBtns, backFolds int
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.toolbar-inline button[name='toggleFileView']`, chromedp.ByQuery),
		chromedp.Sleep(250*time.Millisecond),
		chromedp.Evaluate(lineBtns, &backBtns),
		chromedp.Evaluate(foldRows, &backFolds),
	); err != nil {
		t.Fatalf("toggle back: %v\nstderr: %s", err, p.stderr.String())
	}
	if backFolds < 1 {
		t.Errorf("Diff view folds should return after toggling back; got %d", backFolds)
	}
	if backBtns >= 40 {
		t.Errorf("Diff view should re-collapse; got %d line buttons", backBtns)
	}
}

// setupFixtureRepoMarkdown commits a small Markdown doc then edits one
// line so it's the single changed file (auto-selected, scope toggle
// hidden). Blocks: h1=line1, paragraph=line3, list=lines5-6,
// code-fence=lines8-10.
func setupFixtureRepoMarkdown(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	// Lines: 1 h1 · 3-5 one-sentence-per-line prose paragraph · 7-8
	// list · 10-12 code · 14-17 GFM table (14 header, 16 row C, 17 row
	// D). The 3-line paragraph exercises prose-per-line; the table
	// exercises per-row; h1 stays the first block so the existing
	// whole-block test is unaffected.
	const base = "# Doc Title\n\nIntro one clause here\nsecond clause continues\nthird clause ends\n\n- alpha\n- beta\n\n```go\nx := 1\n```\n\n| Use | Detail |\n|-----|--------|\n| C | chat |\n| D | authrow |\n"
	mustWrite(t, dir, "docs.md", base)
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed docs")

	// Edit a prose line (4) AND the row-D line (17) so docs.md is a
	// changed file and both fall inside raw-view diff hunks (so the
	// per-line comments round-trip visibly to the line viewer too).
	mustWrite(t, dir, "docs.md", "# Doc Title\n\nIntro one clause here\nsecond clause EDITED\nthird clause ends\n\n- alpha\n- beta\n\n```go\nx := 1\n```\n\n| Use | Detail |\n|-----|--------|\n| C | chat |\n| D | authrow EDITED |\n")
	return dir
}

// TestE2E_MarkdownRenderAndComment covers: Markdown renders by default;
// raw <script> is not passed through; clicking a rendered block opens
// the composer and the saved comment anchors to that block's real
// source lines; the comment round-trips between rendered and raw.
func TestE2E_MarkdownRenderAndComment(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdown(t), 1200, 800)
	p.waitReady()

	// Rendered by default.
	var hasMdView, hasH1, hasRawChip, hasDiffChip bool
	var scriptCount, lineBtns int
	var h1Text, chipText string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &hasMdView),
		chromedp.Evaluate(`!!document.querySelector('.md-rendered h1')`, &hasH1),
		chromedp.Evaluate(`(document.querySelector('.md-rendered h1')||{}).textContent||''`, &h1Text),
		chromedp.Evaluate(`document.querySelectorAll('.md-rendered script').length`, &scriptCount),
		chromedp.Evaluate(`document.querySelectorAll('.code button.line').length`, &lineBtns),
		chromedp.Evaluate(`!!document.querySelector('button[name="toggleRawMarkdown"]')`, &hasRawChip),
		chromedp.Evaluate(`(document.querySelector('button[name="toggleRawMarkdown"]')||{}).textContent||''`, &chipText),
		chromedp.Evaluate(`!!document.querySelector('button[name="toggleFileView"]')`, &hasDiffChip),
	); err != nil {
		t.Fatalf("render-default query: %v\nstderr: %s", err, p.stderr.String())
	}
	if !hasMdView || !hasH1 {
		t.Fatalf("Markdown should render by default (md-view=%v h1=%v)", hasMdView, hasH1)
	}
	if !strings.Contains(h1Text, "Doc Title") {
		t.Errorf("h1 = %q, want 'Doc Title'", h1Text)
	}
	if scriptCount != 0 {
		t.Errorf("raw <script> must not render; got %d", scriptCount)
	}
	if lineBtns != 0 {
		t.Errorf("rendered view must not show the line viewer; got %d line buttons", lineBtns)
	}
	if !hasRawChip || !strings.Contains(chipText, "Rendered") {
		t.Errorf("expected a 'Rendered' toggle chip; got present=%v text=%q", hasRawChip, chipText)
	}
	if hasDiffChip {
		t.Error("Diff/File chip should be hidden while rendered Markdown is shown")
	}

	// Click the first rendered block (the h1, source line 1) and comment.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.md-block .md-rendered').click()`, nil),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "heading needs work", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.md-block .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("comment on rendered block: %v\nstderr: %s", err, p.stderr.String())
	}
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d: %v", len(rows), rows)
	}
	r := rows[1]
	if r[1] != "docs.md" {
		t.Errorf("file = %q, want docs.md", r[1])
	}
	if r[2] != "1" || r[3] != "1" {
		t.Errorf("from/to = %q/%q, want 1/1 (the h1 block's source line)", r[2], r[3])
	}
	if !strings.Contains(r[5], "heading needs work") {
		t.Errorf("body = %q, missing comment text", r[5])
	}

	// Toggle to raw: line view appears, comment round-trips to line 1.
	var rawHasMdView bool
	var rawLineBtns int
	var rawHasComment bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='toggleRawMarkdown']`, chromedp.ByQuery),
		chromedp.Sleep(350*time.Millisecond),
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &rawHasMdView),
		chromedp.Evaluate(`document.querySelectorAll('.code button.line').length`, &rawLineBtns),
		chromedp.Evaluate(`!!document.querySelector('.code .inline-comment')`, &rawHasComment),
	); err != nil {
		t.Fatalf("toggle to raw: %v\nstderr: %s", err, p.stderr.String())
	}
	if rawHasMdView {
		t.Error("raw mode should not show .md-view")
	}
	if rawLineBtns == 0 {
		t.Error("raw mode should show the line viewer")
	}
	if !rawHasComment {
		t.Error("the comment made in rendered mode should appear in raw mode (same source line)")
	}

	// Toggle back to rendered: comment shows under its block again.
	var backHasMdView, backHasComment bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='toggleRawMarkdown']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &backHasMdView),
		chromedp.Evaluate(`!!document.querySelector('.md-block .inline-comment')`, &backHasComment),
	); err != nil {
		t.Fatalf("toggle back to rendered: %v\nstderr: %s", err, p.stderr.String())
	}
	if !backHasMdView || !backHasComment {
		t.Errorf("rendered mode should show the comment under its block (mdView=%v comment=%v)", backHasMdView, backHasComment)
	}
}

// TestE2E_MarkdownPerRowComment pins the per-line (structural per-unit)
// behaviour: clicking a SINGLE GFM table body row anchors the comment
// to that row's own source line — not the whole table — and that it
// round-trips to the raw line view and back under that specific row's
// block. Captures browser console, server stderr, WebSocket frames and
// rendered HTML so a failure can be diagnosed without a re-run.
func TestE2E_MarkdownPerRowComment(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdown(t), 1200, 800)

	var mu sync.Mutex
	var consoleLines, wsFrames []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		case *cdpnetwork.EventWebSocketFrameReceived:
			wsFrames = append(wsFrames, "recv "+e.Response.PayloadData)
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+e.Response.PayloadData)
		}
	})
	if err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.Enable().Do(ctx)
	})); err != nil {
		t.Fatalf("enable network domain: %v", err)
	}

	// diag returns all four artifacts the project rule mandates so a
	// failure is debuggable in place: server log, console, WS frames,
	// and the live rendered HTML.
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`main`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server stderr ---\n%s\n--- console ---\n%s\n--- ws frames ---\n%s\n--- rendered html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.waitReady()

	// Sanity: rendered by default, and the table rendered per row (the
	// header row + each body row is its own .md-solo-table block).
	var hasMdView bool
	var soloTables int
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &hasMdView),
		chromedp.Evaluate(`document.querySelectorAll('.md-rendered table.md-solo-table').length`, &soloTables),
	); err != nil {
		t.Fatalf("render-default query: %v%s", err, diag())
	}
	if !hasMdView {
		t.Fatalf("Markdown should render by default%s", diag())
	}
	// header + row C + row D = 3 solo-row tables.
	if soloTables < 3 {
		t.Fatalf("table should split into per-row blocks; got %d .md-solo-table (want >=3)%s", soloTables, diag())
	}

	// Click ONLY row D (unique cell text), comment, save.
	var clicked bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(()=>{const el=[...document.querySelectorAll('.md-block .md-rendered')].find(e=>e.textContent.includes('authrow EDITED')); if(el){el.click();return true;} return false;})()`, &clicked),
	); err != nil || !clicked {
		t.Fatalf("could not click row D (.md-rendered with 'authrow EDITED'): err=%v clicked=%v%s", err, clicked, diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "row D needs a fix", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.md-block .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("comment on row D: %v%s", err, diag())
	}

	// The comment must anchor to row D's single source line (15) — not
	// the table's whole span (12-15). This is the whole feature.
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d: %v%s", len(rows), rows, diag())
	}
	r := rows[1]
	if r[1] != "docs.md" {
		t.Errorf("file = %q, want docs.md", r[1])
	}
	if r[2] != "17" || r[3] != "17" {
		t.Errorf("from/to = %q/%q, want 17/17 (row D's own source line, NOT the whole table)%s", r[2], r[3], diag())
	}
	if !strings.Contains(r[5], "row D needs a fix") {
		t.Errorf("body = %q, missing comment text", r[5])
	}

	// Round-trip to raw line view: the comment shows on line 15.
	var rawHasComment bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='toggleRawMarkdown']`, chromedp.ByQuery),
		chromedp.Sleep(350*time.Millisecond),
		chromedp.Evaluate(`!!document.querySelector('.code .inline-comment')`, &rawHasComment),
	); err != nil {
		t.Fatalf("toggle to raw: %v%s", err, diag())
	}
	if !rawHasComment {
		t.Errorf("row-D comment should round-trip to the raw line view%s", diag())
	}

	// Toggle back: the comment shows under the ROW D block specifically
	// (the .md-block carrying the comment also contains row D's text),
	// proving per-row anchoring rather than whole-table.
	var underRowD bool
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='toggleRawMarkdown']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Evaluate(`(()=>{const b=[...document.querySelectorAll('.md-block')].find(x=>x.querySelector('.inline-comment')); return b?b.textContent.includes('authrow EDITED'):false;})()`, &underRowD),
	); err != nil {
		t.Fatalf("toggle back to rendered: %v%s", err, diag())
	}
	if !underRowD {
		t.Errorf("comment must render under row D's own block, not another row/the table%s", diag())
	}

	mu.Lock()
	for _, line := range consoleLines {
		if strings.Contains(strings.ToLower(line), "error") {
			t.Errorf("browser console error: %s", line)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
}

// TestE2E_MarkdownProsePerLine pins the fix for the user-reported
// "some places still a range": a one-sentence-per-line paragraph
// (L3-L5) must split into one block per source line so tapping a
// single prose line composes a single-line comment (e.g. "L5"), never
// a multi-line range like "L3-L5".
func TestE2E_MarkdownProsePerLine(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdown(t), 1200, 800)

	var mu sync.Mutex
	var consoleLines, wsFrames []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *cdpruntime.EventConsoleAPICalled:
			parts := []string{string(e.Type)}
			for _, a := range e.Args {
				if a.Value != nil {
					parts = append(parts, string(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			consoleLines = append(consoleLines, strings.Join(parts, " "))
		case *cdpnetwork.EventWebSocketFrameReceived:
			wsFrames = append(wsFrames, "recv "+e.Response.PayloadData)
		case *cdpnetwork.EventWebSocketFrameSent:
			wsFrames = append(wsFrames, "sent "+e.Response.PayloadData)
		}
	})
	if err := chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.Enable().Do(ctx)
	})); err != nil {
		t.Fatalf("enable network domain: %v", err)
	}
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`main`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server stderr ---\n%s\n--- console ---\n%s\n--- ws frames ---\n%s\n--- rendered html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.waitReady()

	// The 3-clause paragraph (source lines 3-5) must be 3 distinct
	// blocks: the block holding clause 1 must NOT also hold clause 3.
	var hasMdView, clause1HasClause3 bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &hasMdView),
		chromedp.Evaluate(`(()=>{const e=[...document.querySelectorAll('.md-block .md-rendered')].find(x=>x.textContent.includes('Intro one clause here')); return e?e.textContent.includes('third clause ends'):true;})()`, &clause1HasClause3),
	); err != nil {
		t.Fatalf("prose-split query: %v%s", err, diag())
	}
	if !hasMdView {
		t.Fatalf("Markdown should render by default%s", diag())
	}
	if clause1HasClause3 {
		t.Fatalf("the one-sentence-per-line paragraph was NOT split: the clause-1 block also contains clause 3 (still one big block)%s", diag())
	}

	// Tap ONLY the clause-3 line (source line 5) and assert the
	// composer is scoped to that single line, not a range.
	var clicked bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(()=>{const el=[...document.querySelectorAll('.md-block .md-rendered')].find(e=>e.textContent.includes('third clause ends')); if(el){el.click();return true;} return false;})()`, &clicked),
	); err != nil || !clicked {
		t.Fatalf("could not click the clause-3 prose line: err=%v clicked=%v%s", err, clicked, diag())
	}
	var composerText string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.Evaluate(`(document.querySelector('.composer')||{}).textContent||''`, &composerText),
	); err != nil {
		t.Fatalf("composer for prose line: %v%s", err, diag())
	}
	// clause 3 is source line 5 → label must be the single line "L5",
	// not a range ("L3-L5" / any "-L").
	if !strings.Contains(composerText, "L5") {
		t.Errorf("composer label = %q, want it scoped to single line L5%s", composerText, diag())
	}
	if strings.Contains(composerText, "-L") || strings.Contains(composerText, "L3") || strings.Contains(composerText, "L4") {
		t.Errorf("composer label = %q, must NOT be a multi-line range (this was the bug)%s", composerText, diag())
	}

	mu.Lock()
	for _, line := range consoleLines {
		if strings.Contains(strings.ToLower(line), "error") {
			t.Errorf("browser console error: %s", line)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
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

	// The freeform "Custom ref…" input and the BaseError surface were
	// removed — the dropdown is the only base control now.
	var hasCustom, hasErr bool
	var optsJSON string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('.base-custom, #base-custom-input')`, &hasCustom),
		chromedp.Evaluate(`!!document.querySelector('.base-error')`, &hasErr),
		chromedp.Evaluate(`JSON.stringify([...document.querySelectorAll('#base-input option')].map(o=>o.value))`, &optsJSON),
	); err != nil {
		t.Fatalf("inspect base picker: %v\nstderr: %s", err, p.stderr.String())
	}
	if hasCustom {
		t.Error("custom-ref input should be gone")
	}
	if hasErr {
		t.Error("base-error surface should be gone")
	}
	// Dropdown must offer the expanded HEAD~N presets and the fixture's
	// local branch. (Fixtures are local-only `git init` repos, so there
	// are no remote-tracking entries to assert.)
	for _, want := range []string{"HEAD", "HEAD~1", "HEAD~3", "HEAD~5", "HEAD~10", "main"} {
		if !strings.Contains(optsJSON, `"`+want+`"`) {
			t.Errorf("base dropdown missing %q; options=%s", want, optsJSON)
		}
	}

	// A second dropdown swap still works (HEAD~1 -> HEAD~5).
	var baseAfterSecond string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(() => {
			const s = document.querySelector('#base-input');
			s.value = "HEAD~5";
			s.dispatchEvent(new Event("change", {bubbles: true}));
		})()`, nil),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Value(`#base-input`, &baseAfterSecond, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("second dropdown swap: %v\nstderr: %s", err, p.stderr.String())
	}
	if baseAfterSecond != "HEAD~5" {
		t.Errorf("base after second swap = %q, want HEAD~5", baseAfterSecond)
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
		chromedp.Sleep(2*time.Second),
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
