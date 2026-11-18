//go:build browser

// End-to-end for the request guards: the launch token on every route but the
// embedded assets, the origin check on HTTP actions, and the sandbox on repo
// files served by staticFallback.
//
// Run: go test -tags=browser -run 'TestE2E_(TokenRequired|CrossOriginActionBlocked|RepoHTMLSandboxed)' ./e2e/...

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

const tokenRefused = "open the URL prereview printed"

// freshBrowser starts a second Chromium with its own empty cookie jar: a
// browser that never opened the printed URL.
func freshBrowser(t *testing.T) context.Context {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(findChromium(t)),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(func() { cancel(); cancelAlloc() })
	return ctx
}

func bodyText(t *testing.T, ctx context.Context, url string) string {
	t.Helper()
	var body string
	if err := chromedp.Run(ctx, chromedp.Navigate(url), chromedp.Text(`body`, &body, chromedp.ByQuery)); err != nil {
		t.Fatalf("navigate %s: %v", url, err)
	}
	return body
}

func TestE2E_TokenRequired(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1200, 800)
	statuses := wsHandshakes(t, p.ctx)
	diag := captureDiag(t, p)
	base := baseURL(p.url)

	if !strings.Contains(p.url, "?token=") {
		t.Fatalf("READY %q carries no token", p.url)
	}
	if got := bodyText(t, p.ctx, base+"/"); !strings.Contains(got, tokenRefused) {
		t.Fatalf("the URL without its token served the review:\n%s%s", got, diag())
	}

	// The printed URL loads, the token leaves the address bar, and the socket
	// goes live on the cookie.
	p.waitReady()
	var loc string
	if err := chromedp.Run(p.ctx, chromedp.Location(&loc)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loc, base+"/") || strings.Contains(loc, "token=") {
		t.Errorf("address bar = %q after the token redirect, want %s/ with no token", loc, base)
	}
	waitUpgraded(t, statuses, "review tab", diag)
	if got := bodyText(t, p.ctx, base+"/_usage"); strings.Contains(got, tokenRefused) {
		t.Errorf("the cookie did not admit /_usage:\n%s", got)
	}

	// A browser context without the cookie is refused, and a wrong token
	// doesn't buy one.
	other := freshBrowser(t)
	for _, u := range []string{base + "/", base + "/_usage", base + "/README.md", base + "/?token=wrong"} {
		if got := bodyText(t, other, u); !strings.Contains(got, tokenRefused) {
			t.Errorf("a browser without the cookie got %s:\n%s", u, got)
		}
	}
	if css := bodyText(t, other, base+"/prereview.css"); strings.Contains(css, tokenRefused) || css == "" {
		t.Errorf("an embedded asset needs no token, got %q", css)
	}
}

// A page on another port of the same host is same-site, so the browser sends
// the cookie with its request: the origin check is what refuses the action.
func TestE2E_CrossOriginActionBlocked(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1200, 800)
	diag := captureDiag(t, p)
	p.waitReady()
	target := baseURL(p.url) + "/"

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><title>waiting</title><script>
fetch(%q, {method: "POST", mode: "no-cors", credentials: "include",
  headers: {"Content-Type": "text/plain"}, body: JSON.stringify({action: "quit"})})
  .then(() => { document.title = "sent"; }, (e) => { document.title = "error " + e; });
</script>`, target)
	}))
	defer other.Close()

	if err := chromedp.Run(p.ctx, chromedp.Navigate(other.URL)); err != nil {
		t.Fatal(err)
	}
	waitJSTrue(t, p.ctx, `document.title === "sent"`, 10*time.Second, "the other port's page sent its request")

	// The server is still up and serving, and it saw and refused the request.
	time.Sleep(1500 * time.Millisecond)
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(target),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the server stopped after a cross-origin action: %v%s", err, diag())
	}
	if !strings.Contains(p.stderr.String(), "origin rejected") {
		t.Fatalf("the server never logged refusing the request, so it may have stopped at the token guard instead:%s", diag())
	}
}

// A repo .html opened by its own URL runs with no scripts: the response's
// sandbox CSP gives it an opaque origin and no script execution, so the
// review keeps running and its socket count is unchanged.
func TestE2E_RepoHTMLSandboxed(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1200, 800)
	statuses := wsHandshakes(t, p.ctx)
	diag := captureDiag(t, p)
	p.waitReady()
	waitUpgraded(t, statuses, "review tab", diag)
	before := len(statuses())

	page := `<!doctype html><title>static</title><script>
document.title = "ran";
new WebSocket("ws://" + location.host + "/");
fetch("/", {method: "POST", headers: {"Content-Type": "text/plain"}, body: JSON.stringify({action: "quit"})});
</script><p>page</p>`
	if err := os.WriteFile(filepath.Join(p.repo, "scripted.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(baseURL(p.url)+"/scripted.html"),
		chromedp.WaitVisible(`p`, chromedp.ByQuery),
		chromedp.Sleep(1500*time.Millisecond),
		chromedp.Title(&title),
	); err != nil {
		t.Fatalf("open the repo file: %v%s", err, diag())
	}
	if title != "static" {
		t.Errorf("the repo file's script ran on prereview's origin (title %q)", title)
	}
	if got := statuses()[before:]; len(got) != 0 {
		t.Errorf("the repo file opened a WebSocket (handshakes %v)", got)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(baseURL(p.url)+"/"),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("the server stopped after the repo file loaded: %v%s", err, diag())
	}
}
