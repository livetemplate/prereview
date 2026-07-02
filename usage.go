package main

import (
	"bytes"
	_ "embed"
	"html/template"

	"github.com/livetemplate/prereview/gitdiff"
)

// usageMD is the curated in-app usage guide, rendered at /_usage. It is
// first-party content embedded into the binary — never a file from the
// reviewed working directory — so the page can never be shadowed by a repo
// file (and the route is served from an exact ServeMux pattern that outranks
// the SPA catch-all; see registerAssetRoutes in server.go).
//
//go:embed docs/usage.md
var usageMD string

// usagePage is the fully-rendered /_usage HTML, built once at startup. The doc
// is embedded and immutable, so there's nothing to recompute per request.
var usagePage = buildUsagePage()

// usageShell wraps the rendered Markdown in a standalone, themed page. It
// mirrors the SPA's head (same pico/syntax/prereview stylesheets) and the
// .theme-root wrapper so the doc is skinned by the exact same tokens as the
// rest of the app; because no data-mode is set, Light/Dark follows the OS via
// prereview.css's @media (prefers-color-scheme) blocks (a static page has no
// live toggle). The page-layout CSS (centered column, painted surface) lives
// here rather than in the shared prereview.css — it's specific to this one
// page and would otherwise be dead weight in the stylesheet the SPA loads.
var usageShell = template.Must(template.New("usage").Parse(`<!DOCTYPE html>
<html lang="en" data-theme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>prereview · Usage</title>
<link rel="stylesheet" href="/pico.min.css">
<link rel="stylesheet" href="/syntax.css">
<link rel="stylesheet" href="/prereview.css">
<style>
/* .theme-root is display:contents (prereview.css), so .usage-page becomes the
   painted box; body is a 100dvh flex column, so flex:1 fills it. */
.usage-page { flex: 1; min-height: 100dvh; overflow-y: auto;
  background: var(--surface); color: var(--text); font-family: var(--sans); }
.usage-main { max-width: 46rem; margin: 0 auto; padding: 2.5rem 1.25rem 4rem; }
.usage-header { margin: 0 0 1.5rem; font-size: 0.85rem; color: var(--text-muted); }
.usage-header a { color: var(--text-muted); }
/* .md-rendered carries the review view's hover/cursor affordances; neutralize
   them here — this doc is read-only, not commentable. */
.usage-page .md-rendered { cursor: default; padding: 0; border: 0; }
.usage-page .md-rendered:hover { background: none; border-color: transparent; }
</style>
</head>
<body>
<div class="theme-root" data-scheme="solarized">
<div class="usage-page">
<main class="usage-main">
<p class="usage-header"><a href="/">← Back to review</a></p>
<article class="md-rendered">
{{.}}
</article>
</main>
</div>
</div>
</body>
</html>
`))

// buildUsagePage renders docs/usage.md into the standalone page bytes.
func buildUsagePage() []byte {
	body := gitdiff.RenderMarkdownDoc([]byte(usageMD))
	var buf bytes.Buffer
	// The shell template is static and the body is already-safe HTML from the
	// goldmark safe-mode renderer; execution cannot fail on valid input, so a
	// render error would be a programming bug, not a runtime condition.
	if err := usageShell.Execute(&buf, body); err != nil {
		panic("prereview: rendering usage page: " + err.Error())
	}
	return buf.Bytes()
}
