package main

import (
	"strings"
	"testing"
)

// TestUsagePageShell pins the standalone /_usage page bytes without a browser:
// the curated doc renders into a page that carries the theming wrapper, the
// same stylesheets as the SPA, and the doc's own content. The full e2e
// (TestE2E_UsagePage) covers live rendering; this is the fast guard so a
// regression in the shell or the embed doesn't depend on chromium to surface.
func TestUsagePageShell(t *testing.T) {
	got := string(usagePage)

	// Themed by the same .theme-root tokens as the rest of the app.
	if !strings.Contains(got, `class="theme-root" data-scheme=`) {
		t.Error("usage page missing the themed .theme-root wrapper")
	}
	// Same stylesheets the SPA head loads, so tokens + syntax colors apply.
	for _, css := range []string{"/pico.min.css", "/syntax.css", "/prereview.css"} {
		if !strings.Contains(got, css) {
			t.Errorf("usage page missing stylesheet %q", css)
		}
	}
	// The curated guide's own content rendered through the Markdown pipeline.
	if !strings.Contains(got, "Using prereview") {
		t.Error("usage page missing the rendered guide heading")
	}
	if !strings.Contains(got, `class="md-rendered"`) {
		t.Error("usage page missing the .md-rendered container")
	}
}
