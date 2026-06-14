# Deep links smoke test

This fixture exercises the deep-link work — load it with `prereview` and
poke around.

## Try these

1. Load `prereview $(pwd)` and append `#README.md:L7` to the URL.
   The viewer should open this file with line 7 selected.
2. Click the [other doc](OTHER.md) link below. The markdown link gets
   rewritten to a SPA hash, so the file switches without a full reload.
3. Click a heading anchor inside the [section](#fixtures) block.

## Fixtures

- [OTHER.md](OTHER.md) — paired markdown file with a link back here.
- [mockups/dashboard.html](mockups/dashboard.html) — HTML preview with
  anchor IDs (`#hero`, `#features`).
- [https://example.com](https://example.com) — external URL, must
  pass through unchanged.

## Back-button

After clicking around, the browser back-button should cycle through
the files you visited — not through every line-click (those use
`replaceState`).
