# prereview

**A local per-line code review webapp.** Run it in your repo, click on
lines you want to change, leave comments. A CSV is written that an LLM
(or you) can read and act on. No GitHub round-trip; no committed code
required.

Built on [livetemplate](https://github.com/livetemplate/livetemplate)
for the reactive web layer — pure Go binary, no JS runtime needed.

## Install

```bash
go install github.com/livetemplate/prereview@latest
```

This puts `prereview` on your `$PATH`.

## Quick start

### Standalone (manual review)

```bash
cd <your-repo>
prereview
# stdout: READY http://127.0.0.1:43029
```

Open the URL. Browse files in the left drawer, tap a line to select it,
tap another to extend the selection, type a comment, save. Click
**Quit** when done — the server shuts down. Your comments live in
`.prereview/comments.csv`.

### Skill mode (LLM-driven)

```bash
prereview --skill --repo "$(pwd)" --base HEAD &
```

The UI now shows **"Hand off → Claude"** instead of **"Quit"**. When
the user clicks it, `.prereview/DONE` is written with the path to the
CSV. The skill polls for that file, reads the CSV, and acts on the
comments.

See [skill/SKILL.md](skill/SKILL.md) for the LLM-side integration and
[skill/reference.md](skill/reference.md) for the full schema +
filesystem reference.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--repo` | `.` | Path to the git repo to review |
| `--base` | `HEAD` | Git ref to diff against (any rev-spec: `HEAD~1`, `main`, `origin/master`, commit SHA…) |
| `--port` | `0` | TCP port; 0 = OS-assigned |
| `--host` | `127.0.0.1` | Bind address. `0.0.0.0` exposes on LAN (handy for iPhone-over-Tailscale) |
| `--skill` | `false` | Show "Hand off → Claude" instead of "Quit"; writes `.prereview/DONE` on hand-off |
| `--version` | — | Print build version |

## Usage tips

- **Two-click range selection.** Tap a line to anchor. Tap another to
  extend the selection. Tap again to reseat the anchor. Type and save.
- **Edit/resolve/delete.** Each saved comment has Edit, Resolve,
  Delete buttons in the inline card. Resolve keeps the comment in
  the CSV with `resolved=true` (audit trail); Delete removes it
  entirely (with an Undo toast).
- **All-comments overview.** The `💬 N` chip in the top bar (desktop)
  or the overflow menu (mobile) jumps to a list view of every comment
  across all files; tap a comment to jump back to its source line.
- **View file mode.** Toggle "Diff" → "File" (in the overflow menu)
  to hide the diff overlay and read the file as it currently exists.
  Useful when the diff noise gets in the way.
- **Base picker.** The file drawer has a base dropdown (HEAD / HEAD~1
  / HEAD~5 / each local branch) plus a "Custom ref…" expandable for
  arbitrary refs.

## Output

`<repo>/.prereview/comments.csv` is the source of truth — RFC-4180
quoted, 8 columns, one row per comment:

```
id,file,from_line,to_line,side,body,created_at,resolved
```

See [skill/reference.md](skill/reference.md) for the full column
documentation.

## Architecture (at a glance)

- **Single binary**, embeds all assets (including the livetemplate
  client JS bundle) via `//go:embed`.
- **State held server-side** in livetemplate's session storage;
  WebSocket-driven UI patches. Pure Go on the server; no Node/npm.
- **Atomic CSV writes** via tmp+fsync+rename+parent-fsync. Read at any
  time without risk of a torn file.
- **Syntax highlighting** server-side via
  [chroma](https://github.com/alecthomas/chroma) — GitHub light theme.
  Cached per file path; falls back to plain text for unsupported
  languages.

## Development

```bash
git clone https://github.com/livetemplate/prereview
cd prereview
make sync-client   # copies the latest livetemplate-client.js into internal/assets/client/
go build .
./prereview --repo "$(pwd)" --port 8765
```

E2E tests use chromedp + a headless browser:

```bash
go test -tags=browser ./...
```

## License

MIT.
