//go:build browser

// End-to-end tests for #114: `prereview --external <url> <path>` reviews the
// path and annotates the live site in one session, one store and one queue.
// Run with: go test -tags=browser -run 'TestCombined|TestE2E_Combined' ./e2e/
package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/csv"
)

// TestCombined_Launch pins Phase 1: a path with --external runs the repo review
// (default store, a v0 version, a live import drain) and serves the proxy.
func TestCombined_Launch(t *testing.T) {
	app := newLiveApp(t)
	repo := setupImportRepo(t)
	ready, store, cmd, stderr := startPrereview(t, prereviewBinary(t), repo, "--external", app.srv.URL, "--agent", "--no-update")
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	p := &runningPrereview{t: t, binary: prereviewBinary(t), store: store, stderr: stderr}
	diag := func() string { return "\n--- server ---\n" + stderr.String() }

	if want := filepath.Join(repo, ".prereview"); store != want {
		t.Errorf("STORE = %q, want the repo's default store %q", store, want)
	}

	// The UI frames the proxy, and both answer behind the one launch token.
	client := tokenClient(t, ready)
	page := get(t, client, ready)
	m := regexp.MustCompile(`class="ext-frame"\s+src="([^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the repo page frames no live site%s", diag())
	}
	if body := get(t, client, m[1]); !strings.Contains(body, "PAGE ONE") {
		t.Errorf("the proxy didn't serve the live site: %.200s", body)
	}

	// The repo-only machinery runs: a baseline version and the import drain.
	if entries, err := os.ReadDir(filepath.Join(store, "versions")); err != nil || len(entries) == 0 {
		t.Errorf("no baseline version in a combined session (%v)", err)
	}
	runImportCmd(t, p, `{"remote_id":"r1","source":"Docs","file":"plan.md","quote":"The rollout starts small.","body":"Too vague?"}`)
	waitRows(t, p, func(rows []csv.Row) bool { return len(rows) == 1 }, "the import drains into comments.csv", diag)
}

// get fetches url with client and returns the body.
func get(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// TestCombined_RefusesSingleFile pins decision 8: a live-site annotation has no
// file, so a single-file scope would hide it; the launch says so instead.
func TestCombined_RefusesSingleFile(t *testing.T) {
	repo := setupImportRepo(t)
	cmd := exec.Command(prereviewBinary(t), "--external", "http://127.0.0.1:1", "--port", "0", "--host", "127.0.0.1", "--no-update", filepath.Join(repo, "plan.md"))
	cmd.Env = prefsIsolatedEnv(repo)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a single-file combined launch must fail; got: %s", out)
	}
	if !strings.Contains(string(out), "single file") {
		t.Errorf("the error should name the single-file restriction: %s", out)
	}
}

// bootChromeCombined is bootChromeAgainstRepo for a combined session fronting
// target, at the given viewport, with the client wired.
func bootChromeCombined(t *testing.T, repo, target string, w, h int, extraArgs ...string) *runningPrereview {
	t.Helper()
	p := bootChromeAgainstRepo(t, repo, w, h, append([]string{"--external", target}, extraArgs...)...)
	// Bound the browser work so a stuck wait fails with the diag dump instead of
	// hanging to the binary's 10-minute timeout.
	ctx, cancel := within(p, 90*time.Second)
	t.Cleanup(cancel)
	p.ctx = ctx
	p.waitReadyAt(w, h)
	return p
}

// pickSurface taps the header's Live toggle if needed to show the live site
// (live) or the files, and waits for that surface.
func pickSurface(t *testing.T, p *runningPrereview, live bool, diag func() string) {
	t.Helper()
	pressed, shown := "false", `!!document.querySelector('.live-wrap.live-hidden') && !!document.querySelector('main.viewer article')`
	if live {
		pressed, shown = "true", `!!document.querySelector('.live-wrap:not(.live-hidden)') && !document.querySelector('main.viewer article')`
	}
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(()=>{const b=document.querySelector('button.live-toggle');if(b.getAttribute('aria-pressed')!=='`+pressed+`')b.click();})()`, nil)); err != nil {
		t.Fatalf("tap Live: %v%s", err, diag())
	}
	waitJSDiag(t, p, shown+` && document.querySelector('button.live-toggle').getAttribute('aria-pressed') === '`+pressed+`'`, "the surface shows (live="+pressed+")", diag)
}

// TestE2E_CombinedStageSurvivesToggle is the §3.1 probe: the live stage stays
// mounted while the files show, so switching surfaces never reloads the framed
// site or loses its route. It also pins that a hidden stage takes no space, and
// that a region pin shows on the first switch to Live.
func TestE2E_CombinedStageSurvivesToggle(t *testing.T) {
	for _, vp := range []struct {
		name string
		w, h int
	}{{"desktop", 1200, 900}, {"phone", 390, 844}} {
		t.Run(vp.name, func(t *testing.T) {
			app := newLiveApp(t)
			repo := setupImportRepo(t)
			p := bootChromeCombined(t, repo, app.srv.URL, vp.w, vp.h, "--agent")
			diag := captureDiag(t, p)

			// Files first: the stage is mounted but takes no space.
			var stageH float64
			if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('.ext-stage').getBoundingClientRect().height`, &stageH)); err != nil {
				t.Fatalf("stage height: %v%s", err, diag())
			}
			if stageH != 0 {
				t.Errorf("the hidden stage takes %vpx%s", stageH, diag())
			}

			// Stay on Files past the beacon's last start-up report (3s): the frame
			// loaded hidden reports a 0x0 page, and only its resize on showing
			// gives the region overlay a page size to draw against.
			time.Sleep(3500 * time.Millisecond)
			pickSurface(t, p, true, diag)
			waitJSDiag(t, p, `((document.querySelector('.bar .title-page')||{}).textContent||'').endsWith('/')`, "the header shows the framed page", diag)
			drawRegion(t, p, "Button too low", diag)

			// Move the framed site to /page2, then switch back and forth.
			if err := chromedp.Run(p.ctx, chromedp.Evaluate(`(()=>{const f=document.querySelector('iframe.ext-frame');window.__frame=f;f.src=new URL('/page2',f.src).href;})()`, nil)); err != nil {
				t.Fatalf("navigate the frame: %v%s", err, diag())
			}
			waitJSDiag(t, p, `((document.querySelector('.bar .title-page')||{}).textContent||'').endsWith('/page2')`, "the beacon reports /page2", diag)
			home, page2 := app.hitCount("/"), app.hitCount("/page2")

			pickSurface(t, p, false, diag)
			pickSurface(t, p, true, diag)
			pickSurface(t, p, false, diag)
			pickSurface(t, p, true, diag)

			var same bool
			if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector('iframe.ext-frame') === window.__frame`, &same)); err != nil {
				t.Fatal(err)
			}
			if !same {
				t.Errorf("the iframe element was replaced across the toggles%s", diag())
			}
			if app.hitCount("/") != home || app.hitCount("/page2") != page2 {
				t.Errorf("the framed site reloaded: / %d→%d, /page2 %d→%d%s", home, app.hitCount("/"), page2, app.hitCount("/page2"), diag())
			}
			if got := textOf(t, p, `.bar .title-page`); !strings.HasSuffix(got, "/page2") {
				t.Errorf("the framed route was lost: header shows %q%s", got, diag())
			}
		})
	}
}

// TestE2E_CombinedLiveKey pins the "l" shortcut: it switches surfaces in a
// combined session and the keyboard help lists it.
func TestE2E_CombinedLiveKey(t *testing.T) {
	app := newLiveApp(t)
	p := bootChromeCombined(t, setupImportRepo(t), app.srv.URL, 1200, 900)
	diag := captureDiag(t, p)
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent("l")); err != nil {
		t.Fatal(err)
	}
	waitJSDiag(t, p, `!!document.querySelector('.live-wrap:not(.live-hidden)') && document.querySelector('button.live-toggle').getAttribute('aria-pressed') === 'true'`, "l shows the live site", diag)
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent("l")); err != nil {
		t.Fatal(err)
	}
	waitJSDiag(t, p, `!!document.querySelector('.live-wrap.live-hidden') && !!document.querySelector('main.viewer article') && document.querySelector('button.live-toggle').getAttribute('aria-pressed') === 'false'`, "l again shows the files", diag)
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent("?")); err != nil {
		t.Fatal(err)
	}
	waitJSDiag(t, p, `[...document.querySelectorAll('.kbd-help-modal.is-open .kbd-help-list dt')].some(e => e.textContent.trim() === 'l')`, "the keyboard help lists l", diag)
}

// TestE2E_CombinedOneQueue pins Phase 3: a line comment and a live-site
// annotation from one session share the Queue (the annotation shows in "This
// file" with its page address), All comments lists both, a jump to the
// annotation switches to the live site, and the agent reads both.
func TestE2E_CombinedOneQueue(t *testing.T) {
	app := newLiveApp(t)
	p := bootChromeCombined(t, setupFixtureRepo(t), app.srv.URL, 1200, 900, "--agent")
	diag := captureDiag(t, p)

	p.clickFile("edited.go")
	addLineComment(t, p, 0, 4, "Rename this")
	pickSurface(t, p, true, diag)
	waitJSDiag(t, p, `((document.querySelector('.bar .title-page')||{}).textContent||'').endsWith('/')`, "the header shows the framed page", diag)
	drawRegion(t, p, "CTA too faint", diag)
	pickSurface(t, p, false, diag)

	// The Queue, scoped to This file, holds both.
	if err := chromedp.Run(p.ctx, chromedp.Click(`.queue-dropdown .queue-trigger`, chromedp.ByQuery)); err != nil {
		t.Fatalf("open the queue: %v%s", err, diag())
	}
	locs := `[...document.querySelectorAll('.queue-row .queue-loc')].map(e => e.textContent.trim())`
	waitJSDiag(t, p, locs+`.join('|') === 'edited.go:4|/' || `+locs+`.join('|') === '/|edited.go:4'`, "the queue lists edited.go:4 and /", diag)

	// A jump from the Queue to the annotation shows the live site, focused.
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`[...document.querySelectorAll('.queue-row')].find(r => r.querySelector('.queue-loc').textContent.trim() === '/').querySelector('button.queue-item').click()`, nil)); err != nil {
		t.Fatalf("jump from the queue: %v%s", err, diag())
	}
	waitJSDiag(t, p, `!!document.querySelector('.live-wrap:not(.live-hidden)') && !!document.querySelector('.pin-layer .is-focused') && document.querySelector('button.live-toggle').getAttribute('aria-pressed') === 'true'`,
		"the queue jump shows the focused annotation", diag)

	// All comments lists the annotation by its page, and jumps back to it.
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent("a")); err != nil {
		t.Fatal(err)
	}
	waitJSDiag(t, p, `[...document.querySelectorAll('.ac-where code')].map(e => e.textContent).sort().join('|') === '/|edited.go'`, "All comments lists both", diag)
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`[...document.querySelectorAll('.ac-item')].find(i => i.querySelector('.ac-where code').textContent === '/').querySelector('.ac-jump-btn').click()`, nil)); err != nil {
		t.Fatalf("jump from All comments: %v%s", err, diag())
	}
	waitJSDiag(t, p, `!document.querySelector('.ac-list') && !!document.querySelector('.live-wrap:not(.live-hidden)')`, "the All comments jump shows the live site", diag)

	// The agent reads both from the one store.
	raw, err := exec.Command(p.binary, "comments", "--json", "--out", p.store).Output()
	if err != nil {
		t.Fatalf("prereview comments: %v", err)
	}
	var cs []struct{ Kind, File, URL string }
	if err := json.Unmarshal(raw, &cs); err != nil || len(cs) != 2 {
		t.Fatalf("comments --json (%v): %s", err, raw)
	}
	kinds := map[string]bool{}
	for _, c := range cs {
		kinds[c.Kind+":"+c.File+c.URL] = true
	}
	if !kinds[":edited.go"] || !kinds["region:/"] { // a line comment has an empty kind
		t.Errorf("the agent's list lacks one surface: %s", raw)
	}
}

// TestE2E_CombinedFrameFollowsPageHost pins that the framed site loads when the
// page was opened by another name for the same address (a phone opening the
// tailnet name from an ALT line). The launch cookie belongs to the host the
// page was opened by, so a frame pointed at the bind address got a 403.
func TestE2E_CombinedFrameFollowsPageHost(t *testing.T) {
	app := newLiveApp(t)
	p := bootChromeCombined(t, setupImportRepo(t), app.srv.URL, 1200, 900)
	diag := captureDiag(t, p)
	p.url = strings.Replace(p.url, "127.0.0.1", "localhost", 1)
	p.waitReadyAt(1200, 900)
	waitJSDiag(t, p, `new URL(document.querySelector('iframe.ext-frame').src).hostname === 'localhost'`, "the frame uses the page's host", diag)
	pickSurface(t, p, true, diag)
	waitJSDiag(t, p, `((document.querySelector('.bar .title-page')||{}).textContent||'').endsWith('/')`, "the framed site loads and its beacon reports /", diag)
}

// waitJSDiag is waitJSTrue that fails with the diag dump (server log, console,
// WS frames, HTML) after 15s.
func waitJSDiag(t *testing.T, p *runningPrereview, js, label string, diag func() string) {
	t.Helper()
	if err := chromedp.Run(p.ctx, chromedp.Poll(js, nil, chromedp.WithPollingTimeout(15*time.Second))); err != nil {
		t.Fatalf("%s: %v%s", label, err, diag())
	}
}
