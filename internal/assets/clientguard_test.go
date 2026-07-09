package assets

import (
	"strings"
	"testing"
)

// TestVendoredClientHasBlockTextSelect guards against a client re-vendor silently
// dropping rendered-surface (data-surface="block") text-select support.
//
// That is exactly the regression this test was born from: the block-surface
// text-select logic lived on an un-merged client branch, was vendored in when
// rendered-Markdown comments shipped, then lost on a later re-vendor from a
// client `main` that never had it. With it gone, selecting a phrase in the
// Markdown Preview view produced no Comment button (textOffsetsFromSelection
// returned null on `.md-view`). The browser e2e (TestE2E_TextSelectRenderedMarkdown)
// catches it, but browser e2e are skipped in CI — so nothing gated the re-vendor.
//
// This is a plain `go test`, so it runs in CI. The sentinel is the exact minified
// comparison that gates the block path; region-select's own data-surface handling
// only ever compares to "html"/"code"/"page", so this substring is unique to
// text-select's block surface. If a future minifier change alters the spelling,
// this may fail even though block support is present — that's the safe direction:
// a human re-checks and updates the sentinel rather than the regression slipping
// through silently.
func TestVendoredClientHasBlockTextSelect(t *testing.T) {
	const sentinel = `data-surface")==="block"`
	if !strings.Contains(string(clientJS), sentinel) {
		t.Fatalf("vendored client is missing block-surface text-select support "+
			"(sentinel %q not found in the client bundle).\n"+
			"A `make sync-client` likely pulled a client build without the "+
			"data-surface=\"block\" branch. Rebuild the client from a main that "+
			"includes it (>= v0.16.3) and re-run `make sync-client`.", sentinel)
	}
}
