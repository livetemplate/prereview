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
