# prereview — repo notes

## Template layout (`templates/`)

The page template is split into single-responsibility files under `templates/`,
parsed together by livetemplate (`stageTemplates` in `store.go` →
`WithParseFiles`, ordered by `templateOrder` in `main.go`):

- **`page.tmpl`** — the page shell and all rendered views. It is the **main**
  template (its top-level markup becomes the `"prereview"` entry); it must be
  parsed first.
- **`partials.tmpl`** — `{{define}}`-only: the reusable comment/region render
  partials (`composer`, `blockComments`, `commentCard*`, `deleteDialog*`,
  `regionToggle`).
- **`icons.tmpl`** — `{{define}}`-only: the SVG icon partials.

Partials files must contain **only** `{{define}}` blocks + comments/whitespace —
any top-level rendered text there would clobber `page.tmpl`'s body (Go's "only
one Parse call may carry body text" rule).

## Editing the templates (read before you touch them)

The `templates/` files are **whitespace-significant**. livetemplate emits each
template's static text **verbatim** into the HTTP response (no minification), so
any space or newline you add or remove between markup lands directly in the DOM
the browser parses. Significant zones:

- `white-space: pre-wrap` on `.content` / `.chroma` — the diff code
- `<textarea>` body content
- the diff gutter's trailing space
- single spaces between inline elements (`</code> <small>`, `file{{end}} <code>`)
- Go-template **conditional attributes**: `class="code{{if …}} pure-add{{end}}"`

### Do NOT run a reflow formatter on these files

prettier and djlint both **corrupt** it (proven empirically, repeatedly): they
reflow pre-wrap/textarea content, delete significant inter-element spaces, and
inject newlines into conditional attribute values. `prettier-plugin-go-template`
reflows the HTML underneath and hits the same wall; `htmlWhitespaceSensitivity:
strict` stops the reflow but then formats nothing. **Hand-format only.**

### After ANY edit, run the output-equivalence guard

```sh
make tmpl-check        # = go test -run TestTemplateOutputSignature -count=1 .
```

It concatenates the `templates/` files (in `templateOrder`) and computes a
rendering-equivalent signature, diffing it against a committed golden
(`testdata/prereview.tmpl.sig.golden`):

- **Green** → your edit produced **DOM-identical** output. Safe reformat.
- **Red** → your edit changed what the browser renders. If you only reformatted,
  that's a **corruption** — revert it. If it was an intentional **content** change,
  regenerate the golden and commit it in the same change:

  ```sh
  go test -run TestTemplateOutputSignature -update-sig .
  ```

The guard runs in CI too (it's a normal `go test`, picked up by `go test ./...`),
so a corrupting reformat fails the build — the note above is the convenience, CI is
the enforcement.

### What's free vs. what needs care when reformatting

- **Splitting attributes onto their own lines is free** — between-attribute
  whitespace is intra-tag and HTML always ignores it; the guard ignores it too.
- **Wrapping a template action across lines is free** — `{{if and (a) (b)}}` split
  over several source lines canonicalizes identically. No trim markers needed.
- **Breaking around literal TEXT needs `{{- -}}` trim markers** to stay output-
  identical (e.g. a long conditional-class ladder). Get it wrong and the guard
  catches it.
- **Reformatting inside a quoted attribute value** (e.g. a long `style="…"`) trips
  the guard even when it's DOM-safe — deliberate, so there's never a false green.
  Use `-update-sig` if the change is genuinely intentional.

There is no auto-formatter and there won't be one: reflow can't be made safe for
this file, and a narrow attribute-only splitter doesn't earn its keep now that the
file is formatted and the guard exists.

## Theming: `data-scheme` / `data-mode` (read before touching colors)

The whole UI is re-skinned by two attributes on the **`.theme-root` wrapper**
(`page.tmpl`, inside `<body>`), NOT on `<html>`:

- **`data-scheme`** — the coordinated color scheme (`solarized` today; gruvbox/
  catppuccin attach to the same `[data-scheme]` CSS later).
- **`data-mode`** — Light/Dark/System (`""` = System; see `state.ThemeMode` /
  `DataMode()`). Omitted for System so `@media (prefers-color-scheme)` drives it.

Three non-obvious facts that took a while to get right (don't regress them):

1. **Why `.theme-root`, not `<html>`.** livetemplate morphs its managed subtree,
   not `<html>`'s attributes — a `data-mode` on `<html>` only changes on a full
   reload. On the wrapper it updates live. The wrapper is `display:contents`
   (no box) so the flex/scroll chain is unchanged.
2. **No specificity hack needed.** Pico sets its `--pico-*` on `:root` under
   `:root:not([data-theme=dark])` = (0,2,0); a bare `[data-scheme]` is (0,1,0)
   and would LOSE (this was a silent P2 bug — chrome rendered Pico-white). But a
   custom property set on a *descendant* (the wrapper) wins for that subtree by
   inheritance PROXIMITY regardless of specificity, so the wrapper's tokens beat
   Pico's `:root` values for all content. `<body>` is an *ancestor* of the
   wrapper, so it does NOT theme — `.layout` is painted `var(--surface)` to
   cover it. `<html data-theme="light">` stays pinned so Pico's own dark palette
   never activates.
3. **Dark tokens are duplicated on purpose.** The Solarized-dark block appears
   twice in `prereview.css` — once `[data-scheme="solarized"][data-mode="dark"]`
   and once inside `@media (prefers-color-scheme: dark)` for no-JS System mode.
   They can't be merged (one is in a media query); a `KEEP IN SYNC` comment
   marks them. Lightness-encoding `--pico-*` vars (heading/form/code text) are
   mapped to semantic tokens in the light block so they auto-flip in dark.

Syntax colors live in `/syntax.css` (`gitdiff/highlight.go`), which carries BOTH
modes: the light chroma block unscoped, the dark block scoped (`scopeSyntax`)
under the same `[data-mode]` selectors — so a fence/diff recolors with no
refetch. Markdown fences are class-based (`WithClasses(true)`) for the same
reason; inline-styled fences can't recolor for dark.
