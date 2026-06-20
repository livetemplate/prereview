//go:build browser

// End-to-end test for `prereview --external`: a live local site proxied on a
// second origin, framed in the UI, region-annotated through the parent overlay.
// Run with: go test -tags=browser -run TestE2E_External ./...
package e2e

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cdpinput "github.com/chromedp/cdproto/input"
	cdpnetwork "github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// liveApp is a stand-in for the user's running local site: two pages plus a
// ROOT-RELATIVE asset (the case a path-prefix proxy would break). It records
// which paths were requested so the test can prove the proxy forwarded them.
type liveApp struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newLiveApp(t *testing.T) *liveApp {
	t.Helper()
	app := &liveApp{hits: map[string]int{}}
	page := func(title, link string) string {
		// Root-relative asset ref (/app.css) + a tall body so the page scrolls,
		// + a link to the other page (full navigation, so the beacon fires).
		return `<!doctype html><html><head><link rel="stylesheet" href="/app.css"></head>` +
			`<body style="margin:0"><h1>` + title + `</h1>` +
			`<div style="height:2400px;background:linear-gradient(#fff,#dde)"></div>` +
			`<a id="nav" href="` + link + `">` + link + `</a></body></html>`
	}
	app.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app.mu.Lock()
		app.hits[r.URL.Path]++
		app.mu.Unlock()
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			// A blocker the proxy must strip so the iframe can frame it.
			w.Header().Set("X-Frame-Options", "DENY")
			_, _ = w.Write([]byte(page("PAGE ONE", "/page2")))
		case "/page2":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(page("PAGE TWO", "/")))
		case "/app.css":
			w.Header().Set("Content-Type", "text/css")
			_, _ = w.Write([]byte("h1{color:#06c}"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(app.srv.Close)
	return app
}

func (a *liveApp) hitCount(path string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hits[path]
}

// startPrereviewExternal launches the binary in --external mode and returns the
// UI URL once READY prints. Mirrors startPrereview but with proxy-mode flags
// (no repo positional).
func startPrereviewExternal(t *testing.T, binary, target, out string) (string, *exec.Cmd, *bytesBuf) {
	t.Helper()
	cmd := exec.Command(binary,
		"--external", target, "--out", out,
		"--port", "0", "--host", "127.0.0.1", "--no-update")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr := newBytesBuf()
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}
	urlCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			t.Logf("prereview stdout: %s", line)
			if strings.HasPrefix(line, "READY ") {
				urlCh <- strings.TrimPrefix(line, "READY ")
				// Keep draining so later stdout lines don't fill the pipe.
				go io.Copy(io.Discard, stdout)
				return
			}
		}
	}()
	select {
	case url := <-urlCh:
		return url, cmd, stderr
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("prereview never printed READY\nstderr: %s", stderr.String())
	}
	return "", nil, nil
}

// bootChromeExternal builds the binary, starts it against the live app in
// external mode, and wires a headless chrome — the external-mode analogue of
// bootChromeAgainstRepo. runningPrereview.repo is set to the --out dir so
// readCSV() finds <out>/.prereview/comments.csv.
func bootChromeExternal(t *testing.T, target string, viewportW, viewportH int) *runningPrereview {
	t.Helper()
	chromium := findChromium(t)
	binary := filepath.Join(t.TempDir(), "prereview")
	if out, err := exec.Command("go", "build", "-o", binary, "..").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out := t.TempDir()
	url, srv, stderr := startPrereviewExternal(t, binary, target, out)

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromium),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.WindowSize(viewportW, viewportH),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	// Bound the whole browser interaction so a stuck WaitVisible/Poll fails
	// fast (with the diag dump) instead of hanging to the 10-min binary timeout.
	ctx, tcancel := context.WithTimeout(ctx, 75*time.Second)
	t.Cleanup(func() {
		tcancel()
		cancel()
		allocCancel()
		_ = srv.Process.Kill()
		_, _ = srv.Process.Wait()
	})
	return &runningPrereview{t: t, url: url, repo: out, cmd: srv, stderr: stderr, ctx: ctx, cancel: cancel}
}

// TestE2E_ExternalRegionAnnotate drives the whole feature through a real
// browser: the live app frames cross-origin through the proxy (root-relative
// asset forwarded), the beacon reports the page, a toggle-armed drag over the
// parent overlay places a region annotation, and it persists as kind=region +
// re-pins on the page. A second-page nav swaps the per-page pin set.
func TestE2E_ExternalRegionAnnotate(t *testing.T) {
	app := newLiveApp(t)
	p := bootChromeExternal(t, app.srv.URL, 1200, 900)

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
	_ = chromedp.Run(p.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.Enable().Do(ctx)
	}))
	diag := func() string {
		var html string
		_ = chromedp.Run(p.ctx, chromedp.OuterHTML(`body`, &html, chromedp.ByQuery))
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("\n--- server ---\n%s\n--- console ---\n%s\n--- ws ---\n%s\n--- html ---\n%s",
			p.stderr.String(), strings.Join(consoleLines, "\n"), strings.Join(wsFrames, "\n"), html)
	}

	// Frame the live site; wait for the client to wire + the beacon's nav to
	// round-trip (CurrentURL renders into the header as .ext-page).
	if err := chromedp.Run(p.ctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`iframe.ext-frame`, chromedp.ByQuery),
		chromedp.WaitVisible(`.bar-external .ext-page`, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
	); err != nil {
		t.Fatalf("frame live site + beacon nav: %v%s", err, diag())
	}

	// Topology proof: the page's ROOT-RELATIVE /app.css forwarded through the
	// proxy (a path-prefix proxy would have missed it).
	if app.hitCount("/app.css") == 0 {
		t.Fatalf("root-relative /app.css was not forwarded through the proxy%s", diag())
	}

	// Arm the toggle → the page-surface region overlay appears.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name="toggleRegionSelect"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.region-overlay[data-surface="page"]`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
	); err != nil {
		t.Fatalf("arm region toggle: %v%s", err, diag())
	}

	// Read the overlay rect and drag a box well inside it.
	var ov struct{ X, Y, Width, Height float64 }
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
		const b = document.querySelector('.region-overlay[data-surface="page"]').getBoundingClientRect();
		return { X: b.left, Y: b.top, Width: b.width, Height: b.height };
	})()`, &ov)); err != nil {
		t.Fatalf("read overlay rect: %v%s", err, diag())
	}
	if ov.Width < 50 || ov.Height < 50 {
		t.Fatalf("overlay too small: %+v%s", ov, diag())
	}
	x1 := ov.X + ov.Width*0.30
	y1 := ov.Y + ov.Height*0.30
	x2 := ov.X + ov.Width*0.60
	y2 := ov.Y + ov.Height*0.55
	if err := chromedp.Run(p.ctx,
		cdpinput.DispatchMouseEvent(cdpinput.MousePressed, x1, y1).WithButton(cdpinput.Left).WithClickCount(1),
		cdpinput.DispatchMouseEvent(cdpinput.MouseMoved, (x1+x2)/2, (y1+y2)/2).WithButton(cdpinput.Left),
		cdpinput.DispatchMouseEvent(cdpinput.MouseMoved, x2, y2).WithButton(cdpinput.Left),
		cdpinput.DispatchMouseEvent(cdpinput.MouseReleased, x2, y2).WithButton(cdpinput.Left).WithClickCount(1),
		chromedp.WaitVisible(`.ext-composer textarea`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("region drag + composer: %v%s", err, diag())
	}

	// Type + save → persists and re-pins on the page. The on-page pin appears
	// immediately; the annotations drawer is collapsed by default.
	if err := chromedp.Run(p.ctx,
		chromedp.SendKeys(`.ext-composer textarea`, "CTA too low contrast", chromedp.ByQuery),
		chromedp.Click(`.ext-composer button[name="addComment"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.pin-layer .area-overlay`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("save region comment: %v%s", err, diag())
	}

	// Regression: after the post-save server render, the pin layer must keep
	// its imperative size/transform (morphdom would otherwise wipe them, so the
	// pin vanished until the next scroll). A sized layer proves sync() re-applied.
	var pinLayerW float64
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`document.querySelector('.pin-layer').getBoundingClientRect().width`, &pinLayerW)); err != nil {
		t.Fatalf("read pin-layer width: %v%s", err, diag())
	}
	if pinLayerW <= 0 {
		t.Errorf("pin layer collapsed after save (width %v) — pin would vanish until scroll%s", pinLayerW, diag())
	}

	// The drawer is collapsed by default (sidebar hidden); opening it via the
	// header toggle reveals the saved annotation.
	var sidebarHidden bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`!document.querySelector('.ext-sidebar').classList.contains('is-open')`, &sidebarHidden)); err != nil {
		t.Fatalf("check sidebar collapsed: %v%s", err, diag())
	}
	if !sidebarHidden {
		t.Errorf("annotations drawer should be collapsed by default%s", diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name="toggleAnnotations"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.ext-sidebar.is-open .ext-anno .anno-body`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open annotations drawer: %v%s", err, diag())
	}

	// Tapping "Locate" on a sidebar annotation highlights its pin on the page
	// (the annotation is on the current page "/", so its pin is in the layer).
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.ext-anno .anno-locate`, chromedp.ByQuery),
		chromedp.WaitVisible(`.pin-layer .area-overlay.is-focused`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("locate/highlight annotation: %v%s", err, diag())
	}

	// Clicking the pin itself opens its comment in the editor (composer in edit
	// mode, body pre-filled). Then cancel to leave the annotation intact.
	var composerLabel, draft string
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.pin-layer .pin-btn`, chromedp.ByQuery),
		chromedp.WaitVisible(`.ext-composer textarea`, chromedp.ByQuery),
		chromedp.Text(`.ext-composer strong`, &composerLabel, chromedp.ByQuery),
		chromedp.Value(`.ext-composer textarea`, &draft, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("click pin → edit composer: %v%s", err, diag())
	}
	if !strings.Contains(composerLabel, "Editing") {
		t.Errorf("clicking a pin should open the editor, label = %q%s", composerLabel, diag())
	}
	if draft != "CTA too low contrast" {
		t.Errorf("editor not pre-filled with the comment body, got %q%s", draft, diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.ext-composer button[name="clearSelection"]`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("cancel edit: %v%s", err, diag())
	}

	// CSV: exactly one kind=region row, anchored to URL "/" with a rectangle.
	rows := p.readCSV()
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 row, got %d:\n%v%s", len(rows), rows, diag())
	}
	row := rows[1]
	if row[10] != "region" {
		t.Errorf("kind = %q, want region", row[10])
	}
	if row[11] == "" || !strings.Contains(row[11], `"x"`) {
		t.Errorf("area = %q, want a rectangle JSON", row[11])
	}
	if row[12] != "/" {
		t.Errorf("url = %q, want /", row[12])
	}

	// Navigate the live app to page 2 by re-pointing the iframe's own src to
	// the proxy's /page2 (a full navigation; setting the parent's iframe src
	// is always allowed, unlike a cross-origin contentWindow read). The
	// beacon on the new page reports it → setProxyURL → the per-page pin set
	// empties (the annotation was on "/").
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(
		`(()=>{const f=document.querySelector('iframe.ext-frame');f.src=new URL('/page2',f.src).href;})()`, nil)); err != nil {
		t.Fatalf("navigate iframe to /page2: %v%s", err, diag())
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Poll(`(() => {
			const p = document.querySelector('.bar-external .ext-page');
			return !!p && p.textContent.indexOf('/page2') !== -1;
		})()`, nil, chromedp.WithPollingTimeout(20*time.Second)),
	); err != nil {
		t.Fatalf("beacon nav to /page2 did not update CurrentURL: %v%s", err, diag())
	}
	// On page 2 the per-page pin layer is empty, but the sidebar (all pages)
	// still lists the page-1 annotation.
	var pinCount, annoCount int
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('.pin-layer .area-overlay').length`, &pinCount),
		chromedp.Evaluate(`document.querySelectorAll('.ext-sidebar .ext-anno').length`, &annoCount),
	); err != nil {
		t.Fatalf("count pins/annos on page2: %v%s", err, diag())
	}
	if pinCount != 0 {
		t.Errorf("page2 pin layer should be empty (annotation was on /), got %d pins%s", pinCount, diag())
	}
	if annoCount != 1 {
		t.Errorf("sidebar should still list the page-1 annotation, got %d%s", annoCount, diag())
	}
}
