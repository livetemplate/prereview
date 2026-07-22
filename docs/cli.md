# prereview CLI reference

Companion to the [README](../README.md). The README shows the common
invocations; this is the full reference for every flag, mode, subcommand, and
combination.

```
Usage: prereview [flags] [path]
```

`path` is the review target — a git repo, a non-git directory, or a single
file. It's the trailing **positional** argument and defaults to the current
directory, so a bare `prereview` just works. **Flags must come before the
path** (Go's flag parser stops at the first non-flag): `prereview --agent ./docs`,
not `prereview ./docs --agent`.

## Defaults

| | Default | |
|---|---|---|
| `[path]` | `.` | current directory |
| `--base` | `HEAD` | working tree vs last commit (git mode only) |
| `--port` | `0` | OS-assigned random free port |
| `--host` | `127.0.0.1` | auto-resolves to the Tailscale IP on a remote box |
| `--out` | the review path | the directory whose `.prereview/` holds the store |
| `--agent` | off | UI shows **Quit**; with `--agent`, a **Queue** + **End session** UI |

So `prereview` ≡ `prereview --port 0 --host 127.0.0.1 .` reviewing the current
git repo's working tree against `HEAD`, in standalone (human-only) mode.

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
Everything else — comments, CSV, re-anchoring, the agent queue — is identical to
git mode.

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
re-anchoring — and are informational feedback about a page, not file-edit
directives.

`--out` is **required** (there's no repo to default the store to), and `[path]`
and `--base` are **ignored**.

### Agent mode (`--agent`)

Standalone `prereview` is human-only: you review, comments save to
`comments.csv`, and the top-bar button is **Quit**. `--agent` runs the review
**under a coding agent** — it streams the review as a queue of JSON events the
agent consumes with `prereview watch`, and swaps the UI for a **Queue**
(⏸ Pause / ▶ Resume) control plus an **End session** button.

```bash
prereview --agent "$(pwd)"          # what an agent's skill/command runs for you
```

This replaces the old one-shot `--skill` / `--stream` / `.prereview/DONE`
hand-off with a **continuous** model — no re-invocation between rounds, and no
hand-written CSV parser:

- **The queue.** Every comment (and every accepted suggestion) rides a
  `draft → queued → done` lifecycle. Comments you save while **live** are sent to
  the agent immediately; **Pause** holds them so you can batch, and **Resume**
  releases the whole batch at once. The Queue dropdown shows the counts (queued ·
  done · draft, plus "accepted, awaiting apply" and reviewer replies still
  awaiting the agent) and is the hub for Pause/Resume and End session.
- **The event stream.** In agent mode, after the usual `READY`/`REPO` preamble
  prereview emits one JSON object per line to **stdout** and mirrors it to
  `.prereview/events.jsonl` (append-only, reset each launch). Three event types:
  - **`ready`** (seq 0) — once, after the preamble. Carries `repo`, `csv`, and
    optional `paused` / `skill_updated`.
  - **`snapshot`** — on every queue mutation (debounced). A **full snapshot** of
    the still-actionable queue: `{"event":"snapshot","seq":N,"comments":[…],
    "suggestions":[…]}` (both arrays always present, `[]` when empty). Pre-filtered
    to what needs the agent — unresolved, non-outdated, non-draft comments, plus
    any comment the reviewer just replied on. The consumer dedupes by `id`.
  - **`end`** — once, on **End session**; the only terminator. The server shuts
    down right after.
  Every event carries a monotonic `seq`. The CSV stays the authoritative store;
  the stream is a convenience layer over it. Works in repo, no-git, and
  `--external` modes.
- **The reader.** The agent consumes the stream **only** with
  `prereview watch --since <seq>` (see [Subcommands](#subcommands)) — never a
  hand-rolled `tail`/`head`, which drops events when the tail isn't running. An
  agent that can't background + block-read instead polls
  `prereview comments --json` once per turn.

`--skill` and `--stream` are **deprecated aliases** of `--agent`: they still
enable agent mode (so existing skills/scripts keep working) but print
`prereview: --skill/--stream are deprecated; use --agent` to stderr. There is no
longer a `--stream`-only variant or a `.prereview/DONE` marker.

Full event-schema and comment-field docs: [skill/reference.md](../skill/reference.md).

## Flags

| Flag | Default | Notes |
|---|---|---|
| `--base <ref>` | `HEAD` | Git ref to diff against: `HEAD~1`, `main`, `origin/master`, a tag, a SHA — any rev-spec. **Git mode only** (ignored for a non-git dir / single file). |
| `--port <n>` | `0` | TCP port; `0` = OS-assigned random free port. |
| `--host <ip>` | `127.0.0.1` | Bind address — see [Binding](#binding--remote-access). |
| `--external <url>` | — | Annotate a **live local site** instead of files: reverse-proxies `<url>` on a second origin and overlays region annotation. **Requires `--out`**; ignores `[path]` and `--base`. See [External mode](#external-mode---external). |
| `--out <dir>` | the review path | Store root — the directory whose `.prereview/` holds `comments.csv` + the event log. Available in **every** mode (defaults to the review path, so repo mode is unchanged when omitted); **required** with `--external`. The `REPO` stdout line is the resolved store root, and the same value the [subcommands](#subcommands) take as `--out`. |
| `--agent` | `false` | Run under a coding agent: stream the review queue as JSON events (consume with `prereview watch`) and show the **Queue** (Pause/Resume) + **End session** UI instead of **Quit**. See [Agent mode](#agent-mode---agent). |
| `--replace` | `false` | prereview refuses to start a second server for the same store (duplicate servers fight over it). `--replace` stops the running one and takes over. Comments auto-save, so nothing is lost. |
| `--skill` / `--stream` | `false` | **Deprecated aliases of `--agent`** — still enable agent mode, print a deprecation warning. Use `--agent`. |

### Run-and-exit actions

These do one thing and exit — they don't start the server:

| Flag | Effect |
|---|---|
| `--version` | Print the build version. |
| `--install-skill` | Install the prereview integration for one or more coding agents and exit. With no `--client`, shows an interactive menu. The Claude Code skill auto-refreshes to match the binary on the next run after any upgrade (`--update`, brew, scoop, `go install`); other agents' files are not auto-refreshed — re-run with `--client=<id>` to update them. See [integrations.md](integrations.md). |
| `--client <ids>` | With `--install-skill`, the comma-separated agent(s) to install for: any of `claude,codex,gemini,opencode,aider,cursor`. Empty opens the menu. |
| `--update` | Download and install the latest GitHub release (defers to brew/scoop if one manages the binary). |
| `--uninstall` | Remove the binary (your `.prereview/` review comments are left untouched; defers to brew/scoop). |
| `--no-update` | Skip the on-run update check (also via `PREREVIEW_NO_UPDATE=1`). |

## Subcommands

Beyond launching a review, `prereview` has **bare-verb subcommands** the coding
agent uses to read from and write to a running review's `.prereview/` store.
They write into the same store the server watches, so their effect shows up live.
You rarely run them by hand — the skill does. Each takes `--out <dir>` (the printed
`REPO` directory; defaults to the current dir). Run any with `-h` for its own flags.

```
prereview watch     [--out <dir>] [--since <seq>]
prereview comments  [--out <dir>] [--json] [--all]
prereview done      [--out <dir>] [--file <f>|-] [--all-open] <comment-id>...
prereview status    [--out <dir>] <working|done> [message]
prereview suggest   [--out <dir>] [--file <payload.json>]
- `prereview quiz` - submit a comprehension quiz about a file's diff for the
  reviewer to answer; writes `.prereview/quiz.jsonl`.
prereview reply     [--out <dir>] (--body "…" | --file <f>|-) <id>
prereview applied   [--out <dir>] [--file <f>|-] <suggestion-id>...
prereview reverted  [--out <dir>] [--file <f>|-] <suggestion-id>...
```

- **`prereview watch`** — the **one** queue reader. Prints every event after
  `--since <seq>` (default `-1` = from the start), **blocks** for the next when
  caught up, returns a batch so the agent can act, and exits `0` only on the `end`
  event. Loop by re-running with the highest `seq` seen. Reads the durable
  `.prereview/events.jsonl`; writes nothing. Run it with a long timeout (e.g. 600s).
- **`prereview comments`** — list the review's comments from a stable interface
  (no CSV hand-parsing). Defaults to the actionable set; `--all` includes
  resolved/outdated/draft. `--json` emits the **same shape as a `snapshot`**, so
  you can pipe ids: `prereview comments --json | jq -r '.[].id' | prereview done --file -`.
  This is the read path for agents that can't block on `watch`.
- **`prereview done`** — mark comments **done** (a badge in the UI, so the reviewer
  sees you acted). Ids come from args, `--file`/stdin (bare ids, a JSON array, or
  JSONL objects with an `id`), or `--all-open` (the whole actionable set). Each
  explicit id is **validated against `comments.csv`** — an unknown id fails
  non-zero and is not recorded. Appends to `.prereview/processed.jsonl`.
- **`prereview status`** — echo the agent's status to a live pill across every open
  tab: `working` while applying a batch, `done` when finished. The `done` message
  doubles as the version changelog entry (see [Versioning](#output) below). Writes
  `.prereview/llm-status.json` atomically.
- **`prereview suggest`** — submit **proposed edits** that render inline as
  before → after boxes the reviewer **accepts** or **rejects**. Reads a JSON
  payload from `--file` or stdin — a single object, a JSON array, or newline-
  delimited objects — and appends to `.prereview/suggestions.jsonl`. Each object:
  `{"id":"…","file":"…","from_line":N,"to_line":N,"side":"new","original":"…","proposed":"…","note":"…"}`
  (`id` optional but recommended — re-using it *replaces* that suggestion; `side`
  defaults to `new`; `to_line` defaults to `from_line`). Decisions come back in
  each `snapshot`'s `suggestions[]`.
- **`prereview reply`** — post a **thread reply** on a comment **or** suggestion,
  so the reviewer sees what you changed (and can reply back to steer you). The id
  is validated against comments + suggestions. Appends to
  `.prereview/agent-replies.jsonl`. See [Threads](../skill/reference.md#threads).
- **`prereview applied`** — ack that you **applied** an accepted suggestion's edit
  to the file, so the UI flips the card from "accepted" to "applied" and drops it
  from the snapshot. Ids come from a snapshot's `suggestions[]` (`verdict=accept`);
  validated against suggestions. Idempotent. Appends to `.prereview/applied.jsonl`.
- **`prereview reverted`** — ack that you **reverted** an applied suggestion —
  restored its `original` text after the reviewer asked to undo the accept
  (delivered as `verdict=revert`). Nets the suggestion back out of "applied", so
  the reviewer's card returns to undecided. Validated against suggestions.
  Idempotent. Appends to `.prereview/reverted.jsonl`.

## Composing flags

Flags compose freely; just keep the path last.

```bash
prereview --base origin/main ../service        # a different repo, diffed against a ref
prereview --agent --base HEAD~3 "$(pwd)"        # agent mode, last-3-commits view
prereview --agent --replace "$(pwd)"            # take over an already-running session
prereview --host 0.0.0.0 --port 8080            # explicit bind (see below)
prereview --external http://localhost:5173 --out ./review   # annotate a live local site
```

`--base` only affects git mode. `--agent` only changes the UI/hand-off; it
composes with any path and base.

## Binding & remote access

`--host` defaults to `127.0.0.1` and is smart on remote boxes: on an SSH host
with a tailnet, prereview binds the host's **Tailscale IP** so you can open the
review from your phone over the tailnet — never the public internet. A remote box
with no tailnet stays on `127.0.0.1` and prints a stderr warning telling you to
pass an explicit `--host`. Passing `--host` explicitly is an absolute override
(never auto-rebound). **Avoid `0.0.0.0`**: it exposes the source diff on every
interface, including any public IP. The first stdout line is `READY <url>`; extra
`ALT <url>` lines (e.g. the MagicDNS hostname) may follow.

## Environment

- `PREREVIEW_QUIZZES_DIR` - override the quiz-prompt overlay directory
  (default `~/.config/prereview/quizzes`). Drop a `*.md` file there to add your
  own quiz prompt, or reuse a built-in's filename to replace it. The prompt is
  guidance only: prereview still enforces the question schema and still checks
  that every cited line exists in the diff, whatever prompt produced the quiz.

| Var | Effect |
|---|---|
| `PREREVIEW_NO_UPDATE=1` | Same as `--no-update`: skip the on-run update check. |

## Output

`<store-root>/.prereview/comments.csv` is the source of truth (RFC-4180, **17
columns**, atomically written via tmp+fsync+rename). The store root is the review
path by default; `--out` redirects it in any mode (and is required with
`--external`). For a single-file review the `.prereview/` dir is the file's
**parent** directory. The `REPO` line on stdout always points at the resolved
store root.

The header (column order is the contract; columns are only ever appended, so older
CSVs may have fewer trailing columns — index by position, default missing ones to
empty):

```
id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url,from_col,to_col,hidden,enqueued
```

`kind` is `line` (default), `text` (a character range within a line, via
`from_col`/`to_col` rune offsets), `file`, `area` (an image rectangle, `area`
holds `{x,y,w,h}` fractions), or `region` (a live-site rectangle from `--external`,
anchored to `url`). `hidden` and `enqueued` are reviewer/queue view flags. See
[skill/reference.md](../skill/reference.md#csv-schema) for the full column docs.

Alongside `comments.csv`, `.prereview/` holds a set of sidecar files. Those the
**agent** writes (via the subcommands) the server reads, and vice-versa:

| File | Written by | Purpose |
|---|---|---|
| `comments.csv` | server | the comments (source of truth) |
| `events.jsonl` | server | the agent-mode event log (`ready`/`snapshot`/`end`); reset each launch |
| `suggestions.jsonl` | **agent** (`prereview suggest`) | proposed edits, rendered as suggestion boxes (durable) |
| `suggestion-decisions.jsonl` | server | your accept/reject/revert verdicts (durable) |
| `processed.jsonl` | **agent** (`prereview done`) | comment ids marked done (reset each launch) |
| `applied.jsonl` | **agent** (`prereview applied`) | accepted-suggestion apply acks (durable) |
| `reverted.jsonl` | **agent** (`prereview reverted`) | applied-suggestion revert acks (durable) |
| `agent-replies.jsonl` | **agent** (`prereview reply`) | agent thread replies (durable) |
| `reviewer-replies.jsonl` | server | reviewer thread replies (durable) |
| `llm-status.json` | **agent** (`prereview status`) | the agent's live working/done status (reset each launch) |
| `versions/` | server | per-file version checkpoints + content blobs and their changelog (durable) |

When an agent finishes a batch that edited files, prereview snapshots a new
**version** of each changed file into `versions/`, using the agent's `prereview
status done` message as that version's changelog entry — surfaced in the file's
**Versions** panel (View / Diff / Restore). See
[skill/reference.md](../skill/reference.md#filesystem-layout) for the full layout.
