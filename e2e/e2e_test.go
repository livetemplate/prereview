//go:build browser

// End-to-end test for prereview. Run with: go test -tags=browser ./...
//
// Requires a chromium/chrome binary on PATH (or /run/current-system/sw/bin/chromium).
// Boots a fixture git repo, launches the prereview binary, navigates Chrome
// to the printed URL, and asserts the diff renders correctly. Captures
// browser console logs and the server's stderr so failures can be diagnosed
// without re-running the test manually.

package e2e

import (
	"bufio"
	"bytes"
	"context"
	stdcsv "encoding/csv"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	cdpinput "github.com/chromedp/cdproto/input"
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

// prefsIsolatedEnv returns the child env with PREREVIEW_UI_PREFS_PATH pointed at
// a per-repo file OUTSIDE the review dir. This keeps e2e hermetic: the durable
// per-user view prefs (theme/mode/focus/show-resolved) never touch the real
// ~/.config (no pollution) and never leak between tests (each test has its own
// repo temp dir). Deriving the path from `repo` — not t.TempDir() — is deliberate:
// two launches against the SAME repo (the relaunch test) share one prefs file,
// which is exactly the cross-relaunch behaviour under test.
func prefsIsolatedEnv(repo string) []string {
	prefs := filepath.Join(filepath.Dir(repo), "prereview-ui-prefs.json")
	return append(os.Environ(), "PREREVIEW_UI_PREFS_PATH="+prefs)
}

// startPrereview launches the binary against repo and returns the READY URL,
// the running cmd, and a captured stderr buffer. Caller must kill the cmd.
// Pass extraArgs (e.g. --agent) to enable agent mode for tests asserting
// agent-mode behavior.
func startPrereview(t *testing.T, binary, repo string, extraArgs ...string) (string, *exec.Cmd, *bytesBuf) {
	t.Helper()
	// --host 127.0.0.1 is explicit ON PURPOSE — do NOT delete as a
	// "redundant default". It forces netaddr.ResolveBindHost's operator-override
	// path so e2e stays hermetic: without it, on an SSH+tailnet machine
	// (this dev box, or CI reached over SSH) the binary auto-rebinds to
	// the Tailscale IP and every test starts depending on tailscaled
	// being up. The flag default being 127.0.0.1 is irrelevant here —
	// what matters is that the flag is *explicitly set*.
	// The review path is a positional arg now, so it must come AFTER every
	// flag (Go's flag package stops parsing at the first non-flag). extraArgs
	// are flags (e.g. --agent), so append the path last.
	args := append([]string{"--base", "HEAD", "--port", "0", "--host", "127.0.0.1"}, extraArgs...)
	args = append(args, repo)
	cmd := exec.Command(binary, args...)
	cmd.Env = prefsIsolatedEnv(repo)
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
	build := exec.Command("go", "build", "-o", binary, "..")
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
	binary string // path to the built prereview binary (for `prereview processed …`)
	cmd    *exec.Cmd
	stderr *bytesBuf
	ctx    context.Context
	cancel context.CancelFunc
}

// bootChromeAgainstPrereview compiles the binary, sets up a fixture repo,
// launches prereview, and opens a chromedp session at the given viewport.
// Above 900px the template renders desktop mode (sidebar always visible);
// at/below 900px it renders mobile mode (drawer overlay).
// Pass extraArgs (e.g. --agent) to enable agent mode for tests asserting
// agent-mode behavior.
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
	build := exec.Command("go", "build", "-o", binary, "..")
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
		t: t, url: url, repo: repo, binary: binary, cmd: srv, stderr: stderr,
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

// clickFile clicks the file button by path. Matches on the button's title
// attribute (always the full path) rather than its text: in the folder-tree
// sidebar a file button's label is just the bare filename, so a full path like
// "docs/index.html" would never match the button text.
func (p *runningPrereview) clickFile(path string) {
	p.t.Helper()
	// JS click (match on the title = full path) rather than chromedp.Click's
	// coordinate dispatch: in the folder tree, chromedp's hit-testing on a file
	// button can hang in headless even though the button is visible — the same
	// stacking-context flakiness the mobile-drawer test works around. A direct
	// .click() fires the same form submit a real click does.
	clickJS := fmt.Sprintf(
		`(()=>{const b=document.querySelector('button[name="selectFile"][title=%q]');if(!b)return false;b.click();return true})()`,
		path)
	var clicked bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(clickJS, &clicked)); err != nil {
		p.t.Fatalf("clickFile %s eval: %v\nstderr: %s", path, err, p.stderr.String())
	}
	if !clicked {
		p.t.Fatalf("clickFile %s: no selectFile button with that title", path)
	}
	// Wait on the file-head's title, which carries the FULL path. The old wait matched a
	// <strong> whose text equalled the path — but the head splits the path into a dir span
	// plus a <strong class="fh-base"> holding only the BASENAME, so any nested path
	// ("docs/logo.png") never matched and the helper blocked forever. Root-level files
	// happened to work because there basename == path, which is why this hid for so long.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(
			fmt.Sprintf(`main.viewer .file-head-name[title=%q]`, path),
			chromedp.ByQuery),
	); err != nil {
		p.t.Fatalf("clickFile %s: %v\nstderr: %s", path, err, p.stderr.String())
	}
}

// openViewItem opens the toolbar "View ▾" overflow dropdown and fires one of
// its panel items by button name. The trigger is clicked for real (so the
// item's reachability through the dropdown is exercised); WaitVisible proves
// the panel opened and holds the item; then a JS .click() fires the form submit
// robustly — the same click also closes the panel (the wrapper's
// lvt-el:toggleClass), which is exactly the production behaviour. The dropdown's
// own open/close/click-away mechanics are covered by TestE2E_ToolbarViewDropdown.
// Desktop-width only (the dropdown lives in .toolbar-inline, hidden <900px where
// the .more-menu takes over).
//
// This used to retry the trigger click to survive livetemplate/client#147 — a morph
// stripping the client-only `.open` class, closing the menu under us at ~1-in-4. That
// is fixed in client v0.18.1 (the client now preserves lvt-el class/attr state across a
// morph), so the single-shot open is reliable again.
func (p *runningPrereview) openViewItem(name string) {
	p.t.Helper()
	itemSel := fmt.Sprintf(`.tb-dropdown-panel button[name=%q]`, name)
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.tb-dropdown-trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tb-dropdown.open `+itemSel, chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector('.tb-dropdown-panel button[name="%s"]').click()`, name), nil),
	); err != nil {
		p.t.Fatalf("openViewItem %s: %v\nstderr: %s", name, err, p.stderr.String())
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
	sel := fmt.Sprintf(`.code .line[data-line="%d"][data-side="%s"]`, displayLine, side)
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

// peekRow clicks the right-margin count badge for the given NEW-side diff line, revealing
// that row's COLLAPSED cards. #165 keeps OPEN cards visible but collapses DECIDED
// suggestions (accepted/rejected) and RESOLVED comments behind the badge; clicking it
// flips the row (`row-toggled`, #174) so the collapsed cards show. We WaitVisible the
// revealed card so callers can interact with it.
//
// The class is SERVER state now, not a client-only toggle — which is what made this helper
// flaky (#173): a re-render landing after the click used to morph the class away and
// re-collapse the card out from under the wait.
func (p *runningPrereview) peekRow(line int) {
	p.t.Helper()
	row := fmt.Sprintf(`.line-row:has(.line[data-line="%d"][data-side="new"])`, line)
	if err := chromedp.Run(p.ctx,
		chromedp.Click(row+` .line-marks`, chromedp.ByQuery),
		chromedp.WaitVisible(row+`.row-toggled .inline-suggestion, `+row+`.row-toggled .inline-comment`, chromedp.ByQuery),
	); err != nil {
		p.t.Fatalf("peekRow %d: %v", line, err)
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
	// --agent so the session runs the full agent-mode UI (this test exercises
	// the add/edit/delete CSV round-trip).
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
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
}

// TestE2E_CmdEnterSavesComment verifies the composer's Cmd/Ctrl+Enter shortcut
// (lvt-key="Mod+Enter" on the form, resolved by the client's modifier matching)
// saves the draft from INSIDE the textarea — without clicking Save. Pins the
// cross-repo client patch end-to-end.
func TestE2E_CmdEnterSavesComment(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	p.clickFile("edited.go")
	p.clickLine(3, 3) // single-line anchor → composer opens

	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "saved via keyboard", chromedp.ByQuery),
		// Ctrl+Enter (Mod = metaKey||ctrlKey) dispatched on the textarea bubbles
		// to the form's lvt-on:keydown="addComment" lvt-key="Mod+Enter".
		chromedp.Evaluate(`(() => {
			const ta = document.querySelector('.composer textarea');
			ta.focus();
			ta.dispatchEvent(new KeyboardEvent('keydown', {key:'Enter', ctrlKey:true, bubbles:true, cancelable:true}));
			return true;
		})()`, nil),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("cmd+enter save: %v\nstderr: %s", err, p.stderr.String())
	}

	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 saved row, got %d:\n%v", len(rows), rows)
	}
	if body := rows[1][5]; !strings.Contains(body, "saved via keyboard") {
		t.Errorf("saved comment body = %q, missing the typed text", body)
	}
}

// TestE2E_Footer verifies the page footer shows the product name (plus the
// version on real releases, never the "(dev)" suffix on dev builds) and a
// "built with livetemplate" link to the livetemplate GitHub repo.
func TestE2E_Footer(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)

	var mu sync.Mutex
	var consoleLines []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			mu.Lock()
			consoleLines = append(consoleLines, string(e.Type))
			mu.Unlock()
		}
	})
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`footer.app-footer`, &html, chromedp.ByQuery))
		return fmt.Sprintf("\n--- server ---\n%s\n--- footer html ---\n%s", p.stderr.String(), html)
	}

	p.waitReady()

	var hasFooter bool
	var footerText, linkHref, linkTarget, linkRel string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`footer.app-footer`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('footer.app-footer')`, &hasFooter),
		chromedp.Evaluate(`(document.querySelector('footer.app-footer')||{}).textContent||''`, &footerText),
		chromedp.Evaluate(`(document.querySelector('footer.app-footer a')||{}).href||''`, &linkHref),
		chromedp.Evaluate(`(document.querySelector('footer.app-footer a')||{}).target||''`, &linkTarget),
		chromedp.Evaluate(`(document.querySelector('footer.app-footer a')||{}).rel||''`, &linkRel),
	); err != nil {
		t.Fatalf("footer query: %v%s", err, diag())
	}
	if !hasFooter {
		t.Fatalf("footer.app-footer should be present%s", diag())
	}
	// e2e builds with plain `go build` (no -ldflags), so version == "dev" —
	// which the footer renders as a bare "prereview" (the noisy "(dev)" suffix
	// was dropped in the polish pass; only real releases show "prereview vX.Y.Z").
	if !strings.Contains(footerText, "prereview") {
		t.Errorf("footer text = %q, want it to start with the product name (prereview)%s", footerText, diag())
	}
	if strings.Contains(footerText, "(dev)") {
		t.Errorf("footer text = %q, should no longer show the '(dev)' suffix%s", footerText, diag())
	}
	if !strings.Contains(footerText, "built with livetemplate") {
		t.Errorf("footer text = %q, want 'built with livetemplate'%s", footerText, diag())
	}
	if linkHref != "https://github.com/livetemplate/livetemplate" {
		t.Errorf("livetemplate link href = %q, want the livetemplate GitHub URL%s", linkHref, diag())
	}
	if linkTarget != "_blank" || !strings.Contains(linkRel, "noopener") {
		t.Errorf("link should open in a new tab safely; target=%q rel=%q%s", linkTarget, linkRel, diag())
	}

	// Layout: the footer must be a SIBLING of .layout (full-width strip
	// at the page bottom), NOT a descendant of it. On desktop .layout is
	// flex-direction:row, so a footer inside it renders as a narrow third
	// column to the right of the viewer — the reported bug.
	var footerInLayout, fullWidth, atBottom bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.layout').contains(document.querySelector('footer.app-footer'))`, &footerInLayout),
		chromedp.Evaluate(`(()=>{const f=document.querySelector('footer.app-footer').getBoundingClientRect();return f.width>=window.innerWidth-2;})()`, &fullWidth),
		chromedp.Evaluate(`(()=>{const f=document.querySelector('footer.app-footer').getBoundingClientRect(),l=document.querySelector('.layout').getBoundingClientRect();return Math.abs(f.bottom-window.innerHeight)<=2 && f.top>=l.bottom-2;})()`, &atBottom),
	); err != nil {
		t.Fatalf("footer layout query: %v%s", err, diag())
	}
	if footerInLayout {
		t.Errorf("footer must NOT be inside .layout (would render as a desktop right column)%s", diag())
	}
	if !fullWidth {
		t.Errorf("footer must span the full viewport width%s", diag())
	}
	if !atBottom {
		t.Errorf("footer must sit at the page bottom below the .layout row%s", diag())
	}

	mu.Lock()
	for _, l := range consoleLines {
		if strings.Contains(strings.ToLower(l), "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	mu.Unlock()
}

// TestE2E_DesktopReadingSurface pins the desktop reading fixes + the
// two-typeface model: the self-hosted Geist (prose sans) and JetBrains
// Mono (code) webfonts both load (200 + applied), prose computes to Geist
// while inline/diff code stays mono, the rendered-Markdown prose is bumped
// well past the cramped 14px mobile base, and the reading column uses the
// available width centered (no longer a narrow left-hugging 60rem).
func TestE2E_DesktopReadingSurface(t *testing.T) {
	// Wide viewport (1920): the prose reading cap (56rem ≈ 896px) only
	// produces visible breathing gutters when the viewer is wider than
	// the cap. At 1200px the viewer (~920px, no TOC here) is barely wider
	// than the cap, so "is it capped/centered" is only meaningfully
	// testable on a wide screen — which is also where issue #27's
	// too-narrow complaint lived.
	p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdown(t), 1920, 1000)

	var mu sync.Mutex
	var consoleLines, wsFrames, netResponses []string
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
		case *cdpnetwork.EventResponseReceived:
			netResponses = append(netResponses, fmt.Sprintf("%d %s", e.Response.Status, e.Response.URL))
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
		return fmt.Sprintf("\n--- server stderr ---\n%s\n--- console ---\n%s\n--- net ---\n%s\n--- ws frames ---\n%s\n--- rendered html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(netResponses, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.waitReadyAt(1920, 1000)

	// Two-typeface model: rendered Markdown PROSE is Geist (sans); code
	// stays JetBrains Mono (asserted on the code surfaces below). Check the
	// sans face actually loaded (woff2 fetched + decoded), is the computed
	// family on the prose, and the desktop prose size is comfortable.
	var geistLoaded bool
	var renderedFamily string
	var proseFontPx float64
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-rendered`, chromedp.ByQuery),
		chromedp.Evaluate(`(async()=>{await document.fonts.ready;return document.fonts.check('1em "Geist"');})()`, &geistLoaded,
			func(ep *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams { return ep.WithAwaitPromise(true) }),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.md-rendered')).fontFamily`, &renderedFamily),
		chromedp.Evaluate(`parseFloat(getComputedStyle(document.querySelector('.md-rendered')).fontSize)`, &proseFontPx),
	); err != nil {
		t.Fatalf("font/prose query: %v%s", err, diag())
	}
	if !geistLoaded {
		t.Errorf("document.fonts.check failed — Geist (prose sans) did not load%s", diag())
	}
	if !strings.Contains(renderedFamily, "Geist") {
		t.Errorf("rendered Markdown computed font-family = %q, want it to start with \"Geist\" (prose is sans)%s", renderedFamily, diag())
	}
	// Desktop prose tracks the 16px desktop html base (1rem). An earlier
	// 1.25rem (20px) bump read as too large on wide monitors — users
	// dropped browser zoom to 80% (20px → 16px) for comfortable reading.
	// Guard both ends: not the oversized bump, not an accidental shrink.
	if proseFontPx < 15 || proseFontPx > 17 {
		t.Errorf("desktop prose font-size = %.1fpx, want ≈16 (the 1rem desktop base; 20px read as too large)%s", proseFontPx, diag())
	}

	// Both faces are served by our own routes with a 200 (self-hosted, no
	// CDN): Geist for the prose, JetBrains Mono for the inline code spans.
	mu.Lock()
	var geistOK, jbmOK bool
	for _, r := range netResponses {
		if strings.HasPrefix(r, "200 ") && strings.Contains(r, "/fonts/geist-") {
			geistOK = true
		}
		if strings.HasPrefix(r, "200 ") && strings.Contains(r, "/fonts/jetbrains-mono-") {
			jbmOK = true
		}
	}
	mu.Unlock()
	if !geistOK {
		t.Errorf("expected a 200 for a /fonts/geist-*.woff2 route (prose sans)%s", diag())
	}
	if !jbmOK {
		t.Errorf("expected a 200 for a /fonts/jetbrains-mono-*.woff2 route (code mono)%s", diag())
	}

	// Reading column (issue #27 "narrower body for breathing space"):
	// the prose is capped to a readable measure (80ch) and CENTERED, so
	// on a wide viewer there are symmetric breathing gutters rather than
	// edge-to-edge text. The anti-regression that still matters is
	// SYMMETRY — the original bug was a narrow column hugging one side
	// (60rem pinned left with a huge empty right region). So we assert:
	//   • not the old 60rem cap;
	//   • symmetric side gaps (centered, not hugging);
	//   • the column is genuinely narrower than the viewer (gutters
	//     exist) yet still a substantial reading width (not the old
	//     too-narrow bug).
	var maxWidth string
	var symmetric, capped, wideEnough bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.md-view')).maxWidth`, &maxWidth),
		chromedp.Evaluate(`(()=>{const m=document.querySelector('.md-view').getBoundingClientRect(),v=document.querySelector('main.viewer').getBoundingClientRect();return Math.abs((m.left-v.left)-(v.right-m.right))<=24;})()`, &symmetric),
		chromedp.Evaluate(`(()=>{const m=document.querySelector('.md-view').getBoundingClientRect(),v=document.querySelector('main.viewer').getBoundingClientRect();return m.width < v.width - 80;})()`, &capped),
		chromedp.Evaluate(`(()=>{const m=document.querySelector('.md-view').getBoundingClientRect();return m.width >= 560;})()`, &wideEnough),
	); err != nil {
		t.Fatalf("md-view geometry query: %v%s", err, diag())
	}
	if maxWidth == "60rem" {
		t.Errorf("md-view max-width is still the old 60rem%s", diag())
	}
	if !symmetric {
		t.Errorf("md-view is not horizontally balanced — it hugs one side (the reported bug)%s", diag())
	}
	if !capped {
		t.Errorf("md-view fills the viewer edge-to-edge — expected a capped, centered reading column with breathing gutters (issue #27)%s", diag())
	}
	if !wideEnough {
		t.Errorf("md-view reading column is too narrow (<560px) — not the intended comfortable measure%s", diag())
	}

	// Two-typeface split: an inline `code` span inside the (sans) prose
	// stays JetBrains Mono — Pico's --pico-font-family-monospace governs
	// code/kbd/samp/pre regardless of the sans body family. This is the
	// anti-regression that proves prose and code diverged correctly. Also
	// the toolbar font is bumped on desktop.
	var mdCodeFam string
	var btnFontPx float64
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.md-rendered code')).fontFamily`, &mdCodeFam),
		chromedp.Evaluate(`parseFloat(getComputedStyle(document.querySelector('header.bar button')).fontSize)`, &btnFontPx),
	); err != nil {
		t.Fatalf("font-split query: %v%s", err, diag())
	}
	if !strings.Contains(mdCodeFam, "JetBrains Mono") {
		t.Errorf("inline Markdown code font = %q, want JetBrains Mono (code stays mono while prose is sans)%s", mdCodeFam, diag())
	}
	if btnFontPx < 15 {
		t.Errorf("toolbar button font-size = %.1fpx, want >=15 (desktop bump)%s", btnFontPx, diag())
	}

	var codeFam string
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`form[aria-label="Markdown view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
		chromedp.WaitVisible(`.code .line`, chromedp.ByQuery),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.code .content')).fontFamily`, &codeFam),
	); err != nil {
		t.Fatalf("raw-view font query: %v%s", err, diag())
	}
	if !strings.Contains(codeFam, "JetBrains Mono") {
		t.Errorf("raw diff/code view font = %q, want JetBrains Mono (code surfaces stay mono)%s", codeFam, diag())
	}

	mu.Lock()
	for _, line := range consoleLines {
		if strings.Contains(strings.ToLower(line), "error") {
			t.Errorf("browser console error: %s", line)
		}
	}
	t.Logf("captured %d console, %d ws, %d net", len(consoleLines), len(wsFrames), len(netResponses))
	mu.Unlock()
}

// TestE2E_QuitShutsServer verifies that in standalone mode the top-bar
// button is "Quit" and clicking it gracefully shuts the server down —
// subsequent HTTP requests fail.
func TestE2E_QuitShutsServer(t *testing.T) {
	// Default boot — no --agent.
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	// Quit now routes through a confirm dialog (#58): the toolbar button is the
	// trigger (still labelled "Quit"); the dialog's own button does the shutdown.
	var btnText string
	if err := chromedp.Run(p.ctx,
		chromedp.Text(`header.bar button[commandfor='confirm-quit']`, &btnText, chromedp.ByQuery),
		chromedp.Click(`header.bar button[commandfor='confirm-quit']`, chromedp.ByQuery),
		chromedp.WaitVisible(`#confirm-quit[open] button[name='quit']`, chromedp.ByQuery),
		chromedp.Click(`#confirm-quit button[name='quit']`, chromedp.ByQuery),
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
}

// TestE2E_ProgressBarOnAction verifies the loading bar appears during a
// file-switch action (~200ms+ in flight) and disappears after the render
// completes. The bar is the framework's LoadingIndicator, opted-in via
// `data-lvt-loading-debounce-ms="200"` on <body>. Polls via
// MutationObserver to catch the element even when its lifetime is short.
func TestE2E_ProgressBarOnAction(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	// Click a file and observe whether the loading bar ever appeared
	// during the round-trip. We can't WaitVisible(.lvt-loading-bar)
	// directly because it could appear AND disappear before our query
	// lands; instead, install a MutationObserver before the click.
	var sawBar bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(() => {
			window.__sawProgressBar = false;
			const obs = new MutationObserver((mutations) => {
				for (const m of mutations) {
					for (const n of m.addedNodes) {
						if (n.classList && n.classList.contains('lvt-loading-bar')) {
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
		chromedp.Evaluate(`!!document.querySelector('.lvt-loading-bar')`, &orphan),
	); err != nil {
		t.Fatalf("orphan probe: %v", err)
	}
	if orphan {
		t.Error(".lvt-loading-bar element should be removed once lvt:updated fires")
	}
	t.Logf("progress bar appeared during fresh.go click: %v", sawBar)
}

// TestE2E_CodeScrollResetsOnFileSwitch verifies that .code's scrollLeft is
// reset to 0 when the user switches files. The .code element is intentionally
// not data-keyed (statics-cache reasons documented in prereview.tmpl), so
// morphdom reuses it across renders and its prior scroll position would
// otherwise carry over. The `lvt-fx:scroll="reset-on:data-path"` directive
// (added to .code) clears scrollLeft/scrollTop on data-path changes.
func TestE2E_CodeScrollResetsOnFileSwitch(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()

	// Land on a real file so .code has horizontal overflow we can scroll.
	p.clickFile("fresh.go")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='fresh.go']`, chromedp.BySearch),
	); err != nil {
		t.Fatalf("wait fresh.go viewer: %v\nstderr: %s", err, p.stderr.String())
	}

	// Desktop (≥900px): long code lines SCROLL horizontally — the .content is
	// white-space:pre, NOT the mobile pre-wrap (the @media split that lets this
	// scroll-reset be meaningful while mobile keeps wrapping per the Safari fix).
	var desktopWS string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.code .content')).whiteSpace`, &desktopWS),
	); err != nil {
		t.Fatalf("desktop white-space query: %v", err)
	}
	if desktopWS != "pre" {
		t.Errorf("desktop .code .content white-space = %q, want \"pre\" (long lines scroll, not wrap)", desktopWS)
	}

	// Force-scroll .code horizontally.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(() => { const c = document.querySelector('.code'); if (c) c.scrollLeft = 200; })()`, nil),
	); err != nil {
		t.Fatalf("force scroll: %v", err)
	}

	// Switch to a different file and confirm .code's scrollLeft is back to 0.
	p.clickFile("edited.go")
	var scrollLeftAfter float64
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='edited.go']`, chromedp.BySearch),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.Evaluate(`document.querySelector('.code').scrollLeft`, &scrollLeftAfter),
	); err != nil {
		t.Fatalf("post-switch query: %v\nstderr: %s", err, p.stderr.String())
	}
	if scrollLeftAfter != 0 {
		t.Errorf(".code scrollLeft after file switch = %v, want 0 (scroll-reset directive should have fired)", scrollLeftAfter)
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
	); err != nil {
		t.Fatalf("add comment: %v\nstderr: %s", err, p.stderr.String())
	}
	// Open all-comments via the `a` shortcut (the sidebar button was removed in
	// #111; `a` is the primary access path, robust to which toolbar dropdown holds
	// the item).
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent("a"),
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

	// Two comments, on two DIFFERENT lines, so the all-comments list has two distinguishable
	// items. They used to both go on line 3 — but since #174 a line is one conversation, so
	// clicking an already-commented line opens its thread instead of the composer, and the
	// second add would wait forever for a composer that never comes. Which line each comment
	// sits on is irrelevant here; this test is about the all-comments list's actions.
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "alpha-cmt", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment alpha: %v%s", err, diag())
	}
	p.clickLine(5, 5)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "beta-cmt", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.Sleep(250*time.Millisecond),
	); err != nil {
		t.Fatalf("add comment beta: %v%s", err, diag())
	}

	// Open the all-comments view (via the `a` shortcut — the sidebar button was
	// removed in #111); assert each item carries the three new actions.
	var items, withResolve, withEdit, withDelete int
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent("a"),
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

	// Cancel the edit, go back to the list — via the `a` shortcut, the same way the list is
	// opened above. The old `.drawer-all-comments` button no longer exists (#111 removed the
	// sidebar entry; it lives in the View menu now), so clicking it waited forever on a node
	// that is never rendered.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='clearSelection']`, chromedp.ByQuery),
		chromedp.Sleep(150*time.Millisecond),
		chromedp.KeyEvent("a"),
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

	// Resolve → the card is hidden by default.
	//
	// It stays in the DOM: since #165 a resolved comment COLLAPSES behind the row's green
	// count badge (a CSS rule) rather than being dropped from the render, so that clicking
	// the badge can peek it back. Assert on what the reviewer SEES (offsetParent), not on
	// presence — the old `!!querySelector('.inline-comment')` check has been failing ever
	// since #165 landed, asserting a render contract the product deliberately changed.
	var visibleAfterResolve int
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.inline-comment button[name='toggleResolved']`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
		chromedp.Evaluate(
			`[...document.querySelectorAll('.inline-comment')].filter(e => e.offsetParent !== null).length`,
			&visibleAfterResolve),
	); err != nil {
		t.Fatalf("resolve: %v\nstderr: %s", err, p.stderr.String())
	}
	if visibleAfterResolve != 0 {
		t.Errorf("a resolved comment should collapse out of sight by default; %d still visible",
			visibleAfterResolve)
	}

	// CSV row should have resolved=true (col 7).
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected 1 row + header, got %d: %v", len(rows), rows)
	}
	if rows[1][7] != "true" {
		t.Errorf("resolved column = %q, want 'true'", rows[1][7])
	}

	// Toggle "Show resolved" (now in the View dropdown) → the resolved comment
	// reappears with is-resolved.
	p.openViewItem("toggleShowResolved")
	if err := chromedp.Run(p.ctx,
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
		.filter(r => r.querySelector('.line.del') && getComputedStyle(r).display !== 'none').length`

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
		chromedp.Click(`form[aria-label="File view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
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
		chromedp.Click(`form[aria-label="File view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
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

	lineBtns := `document.querySelectorAll('.code .line').length`
	foldRows := `document.querySelectorAll('.code .fold-row').length`
	delRows := `document.querySelectorAll('.code .line.del').length`

	// Diff view (default): folded — only a few real lines, >=1 fold.
	var diffBtns, diffFolds int
	var foldText string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.code .line`, chromedp.ByQuery),
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
		chromedp.Click(`form[aria-label="File view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
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
		chromedp.Click(`form[aria-label="File view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
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

// setupFixtureRepoReanchor commits a seed then writes the working-tree
// doc the user "reviews" (one sentence per line so each is its own
// block). The TARGET sentence is line 5.
func setupFixtureRepoReanchor(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "docs.md", "# Reanchor Fixture\n\nseed.\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	// Working-tree v1 (what the reviewer sees). Lines: 1 h1, 3 ctx, 4
	// ctx, 5 TARGET, 6 trailing.
	mustWrite(t, dir, "docs.md", "# Reanchor Fixture\n\nFirst context sentence stays put.\nSecond context sentence also stable.\nTARGET sentence to anchor a comment on.\nTrailing sentence after the target.\n")
	return dir
}

// clickMdBlock clicks the rendered-Markdown block whose text contains
// phrase. Returns false if no such block.
func clickMdBlock(p *runningPrereview, phrase string) bool {
	var ok bool
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(
		`(()=>{const e=[...document.querySelectorAll('.md-block .md-rendered')].find(x=>x.textContent.includes(`+jsStr(phrase)+`)); if(e){e.click();return true;} return false;})()`, &ok))
	return ok
}

func jsStr(s string) string { return "\"" + strings.ReplaceAll(s, `"`, `\"`) + "\"" }

// TestE2E_CommentReanchor_FollowsContent: comment on a sentence, then
// the doc is rewritten inserting lines ABOVE it (simulating the Claude
// edit loop). On reload the comment must auto-follow its content and
// the CSV must self-heal with anchor_status=moved.
func TestE2E_CommentReanchor_FollowsContent(t *testing.T) {
	dir := setupFixtureRepoReanchor(t)
	p := bootChromeAgainstRepo(t, dir, 1200, 800)

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
		t.Fatalf("enable network: %v", err)
	}
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`main`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.waitReady()
	p.clickFile("docs.md")
	if !clickMdBlock(p, "TARGET sentence to anchor a comment on.") {
		t.Fatalf("could not find the TARGET block%s", diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "fix this sentence", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment on TARGET: %v%s", err, diag())
	}
	rows := p.readCSV()
	if len(rows) != 2 || rows[1][2] != "5" || rows[1][3] != "5" {
		t.Fatalf("initial CSV want from/to=5/5, got %v%s", rows, diag())
	}
	if rows[1][9] != "ok" {
		t.Errorf("fresh comment anchor_status = %q, want ok", rows[1][9])
	}

	// The doc is rewritten: two sentences inserted ABOVE the TARGET.
	if err := os.WriteFile(filepath.Join(dir, "docs.md"),
		[]byte("# Reanchor Fixture\n\nFirst context sentence stays put.\nInserted sentence one.\nInserted sentence two.\nSecond context sentence also stable.\nTARGET sentence to anchor a comment on.\nTrailing sentence after the target.\n"),
		0o644); err != nil {
		t.Fatalf("rewrite doc: %v", err)
	}

	// Reload → Mount re-anchors the selected file and self-heals the CSV.
	p.waitReady()
	p.clickFile("docs.md")
	if err := chromedp.Run(p.ctx, chromedp.Sleep(700*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	rows = p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("after reload want 1 row, got %v%s", rows, diag())
	}
	if rows[1][2] != "7" || rows[1][3] != "7" {
		t.Errorf("comment did not follow content: from/to = %s/%s, want 7/7%s", rows[1][2], rows[1][3], diag())
	}
	if rows[1][9] != "moved" {
		t.Errorf("anchor_status = %q, want moved%s", rows[1][9], diag())
	}
	if !strings.Contains(rows[1][5], "fix this sentence") {
		t.Errorf("body lost: %q", rows[1][5])
	}
	mu.Lock()
	for _, l := range consoleLines {
		if strings.Contains(strings.ToLower(l), "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	mu.Unlock()
}

// TestE2E_CommentReanchor_FlagsAndReattaches: comment on a sentence,
// then that sentence's text is rewritten (content gone). On reload the
// comment must be flagged outdated (ints untouched, original snippet
// shown), and the Re-anchor flow must re-point it and clear the flag.
func TestE2E_CommentReanchor_FlagsAndReattaches(t *testing.T) {
	dir := setupFixtureRepoReanchor(t)
	p := bootChromeAgainstRepo(t, dir, 1200, 800)

	var mu sync.Mutex
	var consoleLines []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*cdpruntime.EventConsoleAPICalled); ok {
			mu.Lock()
			consoleLines = append(consoleLines, string(e.Type))
			mu.Unlock()
		}
	})
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`main`, &html, chromedp.ByQuery))
		return fmt.Sprintf("\n--- server ---\n%s\n--- html ---\n%s", p.stderr.String(), html)
	}

	p.waitReady()
	p.clickFile("docs.md")
	if !clickMdBlock(p, "TARGET sentence to anchor a comment on.") {
		t.Fatalf("could not find TARGET block%s", diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "orphan me", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v%s", err, diag())
	}

	// Rewrite the TARGET sentence's text (content gone, same line count).
	if err := os.WriteFile(filepath.Join(dir, "docs.md"),
		[]byte("# Reanchor Fixture\n\nFirst context sentence stays put.\nSecond context sentence also stable.\nCompletely different replacement line.\nTrailing sentence after the target.\n"),
		0o644); err != nil {
		t.Fatalf("rewrite doc: %v", err)
	}

	p.waitReady()
	p.clickFile("docs.md")
	var hasOutdatedBadge, snippetShown bool
	if err := chromedp.Run(p.ctx,
		chromedp.Sleep(700*time.Millisecond),
		chromedp.Evaluate(`!!document.querySelector('.inline-comment.is-outdated .outdated-badge')`, &hasOutdatedBadge),
		chromedp.Evaluate(`(()=>{const p=document.querySelector('.anchor-orig pre'); return !!p && p.textContent.includes('TARGET sentence to anchor a comment on.');})()`, &snippetShown),
	); err != nil {
		t.Fatalf("post-rewrite query: %v%s", err, diag())
	}
	rows := p.readCSV()
	if len(rows) != 2 || rows[1][2] != "5" || rows[1][3] != "5" {
		t.Fatalf("outdated comment ints must stay 5/5, got %v%s", rows, diag())
	}
	if rows[1][9] != "outdated" {
		t.Errorf("anchor_status = %q, want outdated%s", rows[1][9], diag())
	}
	if !hasOutdatedBadge {
		t.Errorf("expected an outdated badge on the comment%s", diag())
	}
	if !snippetShown {
		t.Errorf("expected the original snippet shown%s", diag())
	}

	// Re-anchor: click Re-anchor, then pick a new line, then Save.
	var reanchorClicked bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(()=>{const b=document.querySelector('.inline-comment.is-outdated button[name="reanchorComment"]'); if(b){b.click();return true;} return false;})()`, &reanchorClicked),
	); err != nil || !reanchorClicked {
		t.Fatalf("click Re-anchor: err=%v clicked=%v%s", err, reanchorClicked, diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.reanchor-prompt`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("reanchor prompt should appear%s", diag())
	}
	if !clickMdBlock(p, "Trailing sentence after the target.") {
		t.Fatalf("could not find the re-anchor target block%s", diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`//div[contains(@class,'composer')]//strong[starts-with(normalize-space(text()), 'Re-anchoring comment to')]`, chromedp.BySearch),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
	); err != nil {
		t.Fatalf("save re-anchor: %v%s", err, diag())
	}
	rows = p.readCSV()
	if len(rows) != 2 || rows[1][2] != "6" || rows[1][3] != "6" {
		t.Errorf("re-anchored comment want from/to=6/6, got %v%s", rows, diag())
	}
	if rows[1][9] != "ok" {
		t.Errorf("anchor_status after re-anchor = %q, want ok%s", rows[1][9], diag())
	}
	if !strings.Contains(rows[1][5], "orphan me") {
		t.Errorf("body lost on re-anchor: %q", rows[1][5])
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
	const base = "# Doc Title\n\nIntro clause one stands alone.\nSecond clause continues here.\nThird clause ends the paragraph.\n\n- alpha\n- beta\n\n```go\nx := 1\n```\n\n| Use | Detail |\n|-----|--------|\n| C | chat |\n| D | authrow |\n"
	mustWrite(t, dir, "docs.md", base)
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed docs")

	// Edit a prose line (4) AND the row-D line (17) so docs.md is a
	// changed file and both fall inside raw-view diff hunks (so the
	// per-line comments round-trip visibly to the line viewer too).
	mustWrite(t, dir, "docs.md", "# Doc Title\n\nIntro clause one stands alone.\nSecond clause EDITED here.\nThird clause ends the paragraph.\n\n- alpha\n- beta\n\n```go\nx := 1\n```\n\n| Use | Detail |\n|-----|--------|\n| C | chat |\n| D | authrow EDITED |\n")
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
	var hasMdView, hasH1, hasMdRadios, hasFileRadios bool
	var scriptCount, lineBtns int
	var h1Text, checkedView string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &hasMdView),
		chromedp.Evaluate(`!!document.querySelector('.md-rendered h1')`, &hasH1),
		chromedp.Evaluate(`(document.querySelector('.md-rendered h1')||{}).textContent||''`, &h1Text),
		chromedp.Evaluate(`document.querySelectorAll('.md-rendered script').length`, &scriptCount),
		chromedp.Evaluate(`document.querySelectorAll('.code .line').length`, &lineBtns),
		chromedp.Evaluate(`!!document.querySelector('form[aria-label="Markdown view"]')`, &hasMdRadios),
		chromedp.Evaluate(`(document.querySelector('form[aria-label="Markdown view"] input:checked')||{}).value||''`, &checkedView),
		chromedp.Evaluate(`!!document.querySelector('form[aria-label="File view"]')`, &hasFileRadios),
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
	if !hasMdRadios || checkedView != "rendered" {
		t.Errorf("expected Markdown view radios with rendered checked; radios present=%v checked value=%q", hasMdRadios, checkedView)
	}
	// The Diff/File radios STAY visible in rendered Markdown: since #110 that choice gates the
	// rendered change-bars, exactly as it gates the diff in raw. (They are hidden only in the
	// HTML preview, whose highlight is deferred — see TestE2E_HTMLPreview*.) This assertion
	// used to demand the opposite and has been failing since #110 shipped.
	if !hasFileRadios {
		t.Error("File view radios should stay visible in rendered Markdown — the Diff/File " +
			"choice gates the rendered change-bars there (#110)")
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
		chromedp.Click(`form[aria-label="Markdown view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
		chromedp.Sleep(350*time.Millisecond),
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &rawHasMdView),
		chromedp.Evaluate(`document.querySelectorAll('.code .line').length`, &rawLineBtns),
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
		chromedp.Click(`form[aria-label="Markdown view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
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
		chromedp.Click(`form[aria-label="Markdown view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
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
		chromedp.Click(`form[aria-label="Markdown view"] input[type="radio"]:not(:checked)`, chromedp.ByQuery),
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
		chromedp.Evaluate(`(()=>{const e=[...document.querySelectorAll('.md-block .md-rendered')].find(x=>x.textContent.includes('Intro clause one')); return e?e.textContent.includes('Third clause ends'):true;})()`, &clause1HasClause3),
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
		chromedp.Evaluate(`(()=>{const el=[...document.querySelectorAll('.md-block .md-rendered')].find(e=>e.textContent.includes('Third clause ends')); if(el){el.click();return true;} return false;})()`, &clicked),
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

// setupFixtureRepoHardWrap commits a seed then writes a hard-wrapped
// (~80-col, lines break mid-sentence) Markdown doc as the single
// changed file — the exact shape the user reported on the
// broadcast-action proposal. Lines: 1 h1, 3-5 one paragraph wrapped
// mid-sentence (line 3 ends "naming," with no terminal punctuation).
func setupFixtureRepoHardWrap(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "docs.md", "# Hardwrap Doc\n\nseed.\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "docs.md",
		"# Hardwrap Doc\n\n"+
			"**The design:** a single primitive — a named topic, with classic publish/subscribe naming,\n"+
			"where per-identity targeting is a topic derived from the identity. This is the `Phoenix.PubSub`\n"+
			"model. Per-connection state is the default; nothing fans out unless you `Publish`.\n")
	return dir
}

// TestE2E_MarkdownHardWrapReflow pins the fix for the user-reported
// "unnecessary line breaks in the same sentence": a hard-wrapped
// paragraph renders as ONE rendered block (a sentence never breaks
// across visual lines), the inline code span that straddles a wrap
// survives, and the block stays commentable at its full paragraph
// source span. Captures console, server stderr, WS frames and HTML.
func TestE2E_MarkdownHardWrapReflow(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoHardWrap(t), 1200, 800)

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

	// Exactly one rendered block holds the whole hard-wrapped paragraph;
	// the previously mid-sentence-broken boundary is contiguous in it.
	var hasMdView bool
	var paraBlocks int
	var blockText, blockHTML string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &hasMdView),
		chromedp.Evaluate(`[...document.querySelectorAll('.md-block .md-rendered')].filter(e=>e.textContent.includes('a single primitive')).length`, &paraBlocks),
		chromedp.Evaluate(`(()=>{const e=[...document.querySelectorAll('.md-block .md-rendered')].find(x=>x.textContent.includes('a single primitive')); return e?e.textContent:'';})()`, &blockText),
		chromedp.Evaluate(`(()=>{const e=[...document.querySelectorAll('.md-block .md-rendered')].find(x=>x.textContent.includes('a single primitive')); return e?e.innerHTML:'';})()`, &blockHTML),
	); err != nil {
		t.Fatalf("hard-wrap render query: %v%s", err, diag())
	}
	if !hasMdView {
		t.Fatalf("Markdown should render by default%s", diag())
	}
	if paraBlocks != 1 {
		t.Fatalf("hard-wrapped paragraph must be ONE block, got %d (a per-line split would give 3 — the reported bug)%s", paraBlocks, diag())
	}
	if !strings.Contains(blockText, "publish/subscribe naming,") ||
		!strings.Contains(blockText, "where per-identity targeting") ||
		!strings.Contains(blockText, "Per-connection state is the default") {
		t.Fatalf("the sentence must be contiguous in one block; block text = %q%s", blockText, diag())
	}
	if !strings.Contains(blockHTML, "<code>Phoenix.PubSub</code>") {
		t.Errorf("code span straddling the wrap was not preserved; block HTML = %q%s", blockHTML, diag())
	}

	// Still commentable: clicking the reflowed block anchors a comment
	// to the FULL paragraph source span (lines 3-5).
	if !clickMdBlock(p, "a single primitive") {
		t.Fatalf("could not click the reflowed paragraph block%s", diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "tighten this paragraph", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.md-block .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("comment on reflowed block: %v%s", err, diag())
	}
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d: %v%s", len(rows), rows, diag())
	}
	r := rows[1]
	if r[1] != "docs.md" {
		t.Errorf("file = %q, want docs.md", r[1])
	}
	if r[2] != "3" || r[3] != "5" {
		t.Errorf("from/to = %q/%q, want 3/5 (full hard-wrapped paragraph span)%s", r[2], r[3], diag())
	}
	if !strings.Contains(r[5], "tighten this paragraph") {
		t.Errorf("body = %q, missing comment text", r[5])
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
		chromedp.Evaluate(`document.querySelectorAll('.code .line.add').length`, &addClassCount),
		chromedp.Evaluate(`document.querySelectorAll('.code .line.del').length`, &delClassCount),
		chromedp.Evaluate(`document.querySelectorAll('.code .line.ctx').length`, &ctxClassCount),
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

	// Mobile (<900px): long code lines WRAP (the documented Safari focus-scroll
	// fix) — the opposite of the desktop white-space:pre. Pins the @media split
	// both ways so the desktop horizontal-scroll change can't leak to mobile.
	var mobileWS string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.code .content`, chromedp.ByQuery),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.code .content')).whiteSpace`, &mobileWS),
	); err != nil {
		t.Fatalf("mobile white-space query: %v\nstderr: %s", err, p.stderr.String())
	}
	if mobileWS != "pre-wrap" {
		t.Errorf("mobile .code .content white-space = %q, want \"pre-wrap\" (lines wrap on small screens)", mobileWS)
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

// TestE2E_DismissUndo verifies the undo-toast's ✕ dismiss button hides the
// toast WITHOUT restoring the comment — the deletion must stand. This is the
// semantic opposite of Undo: same toast, but the comment stays gone. A test
// that only checked the toast vanished would also pass for the (wrong) restore
// handler, so it asserts the CSV stays header-only afterward.
func TestE2E_DismissUndo(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800)
	p.waitReady()
	p.clickFile("edited.go")
	p.clickLine(3, 3)
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "to be dismissed", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment: %v", err)
	}

	// Delete it (via the dialog) → undo toast appears.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('dialog[id^="confirm-delete-"]').showModal()`, nil),
		chromedp.WaitVisible(`dialog[id^='confirm-delete-'][open] button[name='deleteComment']`, chromedp.ByQuery),
		chromedp.Click(`dialog[id^='confirm-delete-'][open] button[name='deleteComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.undo-toast`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("delete + see toast: %v\nstderr: %s", err, p.stderr.String())
	}

	// Click the ✕ dismiss (NOT Undo) → toast goes away, deletion stands.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.undo-toast button[name='dismissUndo']`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.undo-toast`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("click dismiss: %v\nstderr: %s", err, p.stderr.String())
	}

	// The comment must remain deleted (no inline comment, CSV header-only).
	var commentCount int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('.inline-comment').length`, &commentCount),
	); err != nil {
		t.Fatalf("count inline comments: %v", err)
	}
	if commentCount != 0 {
		t.Errorf("after dismiss: %d inline comments rendered, want 0 (deletion should stand)", commentCount)
	}
	rows := p.readCSV()
	if len(rows) != 1 {
		t.Errorf("after dismiss: CSV has %d rows, want 1 (header only — comment stays deleted)", len(rows))
	}
}

// setupNoGitPlanDir creates a NON-git temp directory holding a
// one-sentence-per-line Markdown plan plus a sibling note — the exact
// shape of the user's core case: reviewing a Claude plan that lives
// outside any git repo. No `git init`; prereview must synthesize the
// file list and an all-added diff from the filesystem alone.
func setupNoGitPlanDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, dir, "plan.md",
		"# Migration Plan\n\n"+
			"The first phase introduces the adapter layer.\n"+
			"The second phase migrates the legacy callers.\n"+
			"The third phase deletes the old shim entirely.\n")
	mustWrite(t, dir, "notes.txt", "loose sibling note\n")
	return dir
}

// readCSVRowsAt reads <csvDir>/.prereview/comments.csv (header included).
// Single-file review puts .prereview/ in the file's PARENT dir, so the
// harness's p.readCSV (which joins p.repo) can't be used there.
func readCSVRowsAt(t *testing.T, csvDir string) [][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(csvDir, ".prereview", "comments.csv"))
	if err != nil {
		t.Fatalf("read csv in %s: %v", csvDir, err)
	}
	rows, err := stdcsv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	return rows
}

// TestE2E_NoGitSingleFile is the core "review a Claude plan" scenario:
// the path arg points at a single Markdown file in a NON-git directory. It
// asserts (1) the file is reviewable with the base picker hidden (no
// refs exist), (2) Markdown renders, (3) a comment persists to
// .prereview/ in the file's PARENT directory, and (4) the git-free
// re-anchor engine still follows the sentence after the file is
// rewritten (an LLM editing the plan before hand-off) — CSV self-heals
// with anchor_status=moved. Captures console + server stderr + WS
// frames + rendered HTML for diagnosis.
func TestE2E_NoGitSingleFile(t *testing.T) {
	dir := setupNoGitPlanDir(t)
	planFile := filepath.Join(dir, "plan.md")
	p := bootChromeAgainstRepo(t, planFile, 1200, 800)

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

	// Base picker must be absent (no git refs), Markdown must render, and
	// the sole file is listed.
	var hasBasePicker, hasMdView bool
	var fileBtns int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('.drawer-base')`, &hasBasePicker),
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &hasMdView),
		chromedp.Evaluate(`document.querySelectorAll('#files-drawer button.file-btn').length`, &fileBtns),
	); err != nil {
		t.Fatalf("initial no-git query: %v%s", err, diag())
	}
	if hasBasePicker {
		t.Errorf("base picker must be hidden in no-git mode%s", diag())
	}
	if !hasMdView {
		t.Errorf("Markdown should render for a loose .md file%s", diag())
	}
	if fileBtns != 1 {
		t.Errorf("file buttons = %d, want 1 (single-file review)%s", fileBtns, diag())
	}

	// Comment on the second sentence (source line 4).
	var clicked bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(()=>{const el=[...document.querySelectorAll('.md-block .md-rendered')].find(e=>e.textContent.includes('second phase migrates')); if(el){el.click();return true;} return false;})()`, &clicked),
	); err != nil || !clicked {
		t.Fatalf("click prose line: err=%v clicked=%v%s", err, clicked, diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "tighten this phase's scope", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
	); err != nil {
		t.Fatalf("add comment: %v%s", err, diag())
	}

	rows := readCSVRowsAt(t, dir) // .prereview is in the file's PARENT dir
	if len(rows) != 2 {
		t.Fatalf("want header + 1 row, got %d: %v%s", len(rows), rows, diag())
	}
	row := rows[1]
	if row[1] != "plan.md" {
		t.Errorf("file col = %q, want plan.md (basename, relative to review root)", row[1])
	}
	if row[2] != "4" || row[3] != "4" {
		t.Errorf("from/to = %q/%q, want 4/4 (the 'second phase' sentence)", row[2], row[3])
	}
	if !strings.Contains(row[5], "tighten this phase") {
		t.Errorf("body = %q, missing comment text", row[5])
	}

	// Killer use case: an LLM rewrites the plan (2 lines inserted above
	// the commented sentence) BEFORE the user hands off. The git-free
	// re-anchor engine must follow the sentence on reload and self-heal
	// the CSV — proving anchoring needs no git.
	if err := os.WriteFile(planFile, []byte(
		"# Migration Plan\n\n"+
			"An extra overview line one.\n"+
			"An extra overview line two.\n"+
			"The first phase introduces the adapter layer.\n"+
			"The second phase migrates the legacy callers.\n"+
			"The third phase deletes the old shim entirely.\n"), 0o644); err != nil {
		t.Fatalf("rewrite plan: %v", err)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Sleep(700*time.Millisecond),
	); err != nil {
		t.Fatalf("reload after rewrite: %v%s", err, diag())
	}
	rows = readCSVRowsAt(t, dir)
	if len(rows) != 2 {
		t.Fatalf("post-rewrite: want header + 1 row, got %d: %v%s", len(rows), rows, diag())
	}
	row = rows[1]
	if row[2] != "6" || row[3] != "6" {
		t.Errorf("post-rewrite from/to = %q/%q, want 6/6 (sentence shifted down 2)%s", row[2], row[3], diag())
	}
	if row[9] != "moved" {
		t.Errorf("post-rewrite anchor_status = %q, want \"moved\" (git-free self-heal)%s", row[9], diag())
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

// TestE2E_NoGitDirWalk asserts that pointing prereview at a NON-git
// directory reviews every file in it (recursively, here flat): both the
// .md and the .txt show up, with the base picker hidden. The single-
// file test above covers the comment + re-anchor flow; this one pins
// the directory-walk producer and the no-git gate.
func TestE2E_NoGitDirWalk(t *testing.T) {
	dir := setupNoGitPlanDir(t)
	p := bootChromeAgainstRepo(t, dir, 1200, 800)
	p.waitReady()

	var hasBasePicker bool
	var names []string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('.drawer-base')`, &hasBasePicker),
		chromedp.Evaluate(`[...document.querySelectorAll('#files-drawer button.file-btn')].map(b=>b.textContent.trim())`, &names),
	); err != nil {
		t.Fatalf("dir-walk query: %v\nstderr: %s", err, p.stderr.String())
	}
	if hasBasePicker {
		t.Errorf("base picker must be hidden for a non-git directory")
	}
	joined := strings.Join(names, " | ")
	if !strings.Contains(joined, "plan.md") || !strings.Contains(joined, "notes.txt") {
		t.Errorf("file buttons = %q, want both plan.md and notes.txt listed", joined)
	}
}

// TestE2E_ThematicBreaksDontStackComposers reproduces the user-reported
// bug from the long-plan dogfood case: a Markdown file with multiple
// `---` separators caused 20+ <hr> blocks to collapse to source line 1
// (goldmark's ThematicBreak carries no Lines() data; pre-fix segmentSpan
// returned (0, 0) → lineAt(0) = 1). The template's md-view renders one
// composer and one inline-comment instance per MarkdownBlock whose range
// contains SelectionEndMax, so a comment on L1 stacked N+1 composers on
// Edit. Pin the fix: exactly one .composer and one .inline-comment
// remain visible after clicking Edit.
func TestE2E_ThematicBreaksDontStackComposers(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "plan.md",
		"# Heading One\n\nparagraph A sentence.\n\n---\n\n## Heading Two\n\nparagraph B sentence.\n\n---\n\n## Heading Three\n\nparagraph C sentence.\n\n---\n\n## Heading Four\n")
	planFile := filepath.Join(dir, "plan.md")
	p := bootChromeAgainstRepo(t, planFile, 1200, 800)
	p.waitReady()

	// Sanity: rendered markdown view is up and the H1 sits at L1.
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.md-view .md-rendered h1`, chromedp.ByQuery)); err != nil {
		t.Fatalf("md-view not visible: %v\nstderr: %s", err, p.stderr.String())
	}

	// Click the H1 block (anchored at L1), enter a comment, save.
	if !clickMdBlock(p, "Heading One") {
		t.Fatalf("could not click the H1 block\nstderr: %s", p.stderr.String())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "headline feedback", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("add comment on L1: %v\nstderr: %s", err, p.stderr.String())
	}

	// After save, exactly one inline-comment should render on L1 (not
	// once per <hr> block that pre-fix shared its range).
	var icCount int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('.inline-comment').length`, &icCount),
	); err != nil {
		t.Fatalf("count inline-comments: %v", err)
	}
	if icCount != 1 {
		t.Errorf("inline-comment count = %d after save, want 1 (one per L1 comment, not one per overlapping block)", icCount)
	}

	// Click Edit → composer reopens in edit mode. There must be exactly
	// one composer; the pre-fix bug rendered one per overlapping block.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='editComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`//div[contains(@class,'composer')]//strong[starts-with(normalize-space(text()), 'Editing comment on')]`, chromedp.BySearch),
	); err != nil {
		t.Fatalf("click Edit: %v\nstderr: %s", err, p.stderr.String())
	}
	var composerCount int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('.composer').length`, &composerCount),
	); err != nil {
		t.Fatalf("count composers: %v", err)
	}
	if composerCount != 1 {
		t.Errorf("composer count = %d in edit mode, want 1 (the user-reported bug)", composerCount)
	}
}

// setupFixtureRepoMarkdownTOC builds a fixture with one .md file that
// has enough h1/h2/h3 headings (≥ 2 after the h1–h3 filter) to trigger
// the TOC sidebar AND a second .md file with only a single heading to
// exercise the "TOC absent" branch in the same boot.
func setupFixtureRepoMarkdownTOC(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	// big.md: a longer doc with h1 + multiple h2s + an h3, with enough
	// paragraph body between sections that scroll-spy has room to fire
	// during a scrollIntoView jump.
	big := "# Big Doc\n\nIntro paragraph one.\n\n## First Section\n\n" +
		strings.Repeat("body line.\n\n", 30) +
		"## Second Section\n\n" +
		strings.Repeat("more body.\n\n", 30) +
		"### Nested\n\n" +
		strings.Repeat("deeper body.\n\n", 20) +
		"## Third Section\n\n" +
		strings.Repeat("final body.\n\n", 20)
	mustWrite(t, dir, "big.md", big)

	// tiny.md: only one heading — TOC must not render.
	mustWrite(t, dir, "tiny.md", "# Lonely\n\nJust one heading here.\n")

	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed toc fixture")

	// Edit big.md so it's a changed file (otherwise it might not show
	// up in the diff view depending on the all-files toggle).
	mustWrite(t, dir, "big.md", big+"\n## Appendix\n\nAdded later.\n")
	mustWrite(t, dir, "tiny.md", "# Lonely\n\nJust one heading here.\nNow edited.\n")
	return dir
}

// TestE2E_TOCSidebarDesktop covers the desktop layout (≥ 900px):
// rendering a Markdown file with ≥ 2 headings shows the right-column
// TOC sidebar; clicking an entry updates the URL hash; the scroll-spy
// adds `lvt-active` to the link whose target is at or above the
// trigger line.
func TestE2E_TOCSidebarDesktop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}
	p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdownTOC(t), 1200, 800)
	p.waitReady()
	p.clickFile("big.md")

	// Sidebar present, populated with h1–h3 entries.
	var sidebarVisible bool
	var linkCount int
	var firstHref, firstText string
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.toc-sidebar`, chromedp.ByQuery),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.toc-sidebar')).display !== 'none'`, &sidebarVisible),
		chromedp.Evaluate(`document.querySelectorAll('.toc-sidebar [lvt-spy-link]').length`, &linkCount),
		chromedp.Evaluate(`(document.querySelector('.toc-sidebar [lvt-spy-link]')||{}).getAttribute('href')||''`, &firstHref),
		chromedp.Evaluate(`(document.querySelector('.toc-sidebar [lvt-spy-link]')||{}).textContent||''`, &firstText),
	); err != nil {
		t.Fatalf("toc sidebar query: %v\nstderr: %s", err, p.stderr.String())
	}
	if !sidebarVisible {
		t.Error("toc sidebar not visible at 1200x800; desktop media query failed")
	}
	// Expected headings: # Big Doc, ## First Section, ## Second Section,
	// ### Nested, ## Third Section, ## Appendix = 6.
	if linkCount < 5 {
		t.Errorf("toc link count = %d, want ≥ 5", linkCount)
	}
	if firstHref != "#big-doc" {
		t.Errorf("first link href = %q, want #big-doc", firstHref)
	}
	if !strings.Contains(firstText, "Big Doc") {
		t.Errorf("first link text = %q, want 'Big Doc'", firstText)
	}

	// On initial paint at scroll-top, the spy applies lvt-active to the
	// LATEST heading whose top is above the trigger line. With a short
	// intro between h1 and the first h2, both sit above the line, so
	// the active link is the first h2 — matching how scroll-spy
	// behaves on a freshly-loaded long doc (the reader is "in" the
	// section that just started, not the document title).
	var initialActive string
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(document.querySelector('.toc-sidebar [lvt-spy-link].lvt-active')||{}).getAttribute('href')||''`, &initialActive),
	); err != nil {
		t.Fatalf("initial active query: %v", err)
	}
	if initialActive == "" {
		t.Error("no lvt-active link after initial paint; spy didn't run its synchronous initial check")
	}
	// At minimum the active link must be one of the headings above the
	// fold (we don't tie ourselves to specific layout pixels here).
	if initialActive != "#big-doc" && initialActive != "#first-section" {
		t.Errorf("initial active = %q, want #big-doc or #first-section", initialActive)
	}

	// Click a deeper link (Second Section). The native anchor scroll
	// should land at that heading; once settled, the spy directive
	// (after one rAF) puts lvt-active on the clicked link. We also
	// expect window.location.hash to update.
	var hashAfterClick string
	var activeAfterClick string
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.toc-sidebar a[href="#second-section"]`, chromedp.ByQuery),
		// Smooth scroll is gated by prefers-reduced-motion in CSS, but in
		// headless chromium reduced-motion is the default — so the jump
		// is instant. Still give a beat for the rAF tick.
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(`window.location.hash`, &hashAfterClick),
		chromedp.Evaluate(`(document.querySelector('.toc-sidebar [lvt-spy-link].lvt-active')||{}).getAttribute('href')||''`, &activeAfterClick),
	); err != nil {
		t.Fatalf("click toc link: %v\nstderr: %s", err, p.stderr.String())
	}
	if hashAfterClick != "#second-section" {
		t.Errorf("location.hash after click = %q, want #second-section", hashAfterClick)
	}
	if activeAfterClick != "#second-section" {
		t.Errorf("active link after click = %q, want #second-section (optimistic activation)", activeAfterClick)
	}

	// Confirm the heading itself has an id attribute (auto-id worked).
	var headingHasID bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.getElementById('second-section')`, &headingHasID),
	); err != nil {
		t.Fatalf("heading id query: %v", err)
	}
	if !headingHasID {
		t.Error("rendered heading missing id='second-section'; goldmark AutoHeadingID not flowing through")
	}
}

// TestE2E_FocusModeDesktop covers the desktop focus-mode reading toggle
// (issue #27): both side columns (file drawer + TOC sidebar) are visible
// by default; clicking the toolbar "Focus" button puts .focus-mode on
// .layout and hides BOTH columns; clicking again restores them. State is
// server-side (ToggleFocusMode) and re-rendered over the WS, so the test
// polls for the morphed DOM rather than expecting a synchronous flip.
func TestE2E_FocusModeDesktop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}
	p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdownTOC(t), 1200, 800)
	p.waitReady()
	p.clickFile("big.md")

	// Baseline: both columns visible, no focus-mode class.
	var drawerVisible, tocVisible, hasFocusClass bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.toc-sidebar`, chromedp.ByQuery),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('#files-drawer')).display !== 'none'`, &drawerVisible),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.toc-sidebar')).display !== 'none'`, &tocVisible),
		chromedp.Evaluate(`!!document.querySelector('.layout.focus-mode')`, &hasFocusClass),
	); err != nil {
		t.Fatalf("baseline query: %v\nstderr: %s", err, p.stderr.String())
	}
	if !drawerVisible {
		t.Error("file drawer should be visible before focus mode")
	}
	if !tocVisible {
		t.Error("toc sidebar should be visible before focus mode")
	}
	if hasFocusClass {
		t.Error("focus-mode class should be absent by default")
	}

	// Enable focus mode (now in the View dropdown) → class present, BOTH columns
	// hidden. The toolbar stays visible in focus mode, so the dropdown is still
	// reachable as the restore affordance.
	p.openViewItem("toggleFocusMode")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.layout.focus-mode`, chromedp.ByQuery),
		chromedp.Poll(
			`getComputedStyle(document.querySelector('#files-drawer')).display === 'none' && getComputedStyle(document.querySelector('.toc-sidebar')).display === 'none'`,
			nil, chromedp.WithPollingTimeout(5*time.Second),
		),
	); err != nil {
		t.Fatalf("enable focus mode: %v\nstderr: %s", err, p.stderr.String())
	}

	// Disable focus mode → class gone, both columns visible again.
	p.openViewItem("toggleFocusMode")
	if err := chromedp.Run(p.ctx,
		chromedp.Poll(
			`!document.querySelector('.layout.focus-mode') && getComputedStyle(document.querySelector('#files-drawer')).display !== 'none' && getComputedStyle(document.querySelector('.toc-sidebar')).display !== 'none'`,
			nil, chromedp.WithPollingTimeout(5*time.Second),
		),
	); err != nil {
		t.Fatalf("disable focus mode: %v\nstderr: %s", err, p.stderr.String())
	}
}

// TestE2E_SidebarCollapseDesktop covers #137: per-side collapse controls that
// live INSIDE each desktop side column (not the top toolbar). Each column
// collapses to a thin rail that keeps its reopen chevron on-screen.
//   - The collapse chevron sits inside the sidebar (the drawer's rail row, the
//     TOC's "On this page" head) — there is no toolbar button and the desktop
//     hamburger is hidden.
//   - Collapsing a side shrinks it to a rail (still present, list/label hidden)
//     rather than removing it; the chevron reopens it.
//   - Collapsing one side never touches the other.
//   - Focus mode still hides BOTH outright; turning it off restores each side to
//     the per-side state it had — a TOC collapsed before Focus stays collapsed.
//
// The toggles are server-side (ToggleFiles / ToggleTOC) re-rendered over the WS,
// so each assertion polls for the morphed DOM rather than a synchronous flip.
func TestE2E_SidebarCollapseDesktop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}
	p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdownTOC(t), 1200, 800)
	p.waitReady()
	p.clickFile("big.md")

	// Baseline: the collapse chevron lives INSIDE each column (WaitVisible
	// proves it), both columns are expanded (list + body showing), there is NO
	// toolbar toggle, and the desktop hamburger is hidden (#137).
	var tocListVisible, drawerBodyVisible, toolbarBtnAbsent, hamburgerHidden bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.toc-sidebar .toc-head .collapse-toggle`, chromedp.ByQuery),
		chromedp.WaitVisible(`#files-drawer .drawer-collapse .collapse-toggle`, chromedp.ByQuery),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.toc-sidebar .toc-list')).display !== 'none'`, &tocListVisible),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('#files-drawer .drawer-body')).display !== 'none'`, &drawerBodyVisible),
		chromedp.Evaluate(`document.querySelector('.toc-toggle') === null`, &toolbarBtnAbsent),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('.hamburger')).display === 'none'`, &hamburgerHidden),
	); err != nil {
		t.Fatalf("baseline query: %v\nstderr: %s", err, p.stderr.String())
	}
	if !tocListVisible || !drawerBodyVisible {
		t.Errorf("both columns should be expanded by default (tocList=%v drawerBody=%v)", tocListVisible, drawerBodyVisible)
	}
	if !toolbarBtnAbsent {
		t.Error("there should be no toolbar collapse button (.toc-toggle) — the controls live in the sidebars")
	}
	if !hamburgerHidden {
		t.Error("the hamburger should be hidden on desktop (the in-drawer rail chevron owns collapse/reopen)")
	}

	// Collapse the TOC via its own chevron. The column becomes a rail: still
	// present, but the list is hidden and the chevron (now a reopen control)
	// stays visible. The drawer is untouched.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.toc-sidebar .collapse-toggle').click()`, nil),
		chromedp.Poll(
			`document.querySelector('.layout.toc-collapsed')`+
				` && getComputedStyle(document.querySelector('.toc-sidebar')).display !== 'none'`+
				` && getComputedStyle(document.querySelector('.toc-sidebar .toc-list')).display === 'none'`+
				` && getComputedStyle(document.querySelector('.toc-sidebar .collapse-toggle')).display !== 'none'`+
				` && getComputedStyle(document.querySelector('#files-drawer .drawer-body')).display !== 'none'`,
			nil, chromedp.WithPollingTimeout(5*time.Second),
		),
	); err != nil {
		t.Fatalf("collapse toc to rail: %v\nstderr: %s", err, p.stderr.String())
	}

	// Reopen the TOC via the rail chevron.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.toc-sidebar .collapse-toggle').click()`, nil),
		chromedp.Poll(
			`!document.querySelector('.layout.toc-collapsed') && getComputedStyle(document.querySelector('.toc-sidebar .toc-list')).display !== 'none'`,
			nil, chromedp.WithPollingTimeout(5*time.Second),
		),
	); err != nil {
		t.Fatalf("reopen toc: %v\nstderr: %s", err, p.stderr.String())
	}

	// Collapse the drawer via its in-drawer chevron. The drawer becomes a rail:
	// still present but shrunk (< 80px) with its body hidden and the chevron
	// still visible. The TOC is untouched.
	var drawerWidth float64
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('#files-drawer .collapse-toggle').click()`, nil),
		chromedp.Poll(
			`document.querySelector('#files-drawer').classList.contains('is-open')`+
				` && getComputedStyle(document.querySelector('#files-drawer')).display !== 'none'`+
				` && getComputedStyle(document.querySelector('#files-drawer .drawer-body')).display === 'none'`+
				` && getComputedStyle(document.querySelector('#files-drawer .collapse-toggle')).display !== 'none'`+
				` && getComputedStyle(document.querySelector('.toc-sidebar .toc-list')).display !== 'none'`,
			nil, chromedp.WithPollingTimeout(5*time.Second),
		),
		chromedp.Evaluate(`document.querySelector('#files-drawer').getBoundingClientRect().width`, &drawerWidth),
	); err != nil {
		t.Fatalf("collapse drawer to rail: %v\nstderr: %s", err, p.stderr.String())
	}
	if drawerWidth >= 80 {
		t.Errorf("collapsed drawer width = %.0fpx, want a thin rail (< 80px)", drawerWidth)
	}

	// Reopen the drawer via the rail chevron.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('#files-drawer .collapse-toggle').click()`, nil),
		chromedp.Poll(
			`getComputedStyle(document.querySelector('#files-drawer .drawer-body')).display !== 'none'`,
			nil, chromedp.WithPollingTimeout(5*time.Second),
		),
	); err != nil {
		t.Fatalf("reopen drawer: %v\nstderr: %s", err, p.stderr.String())
	}

	// Focus-mode composition (no clobber): collapse the TOC, turn Focus ON
	// (hides both columns outright), turn Focus OFF — the TOC must still be a
	// collapsed rail because Focus mode never mutates the per-side state.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.toc-sidebar .collapse-toggle').click()`, nil),
		chromedp.WaitVisible(`.layout.toc-collapsed`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("re-collapse toc before focus: %v\nstderr: %s", err, p.stderr.String())
	}
	p.openViewItem("toggleFocusMode")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.layout.focus-mode`, chromedp.ByQuery),
		chromedp.Poll(
			`getComputedStyle(document.querySelector('#files-drawer')).display === 'none' && getComputedStyle(document.querySelector('.toc-sidebar')).display === 'none'`,
			nil, chromedp.WithPollingTimeout(5*time.Second),
		),
	); err != nil {
		t.Fatalf("focus on over collapsed toc: %v\nstderr: %s", err, p.stderr.String())
	}
	p.openViewItem("toggleFocusMode")
	if err := chromedp.Run(p.ctx,
		chromedp.Poll(
			`!document.querySelector('.layout.focus-mode')`+
				` && document.querySelector('.layout.toc-collapsed')`+
				` && getComputedStyle(document.querySelector('#files-drawer .drawer-body')).display !== 'none'`+
				` && getComputedStyle(document.querySelector('.toc-sidebar .toc-list')).display === 'none'`,
			nil, chromedp.WithPollingTimeout(5*time.Second),
		),
	); err != nil {
		t.Fatalf("focus off restores per-side state (toc stays collapsed): %v\nstderr: %s", err, p.stderr.String())
	}
}

// TestE2E_TOCOverlayMobile covers the mobile flow at 375x812: the
// three-dots menu shows a "Table of contents" entry; tapping it opens
// a full-viewport overlay; tapping an entry in the overlay closes it
// AND updates the URL hash in one gesture.
func TestE2E_TOCOverlayMobile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}
	p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdownTOC(t), 375, 812)

	// Manually navigate at mobile viewport — waitReady would force
	// desktop emulation. The first action depends on the deferred
	// livetemplate-client.js being parsed; sleep 2s per the existing
	// pattern in TestE2E_MobileDrawer.
	if err := chromedp.Run(p.ctx,
		chromedp.EmulateViewport(375, 812),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.hamburger`, chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
	); err != nil {
		t.Fatalf("initial mobile nav: %v\nstderr: %s", err, p.stderr.String())
	}

	// Open file drawer, click big.md.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.hamburger').click()`, nil),
		chromedp.WaitVisible(`#files-drawer.is-open`, chromedp.ByQuery),
		chromedp.Evaluate(`(Array.from(document.querySelectorAll('button[name="selectFile"]')).find(b => b.textContent.includes('big.md'))||{}).click()`, nil),
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open big.md on mobile: %v\nstderr: %s", err, p.stderr.String())
	}

	// Desktop sidebar must be hidden under 900px.
	var desktopSidebarVisible bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(()=>{ const s=document.querySelector('.toc-sidebar'); return !!s && getComputedStyle(s).display !== 'none'; })()`, &desktopSidebarVisible),
	); err != nil {
		t.Fatalf("sidebar visibility query: %v", err)
	}
	if desktopSidebarVisible {
		t.Error("toc-sidebar should NOT be visible at 375x812 (mobile media query failed)")
	}

	// Tap three-dots → menu opens → "Table of contents" item present.
	var tocMenuItemPresent bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('button[name="toggleMoreMenu"]').click()`, nil),
		chromedp.WaitVisible(`.more-menu.is-open`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('button[name="openTOC"]')`, &tocMenuItemPresent),
	); err != nil {
		t.Fatalf("open more menu: %v\nstderr: %s", err, p.stderr.String())
	}
	if !tocMenuItemPresent {
		t.Fatal("Table of contents menu item missing on mobile (RenderedHeadings gate misfired)")
	}

	// Tap "Table of contents" → mobile overlay opens.
	var overlayOpen bool
	var overlayLinkCount int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('button[name="openTOC"]').click()`, nil),
		chromedp.WaitVisible(`.toc-modal.is-open`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.toc-modal').classList.contains('is-open')`, &overlayOpen),
		chromedp.Evaluate(`document.querySelectorAll('.toc-modal [lvt-spy-link]').length`, &overlayLinkCount),
	); err != nil {
		t.Fatalf("open toc overlay: %v\nstderr: %s", err, p.stderr.String())
	}
	if !overlayOpen {
		t.Fatal("toc overlay did not open after tapping menu item")
	}
	if overlayLinkCount < 5 {
		t.Errorf("overlay link count = %d, want ≥ 5", overlayLinkCount)
	}

	// Tap an overlay link → native anchor scroll + closeTOC action,
	// composed via <a href + lvt-on:click="closeTOC". After the action
	// round-trip, the overlay must be closed AND the URL hash set.
	var hashAfter string
	var overlayStillOpen bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.toc-modal a[href="#second-section"]').click()`, nil),
		// Give the WebSocket round-trip a beat to apply state.TOCOpen=false.
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Evaluate(`window.location.hash`, &hashAfter),
		chromedp.Evaluate(`document.querySelector('.toc-modal').classList.contains('is-open')`, &overlayStillOpen),
	); err != nil {
		t.Fatalf("tap overlay link: %v\nstderr: %s", err, p.stderr.String())
	}
	if hashAfter != "#second-section" {
		t.Errorf("location.hash after tap = %q, want #second-section (native scroll missing)", hashAfter)
	}
	if overlayStillOpen {
		t.Error("toc overlay still open after tapping a link (closeTOC action didn't fire or didn't compose with native scroll)")
	}
}

// TestE2E_TOCNavigationFromAllCommentsView pins issue #12: on mobile,
// opening the TOC from inside the all-comments overview and tapping a
// heading must (a) dismiss the all-comments view, (b) close the TOC
// overlay, and (c) actually land the user at that heading's block —
// previously the heading wasn't in the DOM at click time so the native
// anchor scroll missed and the user stayed stuck on the comments view.
// The fix wires the link to NavigateToHeading, which sets
// ScrollToHeadingID so the matching md-block carries lvt-fx:scroll into
// view on re-render.
func TestE2E_TOCNavigationFromAllCommentsView(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}
	p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdownTOC(t), 375, 812)

	// Mobile boot — same pattern as TestE2E_TOCOverlayMobile.
	if err := chromedp.Run(p.ctx,
		chromedp.EmulateViewport(375, 812),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`.hamburger`, chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
	); err != nil {
		t.Fatalf("initial mobile nav: %v\nstderr: %s", err, p.stderr.String())
	}

	// Open file drawer, pick big.md so we land on the markdown view with
	// h1/h2/h3 headings (RenderedHeadings ≥ 2 → TOC available).
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.hamburger').click()`, nil),
		chromedp.WaitVisible(`#files-drawer.is-open`, chromedp.ByQuery),
		chromedp.Evaluate(`(Array.from(document.querySelectorAll('button[name="selectFile"]')).find(b => b.textContent.includes('big.md'))||{}).click()`, nil),
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open big.md: %v\nstderr: %s", err, p.stderr.String())
	}

	// Seed a comment so the all-comments view has something to show
	// (the drawer's "All comments" CTA is gated on len(Comments) > 0).
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.md-block .md-rendered').click()`, nil),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.composer textarea`, "seed for #12", chromedp.ByQuery),
		chromedp.Click(`button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.md-block .inline-comment`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("seed comment: %v\nstderr: %s", err, p.stderr.String())
	}

	// Enter the all-comments view with the `a` shortcut. It used to be entered through the
	// file drawer's ".drawer-all-comments" CTA, but #111 removed that button — the JS
	// querySelector returned null and threw. HOW the view is entered is incidental here;
	// what this test is about is TOC navigation once you are IN it.
	if err := chromedp.Run(p.ctx,
		chromedp.KeyEvent("a"),
		chromedp.WaitVisible(`section.all-comments`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("enter all-comments view: %v\nstderr: %s", err, p.stderr.String())
	}

	// Confirm the md-view is NOT in the DOM right now — this is the
	// exact precondition that makes the bug reproducible (heading
	// targets don't exist for the browser to scroll to).
	var mdViewBeforeNav bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &mdViewBeforeNav),
	); err != nil {
		t.Fatalf("pre-nav md-view query: %v", err)
	}
	if mdViewBeforeNav {
		t.Fatal("md-view present while ShowAllComments=true; fixture/setup wrong")
	}

	// Tap three-dots → "Table of contents" → tap a non-first heading.
	// Target "second-section" so we can distinguish "scrolled to start"
	// (top of doc) from "actually scrolled to target".
	var overlayOpen bool
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('button[name="toggleMoreMenu"]').click()`, nil),
		chromedp.WaitVisible(`.more-menu.is-open`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('button[name="openTOC"]').click()`, nil),
		chromedp.WaitVisible(`.toc-modal.is-open`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.toc-modal').classList.contains('is-open')`, &overlayOpen),
	); err != nil {
		t.Fatalf("open TOC from all-comments: %v\nstderr: %s", err, p.stderr.String())
	}
	if !overlayOpen {
		t.Fatal("TOC overlay didn't open from all-comments view")
	}

	// Click the TOC link for #second-section. This must fire
	// NavigateToHeading on the server, which clears ShowAllComments,
	// closes TOCOpen, and sets ScrollToHeadingID = "second-section".
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.toc-modal a[href="#second-section"]').click()`, nil),
		// Round-trip + morph + scroll. The scroll directive runs
		// post-morph so we need a beat for the full sequence.
		chromedp.Sleep(700*time.Millisecond),
	); err != nil {
		t.Fatalf("tap TOC link: %v\nstderr: %s", err, p.stderr.String())
	}

	// Post-conditions: all-comments dismissed, TOC closed, md-view back,
	// and the second-section heading is actually within the viewport.
	var allCommentsStillVisible, tocStillOpen, mdViewAfter bool
	var headingTop, viewportH float64
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`!!document.querySelector('section.all-comments')`, &allCommentsStillVisible),
		chromedp.Evaluate(`!!document.querySelector('.toc-modal.is-open')`, &tocStillOpen),
		chromedp.Evaluate(`!!document.querySelector('.md-view')`, &mdViewAfter),
		chromedp.Evaluate(`(()=>{const h=document.getElementById('second-section'); return h ? h.getBoundingClientRect().top : -99999;})()`, &headingTop),
		chromedp.Evaluate(`window.innerHeight`, &viewportH),
	); err != nil {
		t.Fatalf("post-nav state query: %v\nstderr: %s", err, p.stderr.String())
	}
	if allCommentsStillVisible {
		t.Error("all-comments view still visible after TOC nav — issue #12 not fixed")
	}
	if tocStillOpen {
		t.Error("TOC overlay still open after heading tap (NavigateToHeading should clear TOCOpen)")
	}
	if !mdViewAfter {
		t.Fatal("md-view not in DOM after TOC nav — page didn't re-render")
	}
	// scrollIntoView with block:"center" puts the heading roughly mid-
	// viewport; tolerate the full viewport range. The bug symptom would
	// be top == 0 (no scroll happened, or the heading is at top of a
	// scrollable region but the user landed at the document top).
	if headingTop < 0 || headingTop > viewportH {
		t.Errorf("second-section heading top = %.0f, want within [0, %.0f] (server-driven scroll missed)", headingTop, viewportH)
	}
}

// TestE2E_TOCAbsentWhenFewHeadings opens a Markdown file with only one
// heading. The sidebar must not render, and the more-menu must not
// expose a "Table of contents" entry.
func TestE2E_TOCAbsentWhenFewHeadings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}
	p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdownTOC(t), 1200, 800)
	p.waitReady()
	p.clickFile("tiny.md")

	var sidebarInDOM, tocMenuItemInDOM bool
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector('.toc-sidebar')`, &sidebarInDOM),
		chromedp.Evaluate(`!!document.querySelector('button[name="openTOC"]')`, &tocMenuItemInDOM),
	); err != nil {
		t.Fatalf("tiny.md toc query: %v\nstderr: %s", err, p.stderr.String())
	}
	if sidebarInDOM {
		t.Error("toc-sidebar rendered for single-heading doc; the ≥ 2 gate failed")
	}
	if tocMenuItemInDOM {
		t.Error("toc menu item present for single-heading doc; the ≥ 2 gate failed")
	}
}

// TestE2E_RelativeImageInMarkdown pins issue #13: a markdown file that
// references a sibling image with a relative path must render the image
// in the SPA (not a broken-image icon), the underlying HTTP GET must
// return the actual PNG bytes (not the SPA HTML shell), and a missing
// sibling must produce a 404 instead of a misleading 200+HTML. The
// .git/ directory must stay inaccessible — the static fallback must
// reject dot-component paths even when an attacker knows the exact
// filename inside.
func TestE2E_RelativeImageInMarkdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e not supported on windows")
	}

	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	// Real 1×1 red PNG: the browser must successfully decode it for
	// naturalWidth > 0 to hold. Generated at runtime so we're not
	// trusting an opaque hex blob.
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	pngBytes := pngBuf.Bytes()
	mustWrite(t, dir, "pixel.png", string(pngBytes))
	// Mockups-shaped layout to mirror the motivating checklistkit case
	// (PLAN.md referencing mockups/screenshots/*.png).
	if err := os.MkdirAll(filepath.Join(dir, "mockups", "screenshots"), 0o755); err != nil {
		t.Fatalf("mkdir mockups: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mockups", "screenshots", "dash.png"), pngBytes, 0o644); err != nil {
		t.Fatalf("write nested png: %v", err)
	}
	mustWrite(t, dir, "vis.md", "# Visual review\n\n![One pixel](pixel.png)\n\n![Dashboard](mockups/screenshots/dash.png)\n")

	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	// Mutate vis.md so it shows up in the working-tree diff — otherwise
	// the file list is empty and the SPA never renders the markdown.
	mustWrite(t, dir, "vis.md", "# Visual review\n\nNow with a second paragraph.\n\n![One pixel](pixel.png)\n\n![Dashboard](mockups/screenshots/dash.png)\n")

	p := bootChromeAgainstRepo(t, dir, 1200, 800)
	p.waitReady()
	p.clickFile("vis.md")

	// Wait for the rendered markdown view, then poll until both <img>
	// elements report naturalWidth > 0 — which is the browser's signal
	// that it actually decoded a real image, NOT that the network
	// request returned HTML (which gives naturalWidth == 0).
	//
	// The markdown renderer now rewrites every relative image src
	// server-absolute (resolveImageSrc, gitdiff/linkrewrite.go), so the
	// rendered src is `/pixel.png`, not `pixel.png` — which is exactly what
	// lets a subdirectory README's images resolve. Query by that resolved src.
	deadline := time.Now().Add(5 * time.Second)
	var pixelW, dashW int
	for time.Now().Before(deadline) {
		if err := chromedp.Run(p.ctx,
			chromedp.WaitVisible(`.md-view`, chromedp.ByQuery),
			chromedp.Evaluate(`(document.querySelector('img[src="/pixel.png"]')||{}).naturalWidth||0`, &pixelW),
			chromedp.Evaluate(`(document.querySelector('img[src="/mockups/screenshots/dash.png"]')||{}).naturalWidth||0`, &dashW),
		); err != nil {
			t.Fatalf("query images: %v\nstderr: %s", err, p.stderr.String())
		}
		if pixelW > 0 && dashW > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pixelW == 0 {
		t.Errorf("pixel.png did not load in the browser (naturalWidth=0) — static fallback regressed?\nstderr: %s", p.stderr.String())
	}
	if dashW == 0 {
		t.Errorf("mockups/screenshots/dash.png did not load (naturalWidth=0) — nested-path static fallback regressed?\nstderr: %s", p.stderr.String())
	}

	// Now exercise the HTTP contract directly. The browser test above
	// proves the integration end-to-end; these calls pin the specific
	// status / Content-Type / body invariants that DevTools-driven
	// debugging will care about, AND verify the security-relevant
	// fallthrough cases.
	httpCases := []struct {
		name        string
		path        string
		wantStatus  int
		wantCType   string // prefix check
		wantBodyEq  []byte // exact body match if non-nil
		wantNotHTML bool   // body must NOT start with "<!" or "<html"
	}{
		{
			name: "GET /pixel.png returns the PNG, not the SPA",
			path: "/pixel.png", wantStatus: 200,
			wantCType: "image/png", wantBodyEq: pngBytes,
		},
		{
			name: "GET /mockups/screenshots/dash.png handles nested paths",
			path: "/mockups/screenshots/dash.png", wantStatus: 200,
			wantCType: "image/png", wantBodyEq: pngBytes,
		},
		{
			name: "GET /missing.png returns 404, not 200+HTML",
			path: "/missing.png", wantStatus: 404,
			wantNotHTML: true,
		},
		{
			// .git/HEAD always exists in a git repo — the dot-component
			// rejection MUST fire even though the file is real. The fall
			// through then returns the SPA shell, which is fine — what
			// matters is that the secret bytes are not in the response.
			name: "GET /.git/HEAD does NOT expose git internals",
			path: "/.git/HEAD", wantStatus: 200,
		},
	}

	client := &http.Client{Timeout: 5 * time.Second}
	for _, tc := range httpCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.Get(p.url + tc.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tc.path, err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d (body prefix=%q)",
					resp.StatusCode, tc.wantStatus, snippet(body))
			}
			if tc.wantCType != "" && !strings.HasPrefix(resp.Header.Get("Content-Type"), tc.wantCType) {
				t.Errorf("Content-Type = %q, want prefix %q",
					resp.Header.Get("Content-Type"), tc.wantCType)
			}
			if tc.wantBodyEq != nil && !bytes.Equal(body, tc.wantBodyEq) {
				t.Errorf("body bytes don't match the file on disk (got %d bytes, want %d)",
					len(body), len(tc.wantBodyEq))
			}
			if tc.wantNotHTML {
				trimmed := bytes.TrimSpace(body)
				if bytes.HasPrefix(trimmed, []byte("<!")) || bytes.HasPrefix(bytes.ToLower(trimmed), []byte("<html")) {
					t.Errorf("body is HTML (status=%d), want a non-HTML 404 — prefix=%q",
						resp.StatusCode, snippet(body))
				}
			}
			// For /.git/HEAD, separately verify the secret didn't leak.
			if tc.path == "/.git/HEAD" {
				gitHead, _ := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
				if len(gitHead) > 0 && bytes.Contains(body, bytes.TrimSpace(gitHead)) {
					t.Errorf("response body contains .git/HEAD contents — dot-component defense failed")
				}
			}
		})
	}
}

func snippet(b []byte) string {
	const max = 80
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

// setupFixtureHTMLRepo builds a fixture repo whose only changed file is
// an HTML page with a stylesheet sibling — exercises the iframe preview
// branch and proves the static fallback serves both the .html document
// and its relative .css subresource. The HTML/CSS are read from
// testdata/htmlpreview/ so the fixture is editable as real files (a
// developer can open them in a browser to inspect the visual).
func setupFixtureHTMLRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	mustWrite(t, dir, "keep.go", "package keep\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	for _, name := range []string{"index.html", "styles.css"} {
		body, err := os.ReadFile(filepath.Join("testdata", "htmlpreview", name))
		if err != nil {
			t.Fatalf("read testdata/htmlpreview/%s: %v", name, err)
		}
		mustWrite(t, dir, name, string(body))
	}
	return dir
}

// TestE2E_HTMLPreviewRendersAndAutoHeights pins the iframe preview (issue
// #26): a .html file renders once in a sandboxed iframe whose top-level
// <body> children carry their real source line range (data-from/data-to),
// and lvt-fx:preview-bridge sizes the iframe from the height its in-iframe
// beacon posts out (the opaque-origin sandbox blocks reading scrollHeight
// directly). The raw ↔ rendered toggle swaps the iframe for the code line
// view and back.
//
// Selecting a region of the preview to anchor a comment is driven by a
// parent-document overlay (lvt-fx:region-select) and is covered end-to-end
// by TestE2E_HTMLPreviewRegionSelect — NOT here. There is deliberately no
// in-iframe click path: iOS does not deliver iframe-internal events to the
// parent.
func TestE2E_HTMLPreviewRendersAndAutoHeights(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureHTMLRepo(t), 1200, 800)
	p.waitReady()

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
		t.Fatalf("enable network: %v", err)
	}
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.clickFile("index.html")

	// The preview iframe is opaque-origin (sandbox="allow-scripts"), so the
	// parent can't read its contentDocument — wait on the iframe element only;
	// the height poll below proves the bridge round-trip completed.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`iframe.html-preview`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("wait preview iframe: %v%s", err, diag())
	}

	// Auto-height: the preview-bridge sets an explicit pixel height from the
	// content's scrollHeight POSTED OUT by the in-iframe beacon (the parent
	// can't read scrollHeight directly under the opaque-origin sandbox).
	var heightPx float64
	if err := chromedp.Run(p.ctx, chromedp.Poll(
		`(() => {
			const fr = document.querySelector('iframe.html-preview');
			const h = parseFloat(fr && fr.style.height);
			return Number.isFinite(h) && h > 0 ? h : 0;
		})()`,
		&heightPx,
		chromedp.WithPollingTimeout(10*time.Second),
	)); err != nil {
		t.Fatalf("iframe did not auto-height: %v%s", err, diag())
	}
	if heightPx <= 0 {
		t.Fatalf("expected a positive auto-height, got %v%s", heightPx, diag())
	}

	// Toggle to Raw → iframe gone, code line view appears.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`form[aria-label="HTML view"] input[type=radio][value="raw"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.code .line`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("toggle to raw: %v%s", err, diag())
	}

	// And back — the preview iframe returns.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`form[aria-label="HTML view"] input[type=radio][value="rendered"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`iframe.html-preview`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("toggle back: %v%s", err, diag())
	}

	mu.Lock()
	for _, l := range consoleLines {
		if strings.Contains(strings.ToLower(l), "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	t.Logf("captured %d console lines, %d ws frames", len(consoleLines), len(wsFrames))
	mu.Unlock()
}

// TestE2E_HTMLPreviewRegionSelect pins issue #26's region commenting on
// the rendered-HTML preview: with the "Select region" toggle armed, a
// transparent overlay in the PARENT document (not inside the iframe — iOS
// doesn't deliver iframe-internal events to parent listeners) captures a
// drawn box, the lvt-fx:region-select directive hit-tests it against the
// iframe's data-from/data-to blocks, and SelectBlock anchors a comment to
// that source line range — round-tripping with the raw view + CSV. The
// drag is synthesized via Chrome's Input.dispatchMouseEvent so the shared
// drag spine's pointerdown/move/up handlers fire like a real gesture.
// setupFixtureHTMLRegionRepo writes a preview fixture with two tall, FIXED-
// height top-level blocks so region-select geometry is deterministic: the top
// <section> is source line 4, the bottom line 5, each 240px. A box dragged over
// the iframe's top quarter therefore always resolves to the top block (line 4),
// independent of fonts or async layout — the geometry the opaque-origin bridge
// posts out is stable. (Static inline CSS only — NEVER a JS framework whose JIT
// would shift layout after the rects are cached.)
func setupFixtureHTMLRegionRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	mustWrite(t, dir, "keep.go", "package keep\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	// Line 4 = top section, line 5 = bottom section (data-from is the source line).
	mustWrite(t, dir, "region.html",
		"<!doctype html>\n"+
			"<html><head><style>body{margin:0}section{height:240px;margin:0}</style></head>\n"+
			"<body>\n"+
			"<section style=\"background:#eef\">Top block</section>\n"+
			"<section style=\"background:#fee\">Bottom block</section>\n"+
			"</body></html>\n")
	return dir
}

func TestE2E_HTMLPreviewRegionSelect(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureHTMLRegionRepo(t), 1200, 800)
	p.waitReady()

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
		t.Fatalf("enable network: %v", err)
	}
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	p.clickFile("region.html")

	// The opaque-origin preview iframe can't be read from the parent; wait for
	// the bridge to size it (height>0 ⇒ the beacon posted height AND block
	// rects, so region-select has rects to hit-test).
	waitPreviewBridgeReady(t, p)

	// Arm the "Select region" toggle → the parent overlay appears.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name="toggleRegionSelect"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.region-overlay[data-surface="html"]`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
	); err != nil {
		t.Fatalf("arm region toggle: %v%s", err, diag())
	}

	// Read the iframe's OWN viewport rect (parent-readable — no contentDocument
	// needed). The fixture's top block is the first 240px of content, so a box
	// over the iframe's top ~100px always resolves to it (source line 4).
	const topBlockLine = 4
	var ir struct{ Left, Top, Width, Height float64 }
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
		const r = document.querySelector('iframe.html-preview').getBoundingClientRect();
		return { Left: r.left, Top: r.top, Width: r.width, Height: r.height };
	})()`, &ir)); err != nil {
		t.Fatalf("read iframe rect: %v%s", err, diag())
	}
	if ir.Width < 50 || ir.Height < 200 {
		t.Fatalf("iframe rect looks wrong: %+v%s", ir, diag())
	}

	// Drag a box over the top block: wide, and from just inside the top down to
	// ~100px (well within the 240px top section), clearing the click threshold.
	x1 := ir.Left + ir.Width*0.10
	y1 := ir.Top + 12
	x2 := ir.Left + ir.Width*0.80
	y2 := ir.Top + 100
	if err := chromedp.Run(p.ctx,
		cdpinput.DispatchMouseEvent(cdpinput.MousePressed, x1, y1).
			WithButton(cdpinput.Left).WithClickCount(1),
		cdpinput.DispatchMouseEvent(cdpinput.MouseMoved, (x1+x2)/2, (y1+y2)/2).
			WithButton(cdpinput.Left),
		cdpinput.DispatchMouseEvent(cdpinput.MouseMoved, x2, y2).
			WithButton(cdpinput.Left),
		cdpinput.DispatchMouseEvent(cdpinput.MouseReleased, x2, y2).
			WithButton(cdpinput.Left).WithClickCount(1),
		chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("region drag + composer: %v%s", err, diag())
	}

	// Type + save → comment persists and renders inline.
	if err := chromedp.Run(p.ctx,
		chromedp.SendKeys(`.composer textarea`, "comment from a drawn region", chromedp.ByQuery),
		chromedp.Click(`.composer button[name="addComment"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.inline-comment .body`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("save region comment: %v%s", err, diag())
	}

	// CSV: a line-anchored row (NOT kind=area) whose range overlaps the
	// block we dragged over — proving the box resolved to source lines.
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d:\n%v%s", len(rows), rows, diag())
	}
	row := rows[1]
	if row[10] == "area" {
		t.Errorf("kind = %q, want a line kind (region over HTML must store a line range, not a rect)", row[10])
	}
	from, _ := strconv.Atoi(row[2])
	to, _ := strconv.Atoi(row[3])
	if from <= 0 || to < from {
		t.Errorf("bad persisted line range from=%q to=%q", row[2], row[3])
	}
	// The resolved range must cover the top block (source line 4) we dragged over.
	if from > topBlockLine || to < topBlockLine {
		t.Errorf("persisted range [%d,%d] does not cover the top block (line %d)", from, to, topBlockLine)
	}

	mu.Lock()
	for _, l := range consoleLines {
		if strings.Contains(strings.ToLower(l), "error") {
			t.Errorf("browser console error: %s", l)
		}
	}
	mu.Unlock()
}

// TestE2E_FileLevelComment pins issue #16, phase 1: a top-level
// "Comment on file" affordance must be reachable for every file type
// (here we exercise a text .go file and a binary 1×1 PNG), the
// composer must save without a line range, and the persisted CSV row
// must have kind=file with from_line/to_line/side/anchor/anchor_status
// all empty/zero — the contract the skill consumes.
func TestE2E_FileLevelComment(t *testing.T) {
	repo := setupFixtureRepo(t)
	// Drop the canonical fixture files into the repo so the e2e and
	// the manual-test path documented in testdata/filecomments/README.md
	// exercise the exact same bytes — a developer copying that README
	// is following the same script as CI.
	for _, name := range []string{"README.md", "logo.png"} {
		body, err := os.ReadFile(filepath.Join("testdata", "filecomments", name))
		if err != nil {
			t.Fatalf("read testdata/filecomments/%s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), body, 0o644); err != nil {
			t.Fatalf("write %s into fixture repo: %v", name, err)
		}
	}
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	p.waitReady()

	// 1) Comment on a text file (edited.go). The file-header "Comment on
	// file" button must be present even without any line clicked.
	//
	// The composer opens in the BLOCKING MODAL (.fc-modal), not inline in .file-comments:
	// a mid-page scroll position used to leave an inline composer off-screen, so it was
	// moved into a viewport-centered dialog. The SAVED cards still list in .file-comments.
	// This test kept waiting on the old inline selector, which never appears — that is why
	// it hung rather than failed.
	p.clickFile("edited.go")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`button[name='openFileComment']`, chromedp.ByQuery),
		chromedp.Click(`button[name='openFileComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.fc-modal .composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.fc-modal .composer textarea`, "this file should be renamed", chromedp.ByQuery),
		chromedp.Click(`.fc-modal button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.file-comments .inline-comment .body`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("file-level comment on edited.go: %v\nstderr: %s", err, p.stderr.String())
	}

	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d:\n%v", len(rows), rows)
	}
	if rows[0][10] != "kind" {
		t.Errorf("header[10] = %q, want %q", rows[0][10], "kind")
	}
	r := rows[1]
	if r[1] != "edited.go" || r[2] != "0" || r[3] != "0" || r[4] != "" || r[8] != "" || r[9] != "" || r[10] != "file" {
		t.Errorf("file-level row shape wrong: %v", r)
	}
	if !strings.Contains(r[5], "renamed") {
		t.Errorf("body = %q, missing %q", r[5], "renamed")
	}

	// 2) Same flow on a binary file — the existing line-anchor path is
	// impossible there, so this is the gap the issue called out.
	p.clickFile("logo.png")
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.binary-preview.binary-image img`, chromedp.ByQuery),
		chromedp.WaitVisible(`button[name='openFileComment']`, chromedp.ByQuery),
		chromedp.Click(`button[name='openFileComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.fc-modal .composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.fc-modal .composer textarea`, "wrong file in this PR", chromedp.ByQuery),
		chromedp.Click(`.fc-modal button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.file-comments .inline-comment .body`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("file-level comment on logo.png: %v\nstderr: %s", err, p.stderr.String())
	}

	rows = p.readCSV()
	if len(rows) != 3 {
		t.Fatalf("expected header + 2 rows after binary comment, got %d:\n%v", len(rows), rows)
	}
	bin := rows[2]
	if bin[1] != "logo.png" || bin[10] != "file" {
		t.Errorf("binary file-level row shape wrong: %v", bin)
	}

	// 3) Cancel from file-mode: opening + cancelling clears the composer
	// without persisting anything.
	p.clickFile("edited.go")
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='openFileComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.fc-modal .composer textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.fc-modal .composer textarea`, "draft to discard", chromedp.ByQuery),
		chromedp.Click(`.fc-modal button[name='clearSelection']`, chromedp.ByQuery),
		// Cancel closes the whole modal — the composer no longer lives in .file-comments,
		// so the old "composer gone from .file-comments" wait was vacuously true while the
		// SendKeys before it could never land.
		chromedp.WaitNotPresent(`.fc-modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.file-comments .inline-comment .body`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("cancel file-level composer: %v\nstderr: %s", err, p.stderr.String())
	}

	rows = p.readCSV()
	if len(rows) != 3 {
		t.Errorf("cancel must not persist a new row, got %d rows", len(rows))
	}
}

// TestE2E_ImageAreaComment pins issue #16 phase 2: dragging a
// rectangle on a binary image opens the composer with the rectangle
// in `state.SelectionArea`, saving persists a `kind=area` row whose
// `area` column carries the 0..1-fraction JSON, and the saved area
// renders as an absolutely-positioned overlay on the image. The
// drag is synthesized via Chrome's Input.dispatchMouseEvent so the
// upstream `lvt-fx:area-select` directive's pointerdown/move/up
// handlers fire identically to a real user gesture.
func TestE2E_ImageAreaComment(t *testing.T) {
	repo := setupFixtureRepo(t)
	// Drop the canonical fixture image into the repo so the e2e and
	// the manual-test path documented in testdata/areacomments/README.md
	// exercise the exact same bytes.
	body, err := os.ReadFile(filepath.Join("testdata", "areacomments", "diagram.png"))
	if err != nil {
		t.Fatalf("read testdata/areacomments/diagram.png: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "diagram.png"), body, 0o644); err != nil {
		t.Fatalf("write diagram.png into fixture repo: %v", err)
	}
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	p.waitReady()

	p.clickFile("diagram.png")

	// Image area-commenting now requires arming the region toggle first (#57):
	// when disarmed the image carries no area-select directive (so touch can
	// pinch-zoom/scroll to examine it). Arm it → the directive attaches.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`button[name="toggleRegionSelect"]`, chromedp.ByQuery),
		chromedp.Click(`button[name="toggleRegionSelect"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("arm region toggle on image: %v\nstderr: %s", err, p.stderr.String())
	}

	// Wait for the armed image-with-areas wrapper to render so the directive
	// has had its FIRE-ON-CHANGE pass to attach pointer handlers.
	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`.image-with-areas.is-armed img[lvt-fx\:area-select]`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("wait img: %v\nstderr: %s", err, p.stderr.String())
	}

	// Compute drag coords inside the rendered image rect. Read the rect
	// from the page so we don't have to know the layout in advance —
	// the drag covers the top-left quadrant (≈ x=0.1, y=0.1, w=0.4, h=0.4
	// in fraction space).
	var rect struct{ Left, Top, Width, Height float64 }
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`(() => {
			const r = document.querySelector('.image-with-areas img').getBoundingClientRect();
			return { Left: r.left, Top: r.top, Width: r.width, Height: r.height };
		})()`, &rect),
	); err != nil {
		t.Fatalf("read image rect: %v", err)
	}
	if rect.Width < 10 || rect.Height < 10 {
		t.Fatalf("image rect too small: %+v", rect)
	}
	x1 := rect.Left + rect.Width*0.10
	y1 := rect.Top + rect.Height*0.10
	x2 := rect.Left + rect.Width*0.50
	y2 := rect.Top + rect.Height*0.50

	// Synthesize a left-button pointer-drag from (x1,y1) → (x2,y2).
	// Chrome's DispatchMouseEvent fires both pointer and mouse events,
	// so the directive's pointerdown/move/up handlers respond
	// identically to a real user gesture. Two Move events between
	// Press and Release help the directive's overlay-paint logic
	// settle on the final coords.
	if err := chromedp.Run(p.ctx,
		cdpinput.DispatchMouseEvent(cdpinput.MousePressed, x1, y1).
			WithButton(cdpinput.Left).WithClickCount(1),
		cdpinput.DispatchMouseEvent(cdpinput.MouseMoved, (x1+x2)/2, (y1+y2)/2).
			WithButton(cdpinput.Left),
		cdpinput.DispatchMouseEvent(cdpinput.MouseMoved, x2, y2).
			WithButton(cdpinput.Left),
		cdpinput.DispatchMouseEvent(cdpinput.MouseReleased, x2, y2).
			WithButton(cdpinput.Left).WithClickCount(1),
		chromedp.WaitVisible(`.file-comments .composer textarea`, chromedp.ByQuery),
		chromedp.WaitVisible(`.area-overlay.area-overlay-pending`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("synthetic drag + composer: %v\nstderr: %s", err, p.stderr.String())
	}

	// Type a body and save.
	if err := chromedp.Run(p.ctx,
		chromedp.SendKeys(`.file-comments .composer textarea`, "this colour should be brighter", chromedp.ByQuery),
		chromedp.Click(`.file-comments button[name='addComment']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.file-comments .inline-comment .body`, chromedp.ByQuery),
		// Pending overlay should clear; a saved overlay (not -pending)
		// should render with the persisted coords.
		chromedp.WaitNotPresent(`.area-overlay.area-overlay-pending`, chromedp.ByQuery),
		chromedp.WaitVisible(`.area-overlay:not(.area-overlay-pending)`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("save area comment: %v\nstderr: %s", err, p.stderr.String())
	}

	// Inspect the CSV: header must have 12 columns ending in `area`,
	// row must have kind=area + a valid area JSON inside [0,1].
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d:\n%v", len(rows), rows)
	}
	if rows[0][11] != "area" {
		t.Errorf("header[11] = %q, want %q", rows[0][11], "area")
	}
	row := rows[1]
	if row[1] != "diagram.png" || row[2] != "0" || row[3] != "0" || row[4] != "" || row[10] != "area" {
		t.Errorf("area row shape wrong: %v", row)
	}
	if !strings.Contains(row[5], "brighter") {
		t.Errorf("body missing: %q", row[5])
	}
	if !strings.Contains(row[11], `"x":`) || !strings.Contains(row[11], `"w":`) {
		t.Errorf("area JSON malformed: %q", row[11])
	}
}

// TestE2E_ImageZoomGating pins issue #57: an annotable image must not capture
// every touch as a comment. Disarmed (the default) the image carries no
// area-select directive and no touch-action lock, so a touch pinch-zooms /
// scrolls to examine it; the region toggle arms capture on demand and a hint
// makes the mode discoverable. Headless chromedp can't perform a real pinch,
// so this asserts the GATING that enables native zoom — not the gesture itself
// (that's phone signoff) — plus the arm/disarm transitions.
func TestE2E_ImageZoomGating(t *testing.T) {
	repo := setupFixtureRepo(t)
	body, err := os.ReadFile(filepath.Join("testdata", "areacomments", "diagram.png"))
	if err != nil {
		t.Fatalf("read testdata/areacomments/diagram.png: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "diagram.png"), body, 0o644); err != nil {
		t.Fatalf("write diagram.png into fixture repo: %v", err)
	}
	p := bootChromeAgainstRepo(t, repo, 1200, 800)
	p.waitReady()
	p.clickFile("diagram.png")

	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.binary-image img`, chromedp.ByQuery)); err != nil {
		t.Fatalf("image not rendered: %v\nstderr: %s", err, p.stderr.String())
	}

	has := func(sel string) bool {
		var v bool
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(fmt.Sprintf(`!!document.querySelector('%s')`, sel), &v)); err != nil {
			t.Fatalf("eval has(%q): %v", sel, err)
		}
		return v
	}
	// hasAttribute avoids CSS-escaping the colon in the directive's attribute name.
	hasDirective := func() bool {
		var v bool
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`(()=>{const i=document.querySelector('.image-with-areas img');return !!i&&i.hasAttribute('lvt-fx:area-select')})()`, &v)); err != nil {
			t.Fatalf("eval hasDirective: %v", err)
		}
		return v
	}
	touchAction := func() string {
		var v string
		if err := chromedp.Run(p.ctx, chromedp.Evaluate(
			`getComputedStyle(document.querySelector('.image-with-areas img')).touchAction`, &v)); err != nil {
			t.Fatalf("read touch-action: %v", err)
		}
		return v
	}

	// 1. Disarmed default: no capture directive, zoomable touch-action, hint +
	//    toggle present, not is-armed.
	if hasDirective() {
		t.Error("disarmed image carries the area-select capture directive — every touch would start a comment (#57)")
	}
	if ta := touchAction(); ta == "none" {
		t.Errorf("disarmed image touch-action = %q, want a zoomable value (not none)", ta)
	}
	if has(`.image-with-areas.is-armed`) {
		t.Error("image is-armed on load; it should default to examine (zoom) mode")
	}
	if !has(`.region-hint`) {
		t.Error("zoom hint missing on disarmed image (#57 discoverability)")
	}
	if !has(`button[name="toggleRegionSelect"]`) {
		t.Error("region toggle (the comment affordance) missing on image")
	}

	// 2. Arm → capture directive attaches, touch-action locks to none, hint hidden.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name="toggleRegionSelect"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.image-with-areas.is-armed img[lvt-fx\:area-select]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("arm region toggle: %v\nstderr: %s", err, p.stderr.String())
	}
	if ta := touchAction(); ta != "none" {
		t.Errorf("armed image touch-action = %q, want none (capture the drag)", ta)
	}
	if has(`.region-hint`) {
		t.Error("zoom hint still shown while armed")
	}

	// 3. Disarm → back to examine mode (directive gone, zoomable again).
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name="toggleRegionSelect"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.image-with-areas img[lvt-fx\:area-select]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("disarm region toggle: %v\nstderr: %s", err, p.stderr.String())
	}
	if ta := touchAction(); ta == "none" {
		t.Errorf("re-disarmed image touch-action = %q, want zoomable", ta)
	}
}

// TestE2E_DeepLinkHash exercises issue #7: the URL hash must drive
// state on initial load AND mirror state on user navigation.
// Verifies: load-with-hash selects file + line; clicking a different
// file updates location.hash; a markdown link `[other](OTHER.md)`
// gets rewritten to a SPA hash and clicking it stays in the SPA.
func TestE2E_DeepLinkHash(t *testing.T) {
	repo := setupFixtureRepo(t)
	// Seed the deep-link fixture files into the repo so the same bytes
	// drive both this test and any manual smoke from testdata/deeplinks.
	for _, name := range []string{"README.md", "OTHER.md", "mockups/dashboard.html"} {
		body, err := os.ReadFile(filepath.Join("testdata", "deeplinks", name))
		if err != nil {
			t.Fatalf("read testdata/deeplinks/%s: %v", name, err)
		}
		dst := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			t.Fatalf("write %s into fixture repo: %v", name, err)
		}
	}

	p := bootChromeAgainstRepo(t, repo, 1200, 800)

	// 1) Load with `#README.md:L7` directly in the URL. The directive's
	//    initial-arm path should dispatch setURLHash and the server
	//    should select README.md with line 7 highlighted.
	deepURL := p.url + "#README.md:L7"
	var locationHash string
	var selectedFileLabel string
	var dataAttrAfter string
	if err := chromedp.Run(p.ctx,
		chromedp.EmulateViewport(1200, 800),
		chromedp.Navigate(deepURL),
		// SSR has the first file alphabetically selected (OTHER.md);
		// after the WS connects, the directive's initial-arm path
		// dispatches setURLHash with "README.md:L7", the server
		// re-renders, and morphdom converges. ~200ms total in manual
		// probes; sleep generously to absorb chromedp + CI jitter.
		chromedp.WaitVisible(`header.bar`, chromedp.ByQuery),
		chromedp.Sleep(3*time.Second),
		chromedp.Evaluate(`window.location.hash`, &locationHash),
		chromedp.Evaluate(`document.querySelector('main.viewer strong')?.textContent || ''`, &selectedFileLabel),
		chromedp.AttributeValue(`header.bar`, "data-lvt-url-hash", &dataAttrAfter, nil, chromedp.ByQuery),
	); err != nil {
		var bodyHTML string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &bodyHTML, chromedp.ByQuery))
		t.Fatalf("load with #README.md:L7: %v\nstderr: %s\nbody: %s", err, p.stderr.String(), bodyHTML)
	}
	if selectedFileLabel != "README.md" {
		t.Errorf("selected file = %q, want README.md (dataAttr=%q hash=%q)",
			selectedFileLabel, dataAttrAfter, locationHash)
	}
	if dataAttrAfter != "README.md:L7" {
		t.Errorf("data-lvt-url-hash = %q, want README.md:L7", dataAttrAfter)
	}
	if locationHash != "#README.md:L7" {
		t.Errorf("after load, location.hash = %q, want %q", locationHash, "#README.md:L7")
	}
	if selectedFileLabel != "README.md" {
		t.Errorf("selected file label = %q, want README.md", selectedFileLabel)
	}

	// 2) Click the OTHER.md file in the drawer. URL hash should update
	//    to the new file (pushState, since the path component changed).
	p.clickFile("OTHER.md")
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`window.location.hash`, &locationHash),
	); err != nil {
		t.Fatalf("read hash after click: %v", err)
	}
	if !strings.HasPrefix(locationHash, "#OTHER.md") {
		t.Errorf("after click OTHER.md, location.hash = %q, want prefix #OTHER.md", locationHash)
	}

	// 3) Go back to README.md and verify the markdown link to OTHER.md
	//    was rewritten to a SPA hash. We're in line view by default;
	//    flip into raw-markdown OFF (so RenderedMarkdown drives) — the
	//    template ships `.md` files in rendered mode by default already,
	//    so just clicking selectFile gets us there.
	p.clickFile("README.md")
	var hrefValue string
	if err := chromedp.Run(p.ctx,
		// Wait for rendered markdown to appear (.md-view contains rendered
		// anchor tags). The link to OTHER.md has its href rewritten by the
		// markdown renderer.
		chromedp.WaitVisible(`.md-view a[href^="#"]`, chromedp.ByQuery),
		chromedp.AttributeValue(`.md-view a[href*="OTHER.md"]`, "href", &hrefValue, nil, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("locate rewritten link: %v\nstderr: %s", err, p.stderr.String())
	}
	if hrefValue != "#OTHER.md" {
		t.Errorf("OTHER.md link href = %q, want %q", hrefValue, "#OTHER.md")
	}

	// 4) Click the rewritten link. Browser navigates to #OTHER.md
	//    natively → hashchange fires → directive dispatches setURLHash
	//    → server switches the file. Verify the file actually changed
	//    without a full page reload (the chromedp session would have
	//    torn down on a real navigation).
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.md-view a[href="#OTHER.md"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`//main[contains(@class,'viewer')]//strong[normalize-space(text())='OTHER.md']`, chromedp.BySearch),
		chromedp.Evaluate(`window.location.hash`, &locationHash),
	); err != nil {
		t.Fatalf("click rewritten link: %v\nstderr: %s", err, p.stderr.String())
	}
	if !strings.HasPrefix(locationHash, "#OTHER.md") {
		t.Errorf("after clicking rewritten link, hash = %q, want prefix #OTHER.md", locationHash)
	}

	// 5) External links pass through unchanged.
	p.clickFile("README.md")
	var externalHref string
	if err := chromedp.Run(p.ctx,
		chromedp.AttributeValue(`.md-view a[href*="example.com"]`, "href", &externalHref, nil, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("locate external link: %v", err)
	}
	if externalHref != "https://example.com" {
		t.Errorf("external href = %q, want https://example.com", externalHref)
	}
}
