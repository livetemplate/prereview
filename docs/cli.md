# prereview CLI reference

Companion to the [README](../README.md). The README shows the common
invocations; this is the full reference for every flag, mode, and combination.

```
Usage: prereview [flags] [path]
```

`path` is the review target — a git repo, a non-git directory, or a single
file. It's the trailing **positional** argument and defaults to the current
directory, so a bare `prereview` just works. **Flags must come before the
path** (Go's flag parser stops at the first non-flag): `prereview --skill ./docs`,
not `prereview ./docs --skill`.

## Defaults

| | Default | |
|---|---|---|
| `[path]` | `.` | current directory |
| `--base` | `HEAD` | working tree vs last commit (git mode only) |
| `--port` | `0` | OS-assigned random free port |
| `--host` | `127.0.0.1` | auto-resolves to the Tailscale IP on a remote box |
| `--out` | the review path | the directory whose `.prereview/` holds the store |
| `--skill` | off | UI shows **Quit** (not **Hand off → Claude**) |
| `--stream` | off | one-shot DONE handoff (no JSON event stream) |

So `prereview` ≡ `prereview --port 0 --host 127.0.0.1 .` reviewing the current
git repo's working tree against `HEAD`.

## Review modes (auto-detected from `path`)

`prereview` classifies the path and adapts — you don't pick a mode.

| Path is… | Mode | What you see |
|---|---|---|
| a dir with `.git` (incl. worktrees/submodules) | **git** | real `git diff` hunks vs `--base`; base picker shown; file list is git/`.gitignore`-aware (tracked + untracked) |
| a dir without `.git` | **no-git** | the dir walked recursively; **every file shown whole** (each line "new"/commentable), no diff, base picker hidden |
| a single file | **no-git (single file)** | just that file, whole; `.prereview/` lives in the file's **parent** dir |
| *(selected by `--external <url>`, not the path)* | **external (proxy)** | reverse-proxies a live local site; drag a box on any page to annotate; needs `--out` |

In no-git mode `--base` is ignored (there are no refs). The directory walk skips
`.git/`, `.prereview/`, dotfiles/dotdirs, and files over the 1 MB render cap.
Everything else — comments, CSV, re-anchoring, hand-off — is identical to git mode.

External mode is the exception to "auto-detected from `path`": it's turned on by
the `--external <url>` flag and ignores `[path]` entirely — see below.

```bash
prereview                        # current git repo
prereview ../service             # a different git repo
prereview ~/.claude/plans        # a non-git directory (e.g. Claude plans)
prereview ~/.claude/plans/x.md   # a single file
```

### External mode (`--external`)

Instead of reviewing files, `--external <url>` reverse-proxies a **running local
website** and overlays a region-annotation UI: you frame the live site, drag a box
on any page, and leave a comment that persists like any other.

```bash
prereview --external http://localhost:5173 --out ./review
```

To keep the app's own root-relative URLs (`/api/…`, its framework client, its
websockets) working with **zero rewriting**, external mode boots **two servers**:
the normal prereview UI **plus** a reverse proxy on its own port — a separate
origin the UI iframes. Both bind the same `--host`, so both are tailnet-reachable
on a remote box. Stdout gains an extra `PROXY <url>` line (the proxy origin) after
the `ALT` lines and before `REPO`; `READY` is still the UI url.

Annotations anchor to a **URL + region rectangle** rather than a file + line:
they're stored as `kind=region` rows with the page in the `url` column and the
rectangle (0..1 fractions of the page's document, so a re-pin survives scroll) in
the `area` column. Like image-area comments they're **frozen** — no content
re-anchoring.

`--out` is **required** (there's no repo to default the store to), and `[path]`
and `--base` are **ignored**.

### Stream mode (`--stream`)

The default handoff is **one-shot**: the skill polls `.prereview/DONE` once,
reads the CSV, acts, and stops. `--stream` turns the handoff into a **continuous,
multi-round** session for an LLM consumer — no re-invocation between rounds, and
no hand-written CSV parser.

```bash
prereview --skill --stream "$(pwd)"
```

`--stream` implies `--skill` and adds an **End session** button next to **Hand
off →**. prereview emits a JSON event log to **stdout** (one object per line,
after the usual `READY`/`REPO` preamble) and mirrors it to
`.prereview/events.jsonl` (append-only, for replay):

- `ready` — once, after the preamble.
- `handoff` — on every **Hand off** click; a full snapshot of the actionable
  comments (unresolved, non-outdated), each as ready-to-use JSON (the opaque
  `anchor` is dropped and `area` is a nested object, not a string). The consumer
  dedupes by `id` across rounds.
- `session_end` — once, on **End session**; the only terminator. The server
  shuts down right after.

Every event carries a monotonic `seq`, so repeated handoffs are distinguishable
(the idempotent DONE marker can't do that). The CSV stays the authoritative
store; the stream is a convenience layer over it. Works in repo, no-git, and
`--external` modes.

## Flags

| Flag | Default | Notes |
|---|---|---|
| `--base <ref>` | `HEAD` | Git ref to diff against: `HEAD~1`, `main`, `origin/master`, a tag, a SHA — any rev-spec. **Git mode only** (ignored for a non-git dir / single file). |
| `--port <n>` | `0` | TCP port; `0` = OS-assigned random free port. |
| `--host <ip>` | `127.0.0.1` | Bind address — see [Binding](#binding--remote-access). |
| `--external <url>` | — | Annotate a **live local site** instead of files: reverse-proxies `<url>` on a second origin and overlays region annotation. **Requires `--out`**; ignores `[path]` and `--base`. See [External mode](#external-mode---external). |
| `--out <dir>` | the review path | Store root — the directory whose `.prereview/` holds `comments.csv` + `DONE`. Available in **every** mode (defaults to the review path, so repo mode is unchanged when omitted); **required** with `--external`. The `REPO` stdout line is the resolved store root. |
| `--skill` | `false` | Show **Hand off → Claude** instead of **Quit**, and write `.prereview/DONE` on hand-off. The Claude Code skill sets this. |
| `--stream` | `false` | Emit a continuous JSON event stream for an LLM (stdout + `.prereview/events.jsonl`): each Hand off emits a `handoff` snapshot, a new **End session** button emits a terminating `session_end`. **Implies `--skill`.** See [Stream mode](#stream-mode---stream). |

### Run-and-exit actions

These do one thing and exit — they don't start the server:

| Flag | Effect |
|---|---|
| `--version` | Print the build version. |
| `--install-skill` | Install the Claude Code skill into `~/.claude/skills/prereview/`. Once installed, it auto-refreshes to match the binary on the next run after any upgrade (`--update`, brew, scoop, `go install`) — no need to re-run this. |
| `--update` | Download and install the latest GitHub release (defers to brew/scoop if one manages the binary). |
| `--uninstall` | Remove the binary (your `.prereview/` review comments are left untouched; defers to brew/scoop). |
| `--no-update` | Skip the on-run update check (also via `PREREVIEW_NO_UPDATE=1`). |

## Composing flags

Flags compose freely; just keep the path last.

```bash
prereview --base origin/main ../service        # a different repo, diffed against a ref
prereview --skill --base HEAD~3 "$(pwd)"        # skill mode, last-3-commits view
prereview --skill --stream "$(pwd)"             # multi-round JSON event stream for an LLM
prereview --host 0.0.0.0 --port 8080            # explicit bind (see below)
prereview --external http://localhost:5173 --out ./review   # annotate a live local site
```

`--base` only affects git mode. `--skill` only changes the UI/hand-off; it
composes with any path and base.

## Binding & remote access

`--host` defaults to `127.0.0.1` and is smart on remote boxes: on an SSH host
with a tailnet, prereview binds the host's **Tailscale IP** so you can open the
review from your phone over the tailnet — never the public internet. Passing
`--host` explicitly is an absolute override (never auto-rebound). **Avoid
`0.0.0.0`**: it exposes the source diff on every interface, including any public
IP. The first stdout line is `READY <url>`; extra `ALT <url>` lines (e.g. the
MagicDNS hostname) may follow.

## Environment

| Var | Effect |
|---|---|
| `PREREVIEW_NO_UPDATE=1` | Same as `--no-update`: skip the on-run update check. |

## Output

`<store-root>/.prereview/comments.csv` is the source of truth (RFC-4180, 13
columns, atomically written). The store root is the review path by default; `--out`
redirects it in any mode (and is required with `--external`). For a single-file
review the `.prereview/` dir is the file's **parent** directory. The `REPO` line on
stdout always points at the resolved store root. The 13th column, `url`, carries the
proxied page for `kind=region` rows (from `--external`); it's empty for every
file-based kind. See [skill/reference.md](../skill/reference.md) for the column
schema and the stdout protocol.
