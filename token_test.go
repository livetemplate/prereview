package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

type guardCase struct {
	name     string
	method   string
	target   string
	cookie   *http.Cookie
	want     int
	body     string // the wrapped handler's answer, when want is 200
	location string // when want is 303
}

func serveCase(t *testing.T, h http.Handler, tc guardCase) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(tc.method, "http://127.0.0.1:4799/", nil)
	req.URL, _ = url.ParseRequestURI(tc.target)
	req.RequestURI = tc.target
	if tc.cookie != nil {
		req.AddCookie(tc.cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != tc.want {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
	}
	if tc.want == 200 && tc.body != "" && rec.Body.String() != tc.body {
		t.Fatalf("body = %q, want %q", rec.Body.String(), tc.body)
	}
	return rec
}

func TestLaunchTokenGuard(t *testing.T) {
	tok, other := newLaunchToken(), newLaunchToken()
	h := tok.guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("served"))
	}))
	good := &http.Cookie{Name: tok.cookie, Value: tok.value}

	for _, tc := range []guardCase{
		{name: "no token", method: "GET", target: "/", want: 403},
		{name: "cookie", method: "GET", target: "/x.png", cookie: good, want: 200, body: "served"},
		{name: "cookie on a POST", method: "POST", target: "/", cookie: good, want: 200, body: "served"},
		{name: "wrong cookie", method: "GET", target: "/", cookie: &http.Cookie{Name: tok.cookie, Value: other.value}, want: 403},
		{name: "another server's cookie", method: "GET", target: "/", cookie: &http.Cookie{Name: other.cookie, Value: other.value}, want: 403},
		{name: "wrong query token", method: "GET", target: "/?token=" + other.value, want: 403},
		{name: "query token on a POST", method: "POST", target: "/?token=" + tok.value, want: 403},
		{name: "query token", method: "GET", target: "/?token=" + tok.value, want: 303, location: "/"},
		{name: "query token keeps the rest", method: "GET", target: "/a/b.html?x=1&token=" + tok.value, want: 303, location: "/a/b.html?x=1"},
		{name: "query token stays on site", method: "GET", target: "//evil.example/x?token=" + tok.value, want: 303, location: "/evil.example/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveCase(t, h, tc)
			if tc.want != 303 {
				if rec.Header().Get("Set-Cookie") != "" {
					t.Fatalf("set a cookie on a %d", tc.want)
				}
				return
			}
			if got := rec.Header().Get("Location"); got != tc.location {
				t.Fatalf("Location = %q, want %q", got, tc.location)
			}
			c := rec.Result().Cookies()
			if len(c) != 1 || c[0].Name != tok.cookie || c[0].Value != tok.value || !c[0].HttpOnly ||
				c[0].SameSite != http.SameSiteStrictMode || c[0].Path != "/" {
				t.Fatalf("cookie = %+v, want %s=<token> HttpOnly SameSite=Strict Path=/", c, tok.cookie)
			}
		})
	}
}

// The files key reads files and nothing else: a GET under it reaches the files
// handler with the prefix stripped, and no other secret stands in for the key.
func TestLaunchTokenFilesHandler(t *testing.T) {
	tok, other := newLaunchToken(), newLaunchToken()
	h := tok.filesHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("file " + r.URL.Path))
	}))
	fb := tok.filesBase()
	for _, tc := range []guardCase{
		{name: "files key", method: "GET", target: fb + "/docs/a.css", want: 200, body: "file /docs/a.css"},
		{name: "files key at the root", method: "GET", target: fb + "/", want: 200, body: "file /"},
		{name: "wrong key", method: "GET", target: other.filesBase() + "/docs/a.css", want: 403},
		{name: "the launch token is not the key", method: "GET", target: filesRoute + tok.value + "/a.css", want: 403},
		{name: "a cookie is not the key", method: "GET", target: filesRoute + "x/a.css", cookie: &http.Cookie{Name: tok.cookie, Value: tok.value}, want: 403},
		{name: "POST", method: "POST", target: fb + "/docs/a.css", want: 403},
	} {
		t.Run(tc.name, func(t *testing.T) { serveCase(t, h, tc) })
	}
}

// guardedHandler leaves the embedded assets and serverPIDRoute open, sends the
// files route to the files handler, and puts everything else behind the token.
// With no files handler the files route is the page's, so it needs the token.
func TestGuardedHandler(t *testing.T) {
	tok := newLaunchToken()
	page := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("page")) })
	files := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("file " + r.URL.Path)) })
	repo, external := guardedHandler(tok, page, files), guardedHandler(tok, page, nil)
	for _, tc := range []struct {
		h http.Handler
		guardCase
	}{
		{repo, guardCase{name: "asset", method: "GET", target: "/prereview.css", want: 200}},
		{repo, guardCase{name: "pid", method: "GET", target: serverPIDRoute, want: 200}},
		{repo, guardCase{name: "usage needs the token", method: "GET", target: "/_usage", want: 403}},
		{repo, guardCase{name: "page needs the token", method: "GET", target: "/", want: 403}},
		{repo, guardCase{name: "page with the cookie", method: "GET", target: "/", cookie: &http.Cookie{Name: tok.cookie, Value: tok.value}, want: 200, body: "page"}},
		{repo, guardCase{name: "files", method: "GET", target: tok.filesBase() + "/a.css", want: 200, body: "file /a.css"}},
		{external, guardCase{name: "no files route without a repo", method: "GET", target: tok.filesBase() + "/a.css", want: 403}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveCase(t, tc.h, tc.guardCase)
			if rec.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatalf("no Referrer-Policy on %s", tc.target)
			}
		})
	}
}

func TestLaunchTokenURL(t *testing.T) {
	tok := newLaunchToken()
	for base, want := range map[string]string{
		"http://127.0.0.1:4799":        "http://127.0.0.1:4799/?token=" + tok.value,
		"https://vm.exe.xyz:4799/":     "https://vm.exe.xyz:4799/?token=" + tok.value,
		"https://host.example/review/": "https://host.example/review/?token=" + tok.value,
	} {
		if got := tok.url(base); got != want {
			t.Errorf("url(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestLaunchTokensDiffer(t *testing.T) {
	a, b := newLaunchToken(), newLaunchToken()
	if a.value == b.value || a.cookie == b.cookie || a.files == b.files || a.files == a.value {
		t.Fatalf("launches share a key or cookie name: %+v %+v", a, b)
	}
	if len(a.value) != 43 || strings.ContainsAny(a.value, "+/=") {
		t.Fatalf("token %q is not 32 bytes of base64url", a.value)
	}
}

// A world-readable stdout file becomes owner-only; a pipe is left alone.
func TestPrivateOutput(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Chmod(0o644); err != nil {
		t.Fatal(err)
	}
	if err := privateOutput(f); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.Stat(); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := privateOutput(w); err != nil {
		t.Fatalf("pipe: %v", err)
	}
}
