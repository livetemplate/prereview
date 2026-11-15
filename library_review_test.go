package main

import (
	"net/http/httptest"
	"testing"
)

// The library review route starts a process, so a cross-site page (an <img>, a
// link on another origin) must not be able to trigger it.
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
