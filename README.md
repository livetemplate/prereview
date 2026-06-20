# prereview

**Review any change — code, Markdown, HTML, images, even a live local site — by line, block, or region, then hand the fixes to an LLM. All local, before anything leaves your machine.**

<p align="center">
  <img src="docs/hero.gif" alt="prereview: selecting a line range on a Go diff, typing a comment, and clicking Hand off to Claude" width="820">
</p>
<p align="center"><sub><em>Select what's wrong — a line, a block, a region — comment, and hand the CSV to Claude.</em></sub></p>

A tiny local webapp for **reviewing your working tree and handing the
fixes to an LLM** — no commit, no PR, no GitHub round-trip. Run
`prereview` in your repo (or point it at any file or directory), click
what you want changed — a line or range in a diff, a rendered Markdown or
HTML block, a region of an image, or a box on a running dev site — and
leave a comment; a CSV is written that you (or an LLM) act on. It ships as
a [Claude Code](https://claude.com/claude-code) skill, so `/prereview`
launches a session, you comment, hit **"Hand off → Claude"**, and Claude
applies the changes. On a remote box it auto-binds your Tailscale address —
review from the Claude mobile app over the tailnet, before anything is
pushed.

## Features

- **Review any artifact, at the granularity that fits** — a line or range
  in a diff; a whole file; a rendered **Markdown** or **HTML** block (the
  comment anchors to real source lines); a dragged **region of a binary
  image**; or a box on a **live local site** (`--external`, proxies a
  running dev server). One tool for code and everything around it.
- **The review → fix loop** — "Hand off → Claude" writes a marker; the
  skill reads the CSV and applies your comments without leaving the chat.
  **Multi-round streaming** (`--stream`) emits a continuous JSON event
  stream the LLM consumes across many rounds — hand off as often as you
  like, ending only when you click **End session**. No re-invocation, no
  hand-written CSV parser.
- **Full GitHub-flavoured Markdown & HTML render** — tables, task-lists,
  syntax-highlighted code, `> [!NOTE]` alerts, footnotes, `:emoji:` and
  mermaid diagrams render the way GitHub shows them; formatted by default,
  but comments anchor to real source lines and round-trip with the raw view.
- **One CSV, atomically written** — the source of truth; read it any time
  without a torn file.
- **Phone-friendly + Tailscale-aware** — on a remote box it binds your
  tailnet address (never the public internet); review from your phone.
- **Single Go binary** — every asset embedded (Pico, fonts, client JS,
  mermaid); no Node, no JS runtime; works fully offline.

## How it's different

Most "AI code review" tools have the model *find* the problems for you to
read. prereview inverts that: **you** spot what's wrong — across any
artifact, not just code — and the LLM does the *fixing*, locally, before
you push.

- **vs. AI reviewers** (CodeRabbit, Gito, Ollama pre-commit hooks, Qodo) —
  they generate the review; prereview captures *your* judgment as
  structured comments an LLM then acts on.
- **vs. team review tools** (Gerrit, ReviewBoard, `arc diff`) — those are
  multi-person, server-side, and code-only; prereview is single-user,
  local, and reviews any artifact.
- **vs. diff viewers** (lazygit, tig, delta, difftastic) — they show
  changes; prereview captures anchored comments and hands them to an LLM.

## Install

`prereview` is a single static binary. **Prerequisite:** `git` on your
`$PATH`. Pick one:

```bash
# Quick install (macOS / Linux) — downloads the latest release, checksum-verified
curl -fsSL https://raw.githubusercontent.com/livetemplate/prereview/main/install.sh | sh
```
```bash
# Homebrew (macOS / Linux)
brew tap livetemplate/prereview https://github.com/livetemplate/prereview
brew trust --formula livetemplate/prereview/prereview   # one-time: brew requires trusting third-party taps
brew install livetemplate/prereview/prereview
```
```powershell
# Windows (Scoop)
scoop bucket add prereview https://github.com/livetemplate/prereview
scoop install prereview/prereview
```
```bash
# Go toolchain
go install github.com/livetemplate/prereview@latest
```

Quick-install knobs: `PREREVIEW_INSTALL_DIR=/path`, `PREREVIEW_VERSION=v0.4.0`.

<details>
<summary>Behind a corporate proxy, upgrading, or uninstalling</summary>

**Go install with the module proxy blocked**, in order: default (uses
`proxy.golang.org`) → `GOPROXY=direct GOSUMDB=off go install …@latest`
(still needs every dependency's VCS host) → an internal `GOPROXY=…` →
fully air-gapped, use the **Quick install** script or **Homebrew** (a
single prebuilt binary, no module fetching).

**Upgrade:** `brew upgrade prereview` · `scoop update prereview` · re-run
the install script · or `prereview --update` (curl/`go install` binaries
self-update hourly; disable with `--no-update` / `PREREVIEW_NO_UPDATE=1`).

**Uninstall** (your `.prereview/` comments are never touched):
`brew uninstall prereview` · `scoop uninstall prereview` ·
`prereview --uninstall` (defers to brew/scoop if one owns it) ·
`rm "$(go env GOPATH)/bin/prereview"`. Leftovers you can delete by hand:
the skill at `~/.claude/skills/prereview/` and the update-check cache.
</details>

### Claude Code skill (for the LLM-driven flow)

The binary embeds the skill — install it with one command:

```bash
prereview --install-skill   # → ~/.claude/skills/prereview/SKILL.md (+ reference.md)
```

Then invoke with `/prereview` or *"review my changes"*. (If it reports
"unknown skill", run `/reload`.) Once installed, the skill auto-refreshes
to match the binary on the next run after any upgrade — `prereview
--update`, `brew upgrade`, `scoop update`, or `go install` — so you never
re-run `--install-skill` to keep it current.

<details>
<summary>Manual / project-scoped install</summary>

From a clone, copy it yourself (e.g. project-scoped so it ships with the repo):

```bash
mkdir -p .claude/skills/prereview
cp skill/SKILL.md .claude/skills/prereview/SKILL.md
```

> The filename must be exactly `SKILL.md` — uppercase. A lowercase
> `skill.md` is silently ignored. `skill/reference.md` (full CSV schema +
> filesystem contract) is optional but handy to copy alongside.
</details>

## Quick start

```bash
cd <your-repo>
prereview                 # standalone: prints READY <url>, shows a Quit button
```

Open the URL, comment, click **Quit**. Comments live in
`.prereview/comments.csv`.

```bash
prereview --skill "$(pwd)" &   # what the Claude skill runs for you
```

In skill mode the UI shows **"Hand off → Claude"**: clicking it writes
`.prereview/DONE`; the skill polls for it, reads the CSV, and acts. Or
just tell Claude *"review my changes"* and it drives the whole loop. See
[skill/SKILL.md](skill/SKILL.md) and [skill/reference.md](skill/reference.md).

## CLI usage

The review target is the **positional path** (default: current dir);
everything else has a sane default, so a bare `prereview` just works.

```bash
prereview                                # current dir (git repo or not) — just works
prereview ./PLAN.md                      # a single file
prereview ./design-docs                  # a non-git directory — every file shown whole
prereview --base origin/main ../service  # a different git repo vs a ref (flags BEFORE the path)
prereview --external http://localhost:5173 --out ./review   # annotate a live local site (dev server)
prereview --skill                        # LLM hand-off mode (path defaults to .)
prereview --skill --stream               # multi-round JSON event stream for an LLM
```

A non-git directory or single file is auto-detected: it's shown whole
(every line commentable), with no diff and no base picker. Flags must come
**before** the path. Full reference — every flag, mode, and combination —
in **[docs/cli.md](docs/cli.md)**.

## Usage

**Comment on lines.** Tap a line to anchor, tap another to extend the
range (tap again to reseat), then type and save. The gutter line numbers
are permalinks — the URL hash tracks your selection so you can share or
reopen it.

**Comment on a whole file** with the **Comment on file** button — handy
for binary, deleted, or unchanged files where no line is clickable. The
file drawer defaults to *changed files only*; the **show all** toggle
exposes the full tree when you want to comment on something that didn't
change.

<p align="center"><img src="docs/file-comment.png" alt="A file-level comment shown above the diff" width="760"></p>
<p align="center"><sub><em>Comment a whole file — changed or not.</em></sub></p>

**Annotate an image region.** On a binary image, drag a rectangle to
select an area and comment on it; the box is stored as fractions, so it
survives re-encoding.

<p align="center"><img src="docs/image-area.gif" alt="Dragging a rectangle on an image and saving a region comment" width="760"></p>
<p align="center"><sub><em>Drag a box on an image to annotate a region.</em></sub></p>

**Markdown & HTML render** by default; tap a rendered block (heading,
paragraph, list…) to select its *source* lines, so the comment anchors
to real line numbers and round-trips with the raw view. A **Preview ⇄
Raw** toggle switches to source. Long docs get a table-of-contents
sidebar.

<p align="center"><img src="docs/markdown-block.gif" alt="Clicking a rendered Markdown block; the comment anchors to its source line" width="760"></p>
<p align="center"><sub><em>Markdown renders with a TOC; click a block and the comment anchors to its source line.</em></sub></p>

**Annotate a live local site** (`--external`). Point prereview at a
running dev server; it proxies the page so you can drag a box on any
region and comment — the annotation re-pins to the page as it scrolls.

<p align="center"><img src="docs/external-region.gif" alt="Dragging a region on a proxied live local site and saving a comment" width="760"></p>
<p align="center"><sub><em>Review a running site: drag a region on the live page and comment.</em></sub></p>

**See every comment in one place** — the **All comments** chip lists
comments across all files (line, file, and area kinds), each with a jump
back to its source.

<p align="center"><img src="docs/all-comments.png" alt="The all-comments overview listing line, file, and area comments across files" width="760"></p>
<p align="center"><sub><em>Every comment across files in one list.</em></sub></p>

**Review from your phone.** On a remote box prereview binds your
Tailscale IP, so the same review + hand-off works from the Claude mobile
app over the tailnet.

<p align="center"><img src="docs/review-mobile.png" alt="prereview reviewing a diff on a phone-sized screen" width="300"></p>
<p align="center"><sub><em>Review and hand off from your phone.</em></sub></p>

**More:** **Diff ⇄ File** toggles changed-hunks-with-context vs the whole
file (line numbers match, so comments resolve across both) · the base
**dropdown** picks `HEAD~N`, branches, or remotes (pass anything else via
`--base`) · each comment has **Edit / Resolve / Delete** (Resolve keeps
an audit trail; Delete has Undo) · **Esc** clears a selection.

## Output

`<repo>/.prereview/comments.csv` is the source of truth — RFC-4180
quoted, 13 columns, one row per comment:

```
id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url
```

`kind` is `line` (default), `file`, `area`, or `region` (a live-site
rectangle from `--external`, anchored to a `url`); `area` carries the
rectangle as `{x,y,w,h}` fractions. See
[skill/reference.md](skill/reference.md) for the full column docs.

## Architecture (at a glance)

- **Single binary**, embeds all assets (incl. the livetemplate client JS)
  via `//go:embed`.
- **State held server-side** in livetemplate's session storage;
  WebSocket-driven UI patches. Pure Go server; no Node/npm.
- **Atomic CSV writes** via tmp+fsync+rename — read at any time without a
  torn file.
- **Server-side syntax highlighting** via
  [chroma](https://github.com/alecthomas/chroma), cached per file path.

## Development

```bash
git clone https://github.com/livetemplate/prereview
cd prereview
make sync-client   # copies the latest livetemplate-client.js into internal/assets/client/
go build .
./prereview
```

E2E tests (in the `e2e/` package) use chromedp + headless chromium: `go test -tags=browser ./e2e/`.
Regenerate the README screenshots with `make screenshots`.

## License

MIT.
