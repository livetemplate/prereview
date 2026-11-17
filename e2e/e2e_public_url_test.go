//go:build browser

// End-to-end: --public-url behind a proxy (#65). The proxy rewrites Host to the
// bind address, as some forwarding proxies do, so the browser's
// Origin (the proxy) never matches the Host prereview sees. Without
// --public-url livetemplate's same-origin check rejects the WebSocket and the
// page never goes live; with it the proxy's origin is allowed, and a tab on
// the plain loopback URL still goes live too.
// Run with: go test -tags=browser -run TestE2E_PublicURL ./e2e/...

package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// originRejected is livetemplate's log text for a WebSocket whose Origin
// failed the check.
const originRejected = "origin not allowed"

// freePort is a port nothing listens on right now.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// startHostRewritingProxy forwards to backendPort on loopback, rewriting Host
// to the backend and setting X-Forwarded-Proto, and returns its own URL.
func startHostRewritingProxy(t *testing.T, backendPort int) string {
	t.Helper()
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(backendPort)}
	rp := httputil.NewSingleHostReverseProxy(target)
	direct := rp.Director
	rp.Director = func(r *http.Request) {
		direct(r)
		r.Host = target.Host
		r.Header.Set("X-Forwarded-Proto", "http")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: rp, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String()
}

// wsHandshakes records the status of every WebSocket handshake a tab makes.
func wsHandshakes(t *testing.T, ctx context.Context) func() []int64 {
	t.Helper()
	var mu sync.Mutex
	var got []int64
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*cdpnetwork.EventWebSocketHandshakeResponseReceived); ok {
			mu.Lock()
			got = append(got, e.Response.Status)
			mu.Unlock()
		}
	})
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpnetwork.Enable().Do(ctx)
	})); err != nil {
		t.Fatalf("enable network: %v", err)
	}
	return func() []int64 {
		mu.Lock()
		defer mu.Unlock()
		return append([]int64(nil), got...)
	}
}

// waitUpgraded waits for a 101 handshake on a tab.
func waitUpgraded(t *testing.T, statuses func() []int64, what string, diag func() string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range statuses() {
			if s == 101 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: no WebSocket upgrade (handshakes %v)%s", what, statuses(), diag())
}

func TestE2E_PublicURLBehindProxy(t *testing.T) {
	backend := freePort(t)
	proxy := startHostRewritingProxy(t, backend)
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1200, 800,
		"--port", strconv.Itoa(backend), "--public-url", proxy)
	if p.url != proxy {
		t.Fatalf("READY = %q, want the public URL %q", p.url, proxy)
	}
	statuses := wsHandshakes(t, p.ctx)
	diag := captureDiag(t, p)
	p.waitReady()
	waitUpgraded(t, statuses, "proxied tab", diag)

	// The page is live through the proxy: an action round-trips over the socket.
	p.clickFile("edited.go")
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.line`, chromedp.ByQuery)); err != nil {
		t.Fatalf("file view through the proxy: %v%s", err, diag())
	}

	// A second tab on the plain loopback URL still goes live: the allow-list
	// covers the URLs prereview prints, not just the public one.
	local, cancel := chromedp.NewContext(p.ctx)
	defer cancel()
	localStatuses := wsHandshakes(t, local)
	if err := chromedp.Run(local,
		chromedp.Navigate(fmt.Sprintf("http://127.0.0.1:%d", backend)),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("loopback tab: %v%s", err, diag())
	}
	waitUpgraded(t, localStatuses, "loopback tab", diag)

	if strings.Contains(p.stderr.String(), originRejected) {
		t.Errorf("a WebSocket was rejected%s", diag())
	}
}

// TestE2E_PublicURLNeededBehindProxy is the control: the same proxy without
// --public-url gets its WebSocket rejected, so the test above proves the flag
// and not the proxy.
func TestE2E_PublicURLNeededBehindProxy(t *testing.T) {
	backend := freePort(t)
	proxy := startHostRewritingProxy(t, backend)
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1200, 800, "--port", strconv.Itoa(backend))
	diag := captureDiag(t, p)
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(proxy),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("page through the proxy: %v%s", err, diag())
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(p.stderr.String(), originRejected) {
		if time.Now().After(deadline) {
			t.Fatalf("the proxied WebSocket wasn't rejected without --public-url%s", diag())
		}
		time.Sleep(100 * time.Millisecond)
	}
}
