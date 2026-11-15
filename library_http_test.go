package main

import (
	"net/http/httptest"
	"testing"
)

// The library review route starts a process, so a cross-site page (an <img>, a
// link on another origin) must not be able to trigger it, nor a page on a domain
// that DNS-rebinds to this machine (its requests carry that domain as the Host).
func TestLibraryReview_SameOriginOnly(t *testing.T) {
	for _, c := range []struct {
		site, referer string
		ok            bool
	}{
		{"same-origin", "", true},
		{"none", "", true}, // typed or bookmarked
		{"cross-site", "", false},
		{"same-site", "", false},
		{"", "http://127.0.0.1:4000/", true}, // no Fetch Metadata: a same-host Referer
		{"", "http://evil.example/", false},
		{"", "", false},
	} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:4000/_library-review", nil)
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		if c.referer != "" {
			r.Header.Set("Referer", c.referer)
		}
		if got := sameOriginNavigation(r); got != c.ok {
			t.Errorf("Sec-Fetch-Site %q, Referer %q: allowed = %v, want %v", c.site, c.referer, got, c.ok)
		}
		w := httptest.NewRecorder()
		if !c.ok {
			libraryReviewHandler(t.TempDir())(w, r)
			if w.Code != 403 {
				t.Errorf("Sec-Fetch-Site %q: status %d, want 403", c.site, w.Code)
			}
		}
	}
}

func TestTrustedHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:4000":                 true,
		"localhost:4000":                 true,
		"[::1]:4000":                     true,
		"devbox.tail262784.ts.net:4000":  true,
		"evil.example:4000":              false, // rebinding: resolves here, isn't us
		"10.0.0.5:4000":                  false, // an IP that isn't the one we were reached on
		"devbox.tail262784.ts.net.:4000": true,
	} {
		r := httptest.NewRequest("GET", "http://"+host+"/_library-review", nil)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if got := trustedHost(r); got != want {
			t.Errorf("Host %s: trusted = %v, want %v", host, got, want)
		}
		if got := sameOriginNavigation(r); got != want {
			t.Errorf("Host %s: same-origin navigation allowed = %v, want %v", host, got, want)
		}
	}
}
