package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newTestUpstream is a stand-in "live local site": an HTML root that advertises
// framing/CSP blockers, plus a non-HTML asset at a root-relative path.
func newTestUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
			_, _ = io.WriteString(w, "<html><head></head><body>hi</body></html>")
		case "/app.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = io.WriteString(w, "console.log(1)")
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestExternalProxyInjectsBeaconAndStripsFraming(t *testing.T) {
	upstream := newTestUpstream()
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	proxy := newExternalProxy(target)

	// HTML navigation: beacon injected, framing/CSP stripped, original kept.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	bs := string(body)

	if got := res.Header.Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options not stripped: %q", got)
	}
	if got := res.Header.Get("Content-Security-Policy"); got != "" {
		t.Errorf("Content-Security-Policy not stripped: %q", got)
	}
	if !strings.Contains(bs, "__prereview") {
		t.Errorf("beacon not injected: %q", bs)
	}
	if !strings.Contains(bs, "<body>hi") {
		t.Errorf("original document body lost: %q", bs)
	}
	beaconAt := strings.Index(bs, "__prereview")
	bodyCloseAt := strings.Index(bs, "</body>")
	if beaconAt < 0 || bodyCloseAt < 0 || beaconAt > bodyCloseAt {
		t.Errorf("beacon must be injected before </body>: beacon@%d body@%d", beaconAt, bodyCloseAt)
	}
}

func TestExternalProxyPassesAssetsThrough(t *testing.T) {
	upstream := newTestUpstream()
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	proxy := newExternalProxy(target)

	// A root-relative asset must forward verbatim — no beacon, byte-identical.
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.Header.Set("Accept", "*/*")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	if got := string(body); got != "console.log(1)" {
		t.Errorf("asset altered in transit: %q", got)
	}
	if strings.Contains(string(body), "__prereview") {
		t.Errorf("beacon must not be injected into non-HTML assets")
	}
}

func TestExternalProxyUnreachableTarget(t *testing.T) {
	// Nothing is listening here; the proxy should answer 502, not panic/hang.
	target, _ := url.Parse("http://127.0.0.1:1") // port 1: reliably refused
	proxy := newExternalProxy(target)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Result().StatusCode != http.StatusBadGateway {
		t.Errorf("unreachable target: want 502, got %d", rec.Result().StatusCode)
	}
}

func TestRunExternalValidation(t *testing.T) {
	cases := []struct {
		name, extURL, out string
	}{
		{"not a url", "::::not-a-url", t.TempDir()},
		{"non-http scheme", "ftp://localhost:8080", t.TempDir()},
		{"missing host", "http://", t.TempDir()},
		{"missing out dir", "http://localhost:8080", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// All cases fail validation before any listener is bound.
			if err := runExternal(tc.extURL, tc.out, "127.0.0.1", false, 0, false); err == nil {
				t.Errorf("expected validation error for %s", tc.name)
			}
		})
	}
}
