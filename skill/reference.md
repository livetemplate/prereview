# prereview reference

Companion to [SKILL.md](./SKILL.md). SKILL.md tells the agent **what to do**;
this file is the lookup for **what every value means**.

## Contents

- [CLI flags](#cli-flags)
- [Agent subcommands](#agent-subcommands)
- [stdout protocol](#stdout-protocol)
- [Exit codes](#exit-codes)
- [Filesystem layout](#filesystem-layout)
- [Agent mode](#agent-mode) — the event stream (`ready` / `snapshot` / `end`)
- [Threads](#threads) — the two-way reviewer↔agent conversation (`prereview reply`)
- [Suggested edits](#suggested-edits)
- [Comment data](#comment-data) — kinds, resolved, re-anchoring
- [CSV schema](#csv-schema)
- [Standalone mode](#standalone-mode)
- [External mode](#external-mode)
- [Atomicity guarantees](#atomicity-guarantees)
- [Behavioral quirks](#behavioral-quirks)

## CLI flags

| Flag | Default | Required | Description |
|---|---|---|---|
| `[path]` (positional) | `.` | recommended (for agent use) | Path to review — the trailing positional argument, e.g. `prereview --agent "$(pwd)"`. Must come **after** all flags. Usually a git repository; may also be a **single file** or a **non-git directory** (e.g. a Claude plan) → no-git mode: no diff, no base, every line "new". For a single file the review root is the file's **parent** directory. |
| `--agent` | `false` | yes (for agent use) | Run under a coding agent: stream the review queue as JSON events (consume with `prereview watch`) and show the Queue (⏸ Pause / ▶ Resume) + **End session** UI. `--skill` and `--stream` are **deprecated aliases** of `--agent` — they still enable agent mode but print a deprecation warning; use `--agent`. |
| `--base` | `HEAD` | no | Git base for diff comparison. `HEAD` = working tree vs last commit; `main` = branch-vs-trunk; `HEAD~3` = last-3-commits view; any rev-spec git accepts works. **Ignored in no-git mode** (single file / non-git dir — there is no base). |
| `--port` | `0` | no | TCP port to listen on. `0` = OS-assigned (random free port — what the agent should normally use to avoid collisions). |
| `--host` | `127.0.0.1` | no | Host/IP to bind on. **Auto-resolved when not set explicitly:** a remote (SSH) box with a tailnet binds its Tailscale IP (phone-reachable, never public); a remote box with no tailnet stays `127.0.0.1` and prints a stderr warning; local stays `127.0.0.1`. An explicit value is an absolute override and is never auto-rebound — avoid `0.0.0.0`, which exposes the source diff on every interface including any public IP. |
| `--external` | — | no | Annotate a live local website instead of files: reverse-proxies the URL on a second origin and overlays region annotation. Requires `--out`; ignores `[path]`/`--base`. See [External mode](#external-mode). |
| `--out` | the review path | with `--external` | Directory whose `.prereview/` holds `comments.csv`. Defaults to the review path; required with `--external`. Also the `--out <REPO>` argument the [agent subcommands](#agent-subcommands) take. |
| `--version` | — | no | Print build version and exit. |
| `--update` / `--uninstall` | — | no | Download+install the latest release, or remove the binary. prereview also self-updates + re-syncs the installed skill on a normal launch (see `skill_updated` in [Agent mode](#agent-mode)); `PREREVIEW_NO_UPDATE=1` or `--no-update` skips the on-run check. |

## Agent subcommands

Besides launching a review, `prereview` has verb subcommands the coding agent uses to
read from and write to a running review's `.prereview/` store. Each takes `--out
<REPO>` (the printed `REPO` directory; defaults to the current dir). Run any with `-h`
for its own flags, or `prereview help` for the top-level list.

| Subcommand | Purpose |
|---|---|
| `prereview watch [--since <seq>]` | The **one** queue reader (see [Agent mode](#agent-mode)). Prints every event after `--since`, blocks for the next when caught up, returns a batch so the agent can act, and exits on the `end` event. Each line carries a `seq`; loop by re-running with the highest seq seen. Never `tail`/`head` the log. |
| `prereview comments [--json] [--all]` | List the review's comments from a stable interface (no CSV hand-parsing). Defaults to the actionable set; `--all` includes resolved/outdated/draft. `--json` emits the same shape as a `snapshot`. |
| `prereview done [--file <f>\|-] [--all-open] <id>...` | Mark comments **done** (badge them in the UI). Ids come from args, `--file`/stdin (bare ids, a JSON array, or JSONL objects with an `id`), or `--all-open` (the whole actionable set). Each explicit id is **validated against `comments.csv`** — an unknown id fails with a non-zero exit and is not recorded. |
| `prereview status <working\|done> [message]` | Echo the agent's status to the review UI (a live pill across every open tab): `working` while applying a batch, `done` when finished. Writes `llm-status.json` atomically. |
| `prereview suggest [--file <f>]` | Submit proposed edits rendered as inline suggestion boxes (append to `suggestions.jsonl`). See [Suggested edits](#suggested-edits). |
| `prereview reply <id> (--body "…"\|--file <f>\|-)` | Post a thread reply on a comment **or** suggestion, so the reviewer sees what you did (and can reply back to steer). Validated against comments + suggestions. See [Threads](#threads). |
| `prereview applied <suggestion-id>...` | Ack that you applied an **accepted** suggestion's edit to the file, so the UI marks it "applied" and drops it from the snapshot. Ids come from a snapshot's `suggestions[]` (`verdict=accept`); validated against suggestions. Idempotent. |
| `prereview reverted <suggestion-id>...` | Ack that you **reverted** an applied suggestion — restored its `original` text after the reviewer asked to undo the accept (delivered as `verdict=revert`). Nets the suggestion back out of "applied" → the card returns to undecided. Validated against suggestions. Idempotent. |

## stdout protocol

On startup, prereview prints these lines to stdout, in order:

```
READY http://<host>:<port>
ALT   http://<host>:<port>     (zero or more; only when on a tailnet)
PROXY http://<host>:<port>     (external mode only; the proxy origin the UI frames)
REPO  <absolute review-root directory>
```

`READY` is the **first line** and carries the canonical, always-reachable URL
(loopback locally; the Tailscale IP on a remote box). It is the only line the skill and
the e2e harness machine-parse — match the literal prefix `READY ` and take the rest.
Zero or more `ALT` lines follow with additional reachable forms (chiefly the MagicDNS
hostname); they are purely additive and parsers may ignore them. In external mode one
extra `PROXY <url>` line follows the `ALT` lines (before `REPO`) — additive too;
`READY` remains the canonical UI url. `REPO` is the directory whose `.prereview/` holds
the store — equal to the path argument for a git repo or non-git directory, and the
file's **parent** directory for a single-file review. Operate relative to the `REPO`
line, not the raw path argument. All other output is slog-formatted and goes to stderr
— including the "remote box, no tailnet" fallback warning.

In **agent mode** (`--agent`), after the preamble prereview emits one JSON object per
line (JSONL) to stdout — a `ready` event, a `snapshot` per queue mutation, then a
single `end` — and mirrors the same lines to `<REPO>/.prereview/events.jsonl`. The
plaintext `READY ` line still comes first, so the preamble parse is unchanged (JSON
lines start with `{`). See [Agent mode](#agent-mode) for the schema.

If the bind fails or the path is invalid (missing, unreadable), prereview exits
non-zero without printing `READY`.

## Exit codes

| Code | Cause |
|---|---|
| `0` | Graceful shutdown via Quit (standalone), End session (agent mode), or SIGINT/SIGTERM |
| `1` | Argument validation failed (missing repo, port already in use, etc.) |
| `1` | Runtime error during shutdown |

In agent mode, clicking **End session** emits the `end` event and shuts the server
down, so a backgrounded launch's job exits on its own — the agent does not need to kill
it.

## Filesystem layout

Everything prereview writes lives under `<REPO>/.prereview/`, where `<REPO>` is the
directory from the stdout `REPO` line:

```
<REPO>/
└── .prereview/
    ├── comments.csv                ← source of truth, rewritten atomically on every change
    ├── events.jsonl                ← agent mode only (--agent); append-only JSON event log, reset each launch
    ├── processed.jsonl             ← INBOUND: per-comment "done" markers (`prereview done`); append-only
    ├── llm-status.json             ← INBOUND: agent-status echo, watched by the server; reset each launch
    ├── suggestions.jsonl           ← INBOUND: the agent's proposed edits (`prereview suggest`); append-only, durable
    └── suggestion-decisions.jsonl  ← reviewer's accept/reject verdicts; server-owned, rewritten atomically, durable
```

The INBOUND files are written by the **agent** and read by the server (the reverse of
`comments.csv`). `suggestion-decisions.jsonl` is the reviewer's reply — server-owned —
and its verdicts ship back to the agent in each `snapshot`'s `suggestions` array (see
[Agent mode](#agent-mode)). The agent writes `llm-status.json` via `prereview status <working|done> [message]`
(atomically) — the file is `{"state":"working"|"done","message":"…","updated_at":"<RFC3339>"}`;
the server polls it (~0.75s) and pushes the status to every open browser tab. Missing/blank `state` is idle. `events.jsonl`, `llm-status.json`, and
`processed.jsonl` reset on each fresh launch; `suggestions.jsonl` /
`suggestion-decisions.jsonl` are durable across launches.

For a git repo or non-git directory `<REPO>` is the path argument. For a single-file
review `<REPO>` is the file's **parent** directory, so sibling files reviewed from that
directory share one `comments.csv` (the `file` column disambiguates rows). `.prereview/`
is created eagerly on startup. Add it to the repo's `.gitignore`.

## Agent mode

`--agent` streams the review queue for a coding-agent consumer. prereview emits a JSON
event log — to stdout (live) and
`<REPO>/.prereview/events.jsonl` (durable replay) — that the agent reads continuously
with **`prereview watch --since <seq>`** (the one reader; do not hand-roll `tail`/`head`,
which drops events whenever the tail isn't running). Consume it until the `end` event.

Three event types:

- **`ready`** (seq 0) — emitted once, after the stdout preamble. Fields:
  `event, seq, ts, repo, csv`, plus optional `paused` and `skill_updated`.
  - `skill_updated:true` means this launch **refreshed the installed prereview skill**
    to match the (possibly self-updated) binary — so the agent's loaded skill is now
    stale. The agent must re-read the skill from its install path
    (`~/.claude/skills/prereview/SKILL.md`) before continuing, and tell the user to
    reload the skill in their agent. (A matching stderr note prints too.)
  - `paused:true` means the reviewer started with the queue paused.
- **`snapshot`** — emitted (debounced) per queue mutation. A **FULL snapshot of the
  still-actionable queue** (a superset, not a delta): act only on the **latest** and
  **dedupe by `id`**. Fields: `event, seq, ts, comments[], suggestions[]`. `comments`
  and `suggestions` are always present (`[]` when empty). The snapshot is pre-filtered
  to what needs you: unresolved, non-outdated, non-draft comments — **plus** any comment
  the reviewer just replied on, whatever its state (resolved, outdated, or already done;
  see [Threads](#threads)); an item you
  replied on last drops out until the reviewer speaks again. While the reviewer has the
  queue **paused** (batching), no snapshot is emitted — mutations still persist, but
  `watch` blocks — and **resume** emits ONE coalesced snapshot of everything queued.
- **`end`** — emitted once on **End session**. The **only** terminator: stop consuming.
  The server shuts down right after (so a backgrounded launch's job completes too).

Every event has `event`, a monotonic `seq`, and an RFC-3339 `ts`. Each comment in a
`snapshot` mirrors the CSV columns **minus** the opaque `anchor` fingerprint and
`resolved`, with `area` as a nested object (or `null`) — no nested JSON-in-a-string.
Each comment carries: `id`, `kind`, `file`, `from_line`, `to_line`, `from_col`,
`to_col`, `side`, `body`, `url`, `area`, `created_at`, `anchor_status`, `text` (the
exact selected substring, for `kind=text`), and `thread` (the conversation, present
only when non-empty — see [Threads](#threads)). See [Comment data](#comment-data) for
how to interpret them.

```jsonc
{"event":"ready","seq":0,"ts":"…","repo":"/abs/dir","csv":"/abs/dir/.prereview/comments.csv"}
// `paused` / `skill_updated` appear only when true (omitted otherwise):
{"event":"ready","seq":0,"ts":"…","repo":"/abs/dir","csv":"…","skill_updated":true}
{"event":"snapshot","seq":1,"ts":"…","comments":[
  {"id":"01J…","kind":"line","file":"main.go","from_line":42,"to_line":42,
   "from_col":0,"to_col":0,"side":"new","body":"rename this","url":"","area":null,
   "created_at":"…","anchor_status":"ok",
   // present only when non-empty; last entry here is the reviewer → respond to it:
   "thread":[{"author":"agent","body":"Renamed to userToken.","at":"…"},
             {"author":"reviewer","body":"also update the docs","at":"…"}]}
],"suggestions":[
  {"id":"s1","file":"docs/readme.md","from_line":12,"to_line":12,"side":"new",
   "verdict":"accept","original":"The API might returns an error.",
   "proposed":"The API may return an error.","anchor_status":"ok"}
]}
{"event":"end","seq":2,"ts":"…"}
```

`events.jsonl` is append-only and reset on each fresh launch. The CSV stays the
authoritative store; the stream is a convenience layer over it.

## Threads

A **thread** is the two-way conversation on a comment or suggestion (#149). It rides on
each snapshot item as a `thread` array (present only when non-empty), oldest first; each
entry is `{ "author": "agent" | "reviewer", "body": "…", "at": "<RFC3339>" }`.

- **You → reviewer:** `prereview reply --out "<REPO>" <id> --body "…"` appends an
  `agent` entry the reviewer sees under the card. Post one after addressing a comment to
  say what you changed (alongside the `done` mark). Validated against comments +
  suggestions, like `done`.
- **Reviewer → you:** the reviewer replies under the card; that `reviewer` entry re-arms
  the snapshot. When an item's **last thread entry is `reviewer`**, they're steering you
  — respond to it, then `reply`.
- **Unread model (why items appear/disappear).** The snapshot carries an item only when
  it is **fresh** (no thread yet, unresolved) or its **last entry is the reviewer's**.
  Reply, and it drops out until the reviewer speaks again — you never re-answer yourself.
  A **resolved, outdated, or already-done** comment reappears iff the reviewer replied on
  it (reopening the conversation); the reply overrides the suppression but never changes
  `resolved`/`outdated`/`done` on its own, and marking `done` again won't settle it —
  only your `reply` drops it back out.

Suggestion threads work identically — a `thread` on a `suggestions[]` entry.

## Suggested edits

The agent proposes edits with `prereview suggest` (a JSON payload — a single object, a
JSON array, or newline-delimited objects — on stdin or via `--file`). It appends to
`<REPO>/.prereview/suggestions.jsonl`; the running UI picks them up live (no restart).

**Prompt entry point (#147).** The reviewer's file-header "Ask for suggestions" picker
sends a prebuilt prompt as an ordinary `kind=file` comment whose body asks for
suggestions. The agent answers it with `prereview suggest` (proposing, not editing the
file), then `done`s the prompt and `reply`s a one-line summary — see the SKILL's
*Suggested edits → Prompts*. Users add their own prompts as `.md` files in
`~/.config/prereview/prompts/` (first `# Heading` = the picker title, the rest = the
prompt body); they override the built-ins by filename.

Fields per suggestion:

- `id` — **stable, your choice.** Re-submitting the same `id` *revises* it (last write
  wins); a new `id` adds one. (Omit it and prereview generates one, but then you can't
  revise it — so always set your own.)
- `file` — repo-relative path (same `file` value the comments use).
- `from_line` / `to_line` — 1-based line range on the **new** side the edit replaces.
  `to_line` defaults to `from_line`.
- `original` — the exact current text at those lines. This is the anchor: prereview
  re-locates it if the file changed, and marks the suggestion `outdated` if it's gone —
  paste it verbatim. Leave empty for a pure insertion.
- `proposed` — the replacement text (multi-line is fine).
- `note` — optional one-line rationale, shown on the box.

**Proposing alternatives (a group).** To offer the user a *choice* between ways to edit
the same text, submit multiple suggestions with the **same** `file`/`from_line`/`to_line`/
`original` and different `proposed` (each a distinct `id`). prereview groups them: the
user accepting one **auto-rejects the rest**, so you get exactly one `accept` and the
others as `reject` — no need for them to reject each by hand.

**Applying the reviewer's decisions.** Each `snapshot`'s `suggestions[]` is a full
snapshot of every decided suggestion (dedupe by `id`). Each entry:

```
{"id","file","from_line","to_line","side","verdict","note","original","proposed","anchor_status"}
```

Act by `verdict`:

- **`accept`** — apply the edit: replace `original` with `proposed` at
  `file`:`from_line`–`to_line`. You own the file write (prereview never edits the user's
  files). Then **`prereview applied "<id>"`** to ack it — that flips the card to "applied"
  and drops it from future snapshots (re-acking is harmless).
- **`reject`** — the user declined it. **Drop it** — do not apply, do not re-submit.
  (It keeps reappearing; dedupe by `id` and skip it, like a handled comment.)
- **`revert`** — the user wants an edit you already applied UNDONE. The file holds your
  applied `proposed`; **restore `original` over it** (the inverse), then
  **`prereview reverted "<id>"`** to ack. That nets it back out of "applied" and returns
  the card to undecided. Idempotent per `id`.

Every emitted entry has `anchor_status` **`ok`** or **`moved`** — both carry
trustworthy `from_line`/`to_line` (a `moved` suggestion was already re-anchored). An
`outdated` suggestion is **never** emitted, so once you apply an `accept` it simply
stops appearing.

## Comment data

These rules apply to the `snapshot`'s `comments[]` and to the CSV (a snapshot entry
mirrors the CSV columns minus the opaque `anchor` and `resolved`).

### Comment kinds

- `line` (or empty for legacy rows) — anchored to a line range via
  `from_line`/`to_line`/`side`. Edit the named file at those lines per the body.
- `text` — a character range **within** a line: `from_col`/`to_col` are 0-based rune
  offsets (half-open) into the raw line; `text` carries the exact selected substring.
  Rendered-Markdown ("block") selections anchor at line level (`from_col==to_col==0`)
  with the phrase in `text`. Apply the body to that range/phrase.
- `file` — applies to the whole file. `from_line`/`to_line` are `0` and `side`/`anchor`/
  `anchor_status` are empty; don't look at them. Treat as guidance for the entire file
  (e.g. "rename to foo.go", "this binary shouldn't be in the PR"), or locate the edit
  from the body semantically.
- `area` — a rectangular region on a binary image (PNG, JPEG, SVG, …). `from_line`/
  `to_line`/`side`/`anchor`/`anchor_status` are zero/empty; `area` holds
  `{"x":0.1,"y":0.2,"w":0.3,"h":0.15}` as 0..1 fractions of the image's natural
  dimensions (multiply by natural width/height for pixels). Treat the body as guidance
  for that region.
- `region` — a rectangle on a **live page** from `--external` live-site review. It
  points at a URL + page region, not a file: `file` is empty and there are no line
  numbers. Treat as **context/feedback** about a page, not an actionable file edit.

### Resolved comments

A comment marked `resolved=true` is one the reviewer has already declared addressed.
**Skip resolved rows** as directives (treat as historical context). Only act on
`resolved=false`. (The snapshot has already dropped them.)

### Re-anchoring

prereview captures a content fingerprint per comment and re-locates it if the doc
changes — including edits *you* make (relocation runs before each snapshot, so the data
you receive is already re-anchored):

- `ok` / empty — line numbers are trustworthy.
- `moved` — the doc changed; prereview auto-corrected `from_line`/`to_line` to follow
  the content. Still trustworthy.
- `outdated` — the anchored content changed or vanished and prereview could **not**
  confidently re-place it; line numbers are stale. **Skip these like `resolved=true`**
  (use `body` as context only). (The snapshot has already dropped them.)

## CSV schema

File: `<REPO>/.prereview/comments.csv`. RFC-4180 quoted; UTF-8. It's the on-disk source
of truth, but you rarely need to parse it — `prereview comments --json` returns the same
shape as a snapshot.

### Header (load-bearing — column order is the contract)

```
id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url,from_col,to_col,hidden,enqueued
```

Older CSVs may have 7–16 columns; columns 0–7 are stable, so index by position and
treat missing trailing columns as empty/default. Columns are only ever appended.

### Column details

| Column | Type | Example | Notes |
|---|---|---|---|
| `id` | string (ULID) | `01HMXFGB3PQT8VN7R6W4ZK2YHE` | Opaque, unique per comment. Don't parse for meaning. |
| `file` | string (relative path) | `internal/foo/bar.go` | Relative to the review root. For a single-file review, the file's basename. Forward slashes regardless of OS. |
| `from_line` | int (1-based) | `42` | First line of the range. `0` when `kind=file`/`area`/`region`. |
| `to_line` | int (1-based) | `48` | Last line (inclusive). Equal to `from_line` for single-line comments. |
| `side` | enum | `new`, `old` | Which side of the diff. `new` = post-change; `old` = pre-change (deleted from base). Empty for `kind=file`/`area`/`region`. |
| `body` | string | `"Why no error wrap?"` | RFC-4180 quoted; newlines preserved. |
| `created_at` | RFC-3339 UTC | `2026-05-13T14:23:11Z` | Set once on creation; unchanged on edit. |
| `resolved` | bool | `true`, `false` | `true` = human marked it addressed; **skip as a directive**. |
| `anchor` | JSON string | `{"text":"…","before":[…]}` | **Internal — do not parse.** Content fingerprint for re-anchoring. May be empty for legacy rows. |
| `anchor_status` | enum | `ok`, `moved`, `outdated`, *(empty)* | See [Re-anchoring](#re-anchoring). Always empty for `kind=file`/`area`/`region`. |
| `kind` | enum | `line`, `text`, `file`, `area`, `region`, *(empty)* | See [Comment kinds](#comment-kinds). Empty means `line` for pre-migration rows. |
| `area` | JSON string | `{"x":0.1,"y":0.2,"w":0.3,"h":0.15}` | 0..1 fractions of the **image** (`kind=area`) or the live page's **document** (`kind=region`). Empty otherwise. |
| `url` | string (app-relative) | `/pricing` | The proxied page for a `kind=region` comment. **Empty for every file-based kind.** |
| `from_col` | int (0-based rune) | `6` | Start of a `kind=text` range (rune offset into the raw line). `0` for every other kind. |
| `to_col` | int (0-based rune) | `12` | End (exclusive) of the `kind=text` range. `0` for every other kind. |
| `hidden` | bool | `true`, `false` | Reviewer-only view flag. **Ignore it** — never affects whether a row is actionable. |
| `enqueued` | bool | `true`, `false` | Queue view flag (#119): `false` = a reviewer *draft* not yet released to the agent. **Ignore it** — the snapshot already excludes drafts. Absent on legacy rows (read as `true`). |

### Parsing example

Use Go's `encoding/csv` or any RFC-4180 parser. Don't hand-split on commas — `body` can
contain commas, newlines, and quotes.

```go
r := csv.NewReader(f)
rows, _ := r.ReadAll()  // rows[0] is the header
for _, row := range rows[1:] {
    if len(row) > 7 && row[7] == "true" { continue }      // skip resolved
    if len(row) > 9 && row[9] == "outdated" { continue }  // skip stale-anchored
    file, from, to := row[1], row[2], row[3]
    body := row[5]
    // …act on it
}
```

## Standalone mode

Bare `prereview [path]` (no `--agent`) is human-only review — the top-bar button reads
**Quit** and gracefully shuts the server down. Nothing is streamed and no queue events
are emitted. Comments still auto-save to `comments.csv`, so the user can read them
manually or relaunch with `--agent` later to have an agent process them.

## External mode

`--external <url>` reverse-proxies a **live local site** instead of reviewing files; the
user drags a box on any page to leave a comment. Comments are stored as `kind=region`
rows — a **URL + rectangle** (`url` + `area` columns), not a file + line — and are
**frozen** (no content re-anchoring, like `area`). Requires `--out` (no repo to default
the store to); `[path]`/`--base` are ignored. Region comments are informational feedback
about a page, not file-edit directives.

## Atomicity guarantees

`comments.csv` is rewritten on every add/edit/delete/resolve via: (1) write
`comments.csv.tmp`, (2) `fsync` the tmp file, (3) `rename(tmp, comments.csv)` (atomic on
POSIX), (4) `fsync` the parent directory. Reading the CSV at any time is safe — you see
either the pre- or post-mutation state, never a torn write.

## Behavioral quirks

- **Untracked files** appear in the file list as added (`[A]`). Commenting works the
  same as on tracked files.
- **No-git mode** (single file or non-git directory): no git base, every file listed as
  added (`[A]`), `--base` ignored, base picker hidden. A non-git directory is walked
  recursively, skipping `.git/`, `.prereview/`, dotfiles/dotdirs, and files over the
  1 MB render cap. Everything else is identical to git mode — the path for reviewing
  Claude plans and loose docs.
- **File-list scope.** By default the drawer lists only files that differ from the base.
  A "Changed N · show all M" toggle switches to the full tracked-file list. On a clean
  tree the scope auto-falls back to all files (never an empty list) and the toggle is
  hidden. Comment processing is unaffected.
- **Diff vs File view.** A top-bar toggle. *Diff* (default) shows changed hunks plus 3
  context lines, long unchanged runs collapsed. *File* shows the whole current file.
  Line numbers are identical in both, so a comment anchors to the same line regardless.
  Presentational only.
- **Markdown files** render by default (`.md`/`.markdown`). A rendered-block comment
  anchors to that block's **real source line range**, so its CSV row looks like any
  other line comment. A "Rendered ⇄ Raw" toggle switches to source lines. Embedded raw
  HTML is not rendered (safe by default).
- **Deleted files** (in base, absent from the working tree) are omitted from the file
  list. Use a different `--base` to review deletions.
- **Binary files** render as "Binary file — cannot display"; treat any binary-file CSV
  rows as informational.
- **Very large files** (>1 MB) render with a "file too large to review" placeholder;
  comments are still accepted but the agent saw only the placeholder — be conservative.
