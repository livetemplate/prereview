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

The whole UI is re-skinned by two **orthogonal** attributes on the
**`.theme-root` wrapper** (`page.tmpl`, inside `<body>`), NOT on `<html>`:

- **`data-scheme`** — the coordinated color scheme. Three ship today —
  `solarized` (default) / `gruvbox` / `catppuccin` — cycled by the toolbar
  palette button (`cycleScheme` → `state.CycleScheme`). The registry is
  `gitdiff.Schemes` (one row = Name + Label + the two chroma styles); the state
  helpers (`DataScheme`/`NextScheme`/`SchemeLabel`) loop it, so **adding a
  scheme = one registry row + one CSS token block** (light + dark + @media).
- **`data-mode`** — Light/Dark/System (`""` = System; see `state.ThemeMode` /
  `DataMode()`, cycled by `cycleTheme`). Omitted for System so `@media
  (prefers-color-scheme)` drives it.

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
3. **Dark tokens are duplicated on purpose.** Each scheme's dark block appears
   twice in `prereview.css` — once `[data-scheme="x"][data-mode="dark"]` and
   once inside `@media (prefers-color-scheme: dark)` for no-JS System mode.
   They can't be merged (one is in a media query); a `KEEP IN SYNC` comment
   marks them. Lightness-encoding `--pico-*` vars (heading/form/code text) are
   mapped to semantic tokens in **the light block** (which always matches) so
   they auto-flip when dark redefines those semantic tokens — forget them in a
   new scheme's light block and dark renders dark-on-dark headings/inputs.

Two more per-scheme gotchas when adding a scheme (gruvbox/catppuccin are the
worked examples):

- **`--pico-primary-inverse` (filled-button label) flips per scheme×mode.** If a
  scheme's *dark* accent is light (gruvbox `#83a598`, catppuccin `#cba6f7`), its
  dark block needs a *dark* inverse (`#282828` / `#1e1e2e`) or the Save/Hand-off
  label goes invisible. `TestE2E_SchemePicker` asserts this per scheme — don't
  remove that check. Light blocks can keep `var(--surface)` (both new light
  accents are dark).
- **Light-mode component colors fall through to global GitHub-pastel defaults**
  (status badges, diff-stats, md-alerts further down the file). New schemes only
  re-skin them in their **dark** block — wherever the dark color already lives in
  a semantic token (`--pr-add`/`--pr-del`/`--accent`) reference the token via
  `color-mix(...)` rather than re-hardcoding; only genuinely-distinct status hues
  (yellow/violet/magenta + the 5 alert accents) are literals. Solarized predates
  this and hardcodes its dark hex — leave it alone (it's screenshot-guarded).

Syntax colors live in `/syntax.css` (`gitdiff/highlight.go`), which carries BOTH
modes: the light chroma block unscoped, the dark block scoped (`scopeSyntax`)
under the same `[data-mode]` selectors — so a fence/diff recolors with no
refetch. Markdown fences are class-based (`WithClasses(true)`) for the same
reason; inline-styled fences can't recolor for dark.

## Text (character-range) comments — `kind=text`

Beyond whole-line comments, a reviewer can select a **word / phrase / multi-line
span** and comment on exactly those characters (mouse drag, iOS long-press, or
keyboard). Anatomy:

- **Model.** `Comment.FromCol/ToCol` (0-based, half-open, **rune** offsets) +
  `Kind="text"` + `CommentAnchor.Snippet` (the exact selected substring, stored
  in the opaque `anchor` JSON — no CSV column). CSV gains `from_col`/`to_col`
  (reader is length-tolerant, 7→15 cols).
- **The load-bearing invariant** (`gitdiff/textcontent_test.go`): a rendered
  line's `.content` `textContent` **equals the raw line rune-for-rune**. Offsets
  are stored in raw-line coords but computed in the browser from the DOM; they
  only agree because of this. Offsets are **rune counts** (client `[...s].length`,
  server `[]rune`), NOT UTF-16 — an emoji is one column on both sides. A chroma
  upgrade that breaks the invariant fails that test loudly.
- **Highlight = server-side `<mark>`.** `gitdiff.MarkRanges` walks the chroma
  token spans and wraps `[FromCol,ToCol)` in `<mark class="comment-span">`,
  splitting across tokens. It's driven from the template by the **zero-arg**
  `PrereviewState.LineDisplay() map[string]HTML` (keyed `L<old>-<new>`) via
  `{{index $.LineDisplay $lkey}}` — livetemplate only pre-computes zero-arg
  methods, so a method-with-arg (`{{$.Foo .}}`) silently breaks rendering.
- **Side-awareness.** A modified line's number exists on BOTH the del(old) and
  add(new) rows, so the composer / cards / `<mark>` gate on `$lside` (== the
  selection's side), or they render twice.
- **Client** (`../client` `dom/directives.ts`, `lvt-fx:text-select` on `.code`):
  native selection → floating Comment button (positioned BELOW the selection on
  a coarse pointer, so iOS's OS-level Copy callout — which always beats z-index —
  doesn't cover it). Keyboard is a **client-only block caret** overlay
  (`[data-lvt-text-caret]`, absolute in the now-`position:relative` `.code`) over
  the char at (is-cursor line, client column): ←/→ move the column, ↑/↓ reuse the
  server `CursorUp/Down` line cursor (the caret re-syncs via `entry.sync()` on
  every render), Shift+arrows extend a selection via `getSelection().modify()` on
  the non-editable text (no contenteditable), Enter commits.
- **Drift.** `kind=text` reuses the line-anchor `relocate` for its line range,
  then `relocateTextColumns` re-finds `Anchor.Snippet` in the moved line to
  re-track the columns (single-line; multi-line keeps its columns).
