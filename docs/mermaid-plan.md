# Plan: Mermaid diagram rendering (#24)

Render ` ```mermaid ` fenced code blocks in the rendered-Markdown view as
diagrams, while keeping each diagram a single per-source-line **commentable**
`MarkdownBlock` (the issue's "should support comments" requirement).

## Why this shape (decisions, locked)

- **Client-side mermaid.js, embedded — not server-side SVG.** Server-side
  rendering needs a runtime headless browser (`chromedp` is dev-only today),
  which breaks the single-binary / offline-over-Tailscale promise. Pure-Go
  mermaid renderers don't exist. mermaid.js is embedded via `embed.FS` (no
  CDN — reviews run offline). This is the *first* client-rendered element in
  an otherwise fully server-rendered markdown pipeline.
- **In-place, not iframe.** The comment/region overlay is drawn in the PARENT
  DOM and cannot reach into a sandboxed iframe (see `prereview.tmpl` HTML-preview
  overlay note). A per-diagram iframe would make commenting *harder*. In-place
  SVG in the parent DOM keeps the overlay working.
- **`lvt-ignore` protects the injected SVG.** livetemplate's morphdom skips an
  `lvt-ignore` element + subtree, so server patches (e.g. adding a comment)
  never clobber mermaid's client-injected SVG.
- **`lvt:updated` re-runs mermaid after SPA navigation.** prereview swaps files
  without a full reload, so a one-shot `DOMContentLoaded` would never fire on
  file→file nav. The init script sweeps on initial load + on each `lvt:updated`.
- **Failure fallback = raw code + error note** (user decision). On a mermaid
  parse error the block reveals the syntax-highlighted source fence plus a small
  error banner. The reviewer keeps the source lines visible and commentable.
- **Security: `securityLevel:'strict'`, `htmlLabels:false`.** This is the one
  place we execute JS over untrusted repo content; the rest of the pipeline
  refuses unsafe HTML, so match that posture.

## Architecture

```
 ```mermaid fence  ──parse──▶  RenderMarkdownBlocks (gitdiff/markdown.go)
                                   │  KindFencedCodeBlock + lang=="mermaid"
                                   ▼
        <div class="md-mermaid" lvt-ignore data-...>      ← ONE MarkdownBlock,
          <pre class="mermaid">{escaped def}</pre>           anchored to fence
        </div>                                               source lines
                                   │ served in page
                                   ▼
   prereview.tmpl init script: on load + lvt:updated, lazy-load
   /mermaid.min.js, render each unprocessed pre.mermaid → SVG (in place).
   On parse error → add .md-mermaid-failed + banner, keep raw text.
```

## Progress tracker

### Phase 1 (Go) — emit the mermaid block  ✅
- [x] **Audit**: confirmed `KindFencedCodeBlock` hits `default → renderNode`; `fcb.Language(src)` returns `"mermaid"`, `fcb.Lines()` gives the raw definition.
- [x] `gitdiff/markdown.go`: added `case ast.KindFencedCodeBlock` — mermaid → `renderMermaidBlock`, else chroma (unchanged). `gitdiff/mermaid.go` holds the local helpers (`isMermaidFence`, `fencedCodeRaw`, `renderMermaidBlock`).
- [x] Unit tests in `gitdiff/mermaid_test.go`: single block; `lvt-ignore`; `pre.mermaid`; def preserved & HTML-escaped (XSS); source-line span; case/info-string variants; `go` fence regression. **All green.**

### Phase 2 (Client + assets) — render + fallback  ✅
- [x] Embedded `internal/assets/mermaid.min.js` (v11.15.0, committed); `assets.MermaidJS()`; served at `/mermaid.min.js`.
- [x] Renderer lives in `internal/assets/mermaid-init.js`, served at `/mermaid-init.js`, referenced via `<script defer src>` — **NOT inlined** (an inline `<script>` in the livetemplate template corrupts client hydration and breaks event delegation; see Learn). Lazy-loads mermaid, sweeps on load + `lvt:updated`, `securityLevel:'strict'`/`htmlLabels:false`/`suppressErrorRendering:true`, fallback = raw code + banner.
- [x] CSS: `.md-mermaid`, centered SVG, `.md-mermaid-failed` banner, raw-text fallback.
- [x] E2E `e2e_mermaid_test.go`: valid fence → SVG; comment round-trips to CSV on lines 6-7; SVG survives the comment's server re-render (lvt-ignore proof); broken fence → banner + raw text; no console errors. **Green (5.9s).**

### Learn (surprises)
- **An inline `<script>` in a livetemplate template breaks the SPA.** livetemplate ships the template's static/dynamic tree to the client; a large inline script becomes template content and corrupts hydration → event delegation dies (SSR still renders, so the page *looks* fine but every click is dead). Fix: serve the script as a standalone embedded file via `src=`, like `livetemplate-client.js`.
- **Commenting needs the file in the changeset.** `selectBlock` anchors to diff lines, so the e2e fixture must commit a base then edit (untracked files render but aren't commentable). The rendering-only tests used untracked files, which misled the first fixture.
- jsdelivr's mermaid@11 `dist/mermaid.min.js` sets `globalThis.mermaid` and has zero dynamic `import()` — fully offline-safe.

## Risks
- mermaid v11 `dist/mermaid.min.js` lazy-loads some exotic diagram modules (mindmap, etc.) via dynamic import — those may fail offline. Core diagrams (flowchart/sequence/class/state/er/gantt/pie) are bundled. Acceptable for v1; note in docs.
- ~3.3 MB added to the binary (lazy-loaded in the browser, only fetched when a page has a mermaid fence).
