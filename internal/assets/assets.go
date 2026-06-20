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
//
// mermaid.min.js is the pinned mermaid UMD bundle, committed for the same
// offline reason — the only third-party JS we ship besides the livetemplate
// client.
package assets

import (
	_ "embed"
)

//go:embed client/livetemplate-client.browser.js
var clientJS []byte

//go:embed client/livetemplate.css
var clientCSS []byte

// picoCSS is the pinned Pico CSS v2.1.1 minified stylesheet (MIT). It was
// previously loaded from a jsdelivr CDN <link>, which silently broke the
// styling when prereview runs offline (its primary mode — reviewed over
// Tailscale/devbox). Vendored + embedded so the binary stays truly
// self-contained, matching the JetBrains Mono fonts and mermaid bundle.
//
//go:embed pico.min.css
var picoCSS []byte

// prereviewCSS is prereview's own stylesheet. It was previously a large inline
// <style> block in prereview.tmpl; extracted to a served asset so the template
// shows its HTML structure rather than ~1340 lines of CSS. Embedded (not a CDN
// link) so the binary stays self-contained and offline-safe, matching pico.
//
//go:embed prereview.css
var prereviewCSS []byte

//go:embed fonts/JetBrainsMono-Regular.woff2
var fontRegular []byte

//go:embed fonts/JetBrainsMono-Bold.woff2
var fontBold []byte

// mermaidJS is the pinned mermaid UMD bundle (v11.15.0). It is self-contained
// (no dynamic import()), so every bundled diagram type renders offline. The
// browser fetches it lazily — only when a page contains a ```mermaid fence.
//
//go:embed mermaid.min.js
var mermaidJS []byte

// mermaidInitJS is prereview's own loader: it lazy-fetches mermaidJS when a
// page has a diagram and renders each fence to SVG. Kept a standalone file
// (not inlined in prereview.tmpl) so it stays out of livetemplate's template
// tree.
//
//go:embed mermaid-init.js
var mermaidInitJS []byte

// ClientJS returns the embedded livetemplate client browser bundle.
func ClientJS() []byte { return clientJS }

// ClientCSS returns the embedded livetemplate shared stylesheet.
func ClientCSS() []byte { return clientCSS }

// PicoCSS returns the embedded Pico CSS v2.1.1 stylesheet.
func PicoCSS() []byte { return picoCSS }

// PrereviewCSS returns prereview's own stylesheet (extracted from the template).
func PrereviewCSS() []byte { return prereviewCSS }

// FontRegular returns the embedded JetBrains Mono Regular (400) woff2.
func FontRegular() []byte { return fontRegular }

// FontBold returns the embedded JetBrains Mono Bold (700) woff2.
func FontBold() []byte { return fontBold }

// MermaidJS returns the embedded mermaid UMD bundle.
func MermaidJS() []byte { return mermaidJS }

// MermaidInitJS returns prereview's mermaid loader/renderer script.
func MermaidInitJS() []byte { return mermaidInitJS }
