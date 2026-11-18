package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"

	"github.com/livetemplate/prereview/internal/proxy"
)

// tokenParam is the query parameter the printed URLs carry the launch token in.
const tokenParam = "token"

// filesRoute is where the HTML preview loads repo files, as
// filesRoute + <files key> + "/" + <repo path>.
const filesRoute = "/_files/"

// launchToken is the per-launch secret a browser must present: in the query of
// the URL prereview printed, then in a cookie. The cookie is named after the
// token because cookies ignore ports, so every server on one host shares a
// cookie jar; one fixed name would let a second review's cookie evict the
// first's.
//
// files is a second key that only reads repo files. The HTML preview's iframe is
// an opaque origin, so the browser sends it no cookie; its <base> carries this
// key instead. The repo's own scripts in the preview can read it, and it gives
// them nothing they can't already reach.
type launchToken struct {
	value  string
	cookie string
	files  string
}

func newLaunchToken() launchToken {
	v := randomKey()
	sum := sha256.Sum256([]byte(v))
	return launchToken{value: v, cookie: proxy.ServerCookiePrefix + hex.EncodeToString(sum[:6]), files: randomKey()}
}

// randomKey is 32 random bytes, base64url. crypto/rand.Read never fails.
func randomKey() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// filesBase is the prefix the HTML preview loads repo files under.
func (t launchToken) filesBase() string { return filesRoute + t.files }

// equal compares secrets in constant time.
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func readOnly(r *http.Request) bool { return r.Method == http.MethodGet || r.Method == http.MethodHead }

// url returns base (a printed server URL) with the token in its query.
func (t launchToken) url(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	if u.Path == "" {
		u.Path = "/"
	}
	q := u.Query()
	q.Set(tokenParam, t.value)
	u.RawQuery = q.Encode()
	return u.String()
}

// guard admits a request that carries the token, by cookie or (for a GET or
// HEAD) in the query, and refuses everything else with 403. A query token is
// swapped for the cookie with a 303 to the same URL minus the token, so the
// token leaves the address bar at once.
func (t launchToken) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(t.cookie); err == nil && equal(c.Value, t.value) {
			next.ServeHTTP(w, r)
			return
		}
		if q := r.URL.Query(); readOnly(r) && equal(q.Get(tokenParam), t.value) {
			http.SetCookie(w, &http.Cookie{
				Name:     t.cookie,
				Value:    t.value,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			q.Del(tokenParam)
			// One leading slash: "//host/x" would redirect off-site.
			dest := "/" + strings.TrimLeft(r.URL.EscapedPath(), "/")
			if rest := q.Encode(); rest != "" {
				dest += "?" + rest
			}
			http.Redirect(w, r, dest, http.StatusSeeOther)
			return
		}
		http.Error(w, "Forbidden: open the URL prereview printed; it carries this session's token.", http.StatusForbidden)
	})
}

// filesHandler serves filesRoute: a GET or HEAD of <files key>/<path> goes to
// files as /<path>. Mount it with files reading repo files and nothing else.
func (t launchToken) filesHandler(files http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, filesRoute), "/")
		if !readOnly(r) || !equal(key, t.files) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		r2 := new(http.Request)
		*r2 = *r
		u := *r.URL
		u.Path, u.RawPath = "/"+path, ""
		r2.URL = &u
		files.ServeHTTP(w, r2)
	})
}

// noReferrer keeps any URL (and so a token in one) out of the Referer header
// of requests the page makes.
func noReferrer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
