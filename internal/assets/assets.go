// Package assets embeds the livetemplate client browser bundle (JS + CSS)
// and the JetBrains Mono webfont so prereview ships as a self-contained
// binary with no CDN dependency (it is reviewed over Tailscale/devbox,
// often offline).
//
// The files in client/ are populated by `make sync-client`, which copies
// them from ../../../client/{dist/livetemplate-client.browser.js,
// livetemplate.css}. They are gitignored — fresh clones must run
// `make sync-client` before `go build`.
//
// fonts/ holds the static Regular (400) and Bold (700) JetBrains Mono
// woff2 (SIL OFL 1.1, see fonts/OFL.txt) — prose needs only those two
// weights; other weights/italics synthesize. These ARE committed.
package assets

import (
	_ "embed"
)

//go:embed client/livetemplate-client.browser.js
var clientJS []byte

//go:embed client/livetemplate.css
var clientCSS []byte

//go:embed fonts/JetBrainsMono-Regular.woff2
var fontRegular []byte

//go:embed fonts/JetBrainsMono-Bold.woff2
var fontBold []byte

// ClientJS returns the embedded livetemplate client browser bundle.
func ClientJS() []byte { return clientJS }

// ClientCSS returns the embedded livetemplate shared stylesheet.
func ClientCSS() []byte { return clientCSS }

// FontRegular returns the embedded JetBrains Mono Regular (400) woff2.
func FontRegular() []byte { return fontRegular }

// FontBold returns the embedded JetBrains Mono Bold (700) woff2.
func FontBold() []byte { return fontBold }
