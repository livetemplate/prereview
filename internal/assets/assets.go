// Package assets embeds the livetemplate client browser bundle (JS + CSS)
// so prereview ships as a self-contained binary with no CDN dependency.
//
// The files in client/ are populated by `make sync-client`, which copies
// them from ../../../client/{dist/livetemplate-client.browser.js,
// livetemplate.css}. They are gitignored — fresh clones must run
// `make sync-client` before `go build`.
package assets

import (
	_ "embed"
)

//go:embed client/livetemplate-client.browser.js
var clientJS []byte

//go:embed client/livetemplate.css
var clientCSS []byte

// ClientJS returns the embedded livetemplate client browser bundle.
func ClientJS() []byte { return clientJS }

// ClientCSS returns the embedded livetemplate shared stylesheet.
func ClientCSS() []byte { return clientCSS }
