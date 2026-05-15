# prereview

**A local per-line code review webapp.** Run it in your repo, click on
lines you want to change, leave comments. A CSV is written that an LLM
(or you) can read and act on. No GitHub round-trip; no committed code
required.

Built on [livetemplate](https://github.com/livetemplate/livetemplate)
for the reactive web layer — pure Go binary, no JS runtime needed.

## Install

### Binary

```bash
go install github.com/livetemplate/prereview@latest
```

This puts `prereview` on your `$PATH`.

### Claude Code skill (optional, for the LLM-driven flow)

The skill lets Claude Code launch and drive a review session for you —
just say *"review my changes"* or `/prereview` instead of running the
binary by hand. It's a single Markdown file you copy into a skills
directory Claude Code scans.

Personal (available in every repo) — from a clone of this repo:

```bash
mkdir -p ~/.claude/skills/prereview
cp skill/SKILL.md ~/.claude/skills/prereview/SKILL.md
```

Or fetch it directly (once the repo is published):

```bash
mkdir -p ~/.claude/skills/prereview
curl -fsSL https://raw.githubusercontent.com/livetemplate/prereview/main/skill/SKILL.md \
  -o ~/.claude/skills/prereview/SKILL.md
```

Project-scoped (commit it so everyone who clones the repo gets it):

```bash
mkdir -p .claude/skills/prereview
cp skill/SKILL.md .claude/skills/prereview/SKILL.md
```

> **The filename must be exactly `SKILL.md` — uppercase, case-sensitive.**
> A lowercase `skill.md` is silently ignored and the skill never
> registers. This is the single most common install mistake.

Requirements & notes:

- The skill shells out to the `prereview` binary, so install the binary
  (above) first and keep it on `$PATH`.
- Invoke it with `/prereview`, or natural-language triggers like
  *"review my changes"* / *"per-line code review"*.
- New or renamed skills are normally picked up within the running
  session; if `/prereview` reports "unknown skill", run `/reload` or
  restart Claude Code (discovery is primarily a startup scan).
- `skill/reference.md` is supplementary (full CSV schema + filesystem
  contract). The skill works without it, but copy it alongside
  `SKILL.md` if you want Claude to have the deep reference handy.

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

With the [Claude Code skill installed](#claude-code-skill-optional-for-the-llm-driven-flow),
just ask Claude to *"review my changes"* (or run `/prereview`). Claude
launches the session, hands you the URL, waits for your hand-off, then
reads and acts on your comments — you don't run anything by hand.

Equivalent manual invocation (what the skill runs for you):

```bash
prereview --skill --repo "$(pwd)" --base HEAD &
```

The UI shows **"Hand off → Claude"** instead of **"Quit"**. When you
click it, `.prereview/DONE` is written with the path to the CSV; the
skill polls for that file, reads the CSV, and acts on the comments.

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
- **Diff vs File.** Toggle "Diff" → "File" (overflow menu on mobile,
  chip on desktop). **Diff** shows only the changed hunks — changed
  lines plus 3 lines of context, with long unchanged runs collapsed to
  a "··· N unchanged lines ···" marker (an unchanged file has no diff,
  so it shows whole). **File** shows the entire current file with no
  diff at all — no add/del coloring, deletions omitted. Line numbers
  are identical in both modes, so a comment made in one resolves in
  the other.
- **Markdown renders by default.** `.md`/`.markdown` files show
  formatted (headings, lists, code, tables) instead of raw lines.
  You can still comment: tap a rendered block (heading, paragraph,
  list, code fence…) and it selects that block's *source* line range —
  the comment anchors to real line numbers, so it round-trips with the
  raw view and the CSV exactly like a line comment. A "Rendered" ⇄
  "Raw" toggle switches to the source line view. Raw HTML embedded in
  the Markdown is not rendered (safe for untrusted repos).
- **Base picker.** The file drawer has a base dropdown listing
  `HEAD~1/3/5/10`, every local branch, and every remote-tracking
  branch (e.g. `origin/main`). For anything more exotic (a specific
  commit SHA, a tag), pass it once at launch with `--base`.
- **File scope.** The drawer defaults to *changed files only* (vs the
  base) — the right default on a large repo. A "Changed N · show all M"
  toggle switches to the full tracked-file list when you want to
  comment on something that didn't change. On a clean tree (nothing
  changed) it auto-shows everything and the toggle is hidden.

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
