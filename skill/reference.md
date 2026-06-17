# prereview reference

Companion to [SKILL.md](./SKILL.md). SKILL.md tells the LLM **what to do**;
this file is the LLM's lookup for **what every value means**.

## CLI flags

| Flag | Default | Required | Description |
|---|---|---|---|
| `[path]` (positional) | `.` | recommended (for skill use) | Path to review — the trailing positional argument, e.g. `prereview --skill "$(pwd)"`. Must come **after** all flags. Usually a git repository; may also be a **single file** or a **non-git directory** (e.g. a Claude plan) → no-git mode: no diff, no base, every line "new". When it's a single file the review root is the file's **parent** directory. |
| `--base` | `HEAD` | no | Git base for diff comparison. `HEAD` = working tree vs last commit; `main` = branch-vs-trunk; `HEAD~3` = last-3-commits view; any rev-spec git accepts works. **Ignored in no-git mode** (single file / non-git dir — there is no base). |
| `--port` | `0` | no | TCP port to listen on. `0` = OS-assigned (random free port — what the skill should normally use to avoid collisions). |
| `--host` | `127.0.0.1` | no | Host/IP to bind on. **Auto-resolved when not set explicitly:** a remote (SSH) box with a tailnet binds its Tailscale IP (phone-reachable, never public); a remote box with no tailnet stays `127.0.0.1` and prints a stderr warning; local stays `127.0.0.1` (unchanged). An explicit value is an absolute override and is never auto-rebound — avoid `0.0.0.0`, which exposes the source diff on every interface including any public IP. |
| `--skill` | `false` | yes (for skill use) | Show the "Hand off → Claude" top-bar button (writes `.prereview/DONE` on click) instead of "Quit" (gracefully shuts down). Without `--skill`, no DONE marker is ever written and the skill's poll loop never terminates. |
| `--external` | — | no | Annotate a live local website instead of files: reverse-proxies the URL on a second origin and overlays region annotation. Requires `--out`; ignores `[path]`/`--base`. |
| `--out` | the review path | with `--external` | Directory whose `.prereview/` holds comments.csv + DONE. Defaults to the review path; required with `--external`. |
| `--stream` | `false` | no | Emit a continuous JSON event stream for an LLM consumer (stdout + `.prereview/events.jsonl`): each Hand off emits a `handoff` snapshot, the new **End session** button emits a terminating `session_end`. **Implies `--skill`.** See [Stream mode](#stream-mode---stream). |
| `--version` | — | no | Print build version and exit. |

## stdout protocol

On startup, prereview prints these lines to stdout, in order:

```
READY http://<host>:<port>
ALT   http://<host>:<port>     (zero or more; only when on a tailnet)
PROXY http://<host>:<port>     (external mode only; the proxy origin the UI frames)
REPO  <absolute review-root directory>
```

`READY` is the **first line** and carries the canonical, always-reachable
URL (loopback locally; the Tailscale IP on a remote box). It is the only
line the skill and the e2e harness machine-parse — match the literal
prefix `READY ` and take the rest. Zero or more `ALT` lines follow with
additional reachable forms (chiefly the MagicDNS hostname, friendlier to
tap on a phone); they are purely additive and parsers may ignore them.
In **external mode** (`--external`) one extra `PROXY <url>` line follows
the `ALT` lines (before `REPO`): it's the proxy origin the UI frames.
It's additive too — parsers may ignore it, and `READY` remains the
canonical UI url (not the proxy).
`REPO` is the directory whose `.prereview/` holds `comments.csv` and
`DONE` — equal to the path argument for a git repo or non-git
directory, and the file's **parent** directory for a single-file review.
Poll and clean up relative to the `REPO` line, not the raw path
argument. All other output is slog-formatted (timestamp + level +
key-value pairs) and goes to stderr — including the "remote box, no
tailnet" fallback warning.

In **stream mode** (`--stream`), after the preamble prereview emits one JSON
object per line (JSONL) to stdout — a `ready` event, then a `handoff` event per
Hand off click, then a single `session_end` — and mirrors the same lines to
`<REPO>/.prereview/events.jsonl`. The plaintext `READY ` line still comes
first, so the existing preamble parse is unchanged (JSON lines start with `{`).
See [Stream mode](#stream-mode---stream) for the event schema.

If the bind fails or the path is invalid (missing, unreadable),
prereview exits non-zero without printing `READY`.

## Exit codes

| Code | Cause |
|---|---|
| `0` | Graceful shutdown via Quit (standalone), End session (stream), or SIGINT/SIGTERM |
| `1` | Argument validation failed (missing repo, port already in use, etc.) |
| `1` | Runtime error during shutdown |

The skill should `kill %1` rather than relying on the binary to exit
on its own — Hand off doesn't shut the server down, it just writes the
DONE marker. (The exception is **stream mode**: clicking **End session**
emits `session_end` and *does* shut the server down, so the background
job exits on its own.)

## Filesystem layout

Everything prereview writes lives under `<REPO>/.prereview/`, where
`<REPO>` is the directory from the stdout `REPO` line:

```
<REPO>/
└── .prereview/
    ├── comments.csv     ← source of truth, rewritten atomically on every change
    ├── DONE             ← written ONLY on "Hand off → Claude" (skill mode); contents = absolute path to comments.csv
    └── events.jsonl     ← stream mode only (--stream); append-only JSON event log, reset each launch
```

For a git repo or non-git directory `<REPO>` is the path argument.
For a single-file review `<REPO>` is the file's **parent** directory, so
sibling files reviewed from that directory share one `comments.csv`
(the `file` column disambiguates rows). `.prereview/` is created
eagerly on startup. Add it to the repo's `.gitignore` (the skill should
not commit reviews).

## CSV schema

File: `<repo>/.prereview/comments.csv`. RFC-4180 quoted; encoding is UTF-8.

### Header (load-bearing — column order is the contract)

```
id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url
```

Older CSVs may have 7–12 columns (pre-`resolved`/`anchor`/`anchor_status`/`kind`/`area`/`url`);
columns 0–7 are stable, so index by position and treat missing trailing
columns as empty/default. Columns are only ever appended.

### Column details

| Column | Type | Example | Notes |
|---|---|---|---|
| `id` | string (ULID) | `01HMXFGB3PQT8VN7R6W4ZK2YHE` | Opaque, unique per comment. Don't parse for meaning. |
| `file` | string (relative path) | `controller.go`, `internal/foo/bar.go` | Relative to the review root (the `REPO` directory). For a single-file review this is just the file's basename. Forward slashes regardless of OS. |
| `from_line` | int (1-based) | `42` | First line of the comment range. |
| `to_line` | int (1-based) | `42`, `48` | Last line (inclusive). Equal to `from_line` for single-line comments. |
| `side` | enum | `new`, `old` | Which side of the diff the lines are on. `new` = post-change content. `old` = pre-change (deleted lines from the base). Most comments are on `new`. |
| `body` | string | `"Why no error wrap?"` | RFC-4180 quoted; newlines preserved inside the quoted string. |
| `created_at` | RFC-3339 UTC | `2026-05-13T14:23:11Z` | Set once on comment creation; unchanged on edit. |
| `resolved` | bool | `true`, `false` | Lowercase. `true` = human marked the comment as already addressed; **skip these as directives**. |
| `anchor` | JSON string | `{"text":"…","before":[…],"after":[…]}` | **Internal — do not parse or act on.** The content fingerprint prereview uses to re-locate a comment when the doc changes. May be empty for legacy rows. |
| `anchor_status` | enum | `ok`, `moved`, `outdated`, *(empty)* | `ok`/empty = line numbers are trustworthy. `moved` = the doc was edited and prereview already auto-corrected `from_line`/`to_line` to follow the content (still trustworthy). `outdated` = the anchored content changed or vanished and prereview could **not** confidently re-place it — `from_line`/`to_line` are stale. **Treat `outdated` like `resolved=true`: skip as a directive** (may still use as context). |
| `kind` | enum | `line`, `file`, `area`, `region`, *(empty)* | What the comment anchors to. `line` (default; empty for pre-migration rows) = a line range. `file` = the whole file. `area` = a rectangle on a binary image. `region` = a rectangle on a live page (from `--external`). For `file`/`area`/`region` the line/side/anchor columns are empty/zero. |
| `area` | JSON string | `{"x":0.1,"y":0.2,"w":0.3,"h":0.15}` | Rectangle as 0..1 fractions — of the **image** (`kind=area`) or of the live page's **document** (`kind=region`, so a re-pin survives scroll). Empty for every other kind. |
| `url` | string (app-relative path) | `/pricing` | The proxied page for a `kind=region` comment. App-relative (the proxy port is random per run, so no absolute URL is stored). **Empty for every file-based kind.** |

### Parsing example

Use Go's `encoding/csv` or any RFC-4180-compliant parser. Don't
hand-split on commas — `body` can contain commas, newlines, and quotes.

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

## Modes

### Skill mode (`--skill`)

- Top-bar button reads **"Hand off → Claude"**
- Clicking writes `.prereview/DONE` with the absolute path to `comments.csv`
- Server keeps running after Hand off (the user can keep editing)
- Skill polls for DONE, reads CSV, optionally kills the server

### Standalone mode (no `--skill`)

- Top-bar button reads **"Quit"**
- Clicking gracefully shuts down the HTTP server
- **No DONE marker is written** — there's no skill polling
- Comments still auto-save to `comments.csv` — the user can read or
  hand off later by relaunching with `--skill`

### External mode (`--external`)

- Reverse-proxies a **live local site** (`--external <url>`) instead of
  reviewing files; the user drags a box on any page to leave a comment
- Comments are stored as `kind=region` rows: a **URL + rectangle**
  (`url` + `area` columns), not a file + line
- Region comments are **frozen** — no content re-anchoring (like `area`)
- Requires `--out` (no repo to default the store to); `[path]`/`--base`
  are ignored

### Stream mode (`--stream`)

For an iterative, multi-round handoff to an LLM. Implies `--skill` and adds an
**End session** button next to Hand off. Instead of the one-shot DONE poll,
prereview emits a JSON event log — to stdout (live) and
`<REPO>/.prereview/events.jsonl` (durable replay) — that the consumer reads
continuously until the user explicitly ends the session:

- `ready` — emitted once, after the stdout preamble.
- `handoff` — emitted on every **Hand off** click; carries the full snapshot of
  actionable comments (unresolved, non-outdated). Dedupe by `id` across rounds,
  so the human's resolve-clicks prune later rounds.
- `session_end` — emitted once on **End session**; the **only** event the
  consumer loop should treat as "stop". The server shuts down right after.

Every event has `event`, a monotonic `seq` (re-handoffs are distinguishable —
the idempotent DONE marker can't do this), and an RFC-3339 `ts`. Each comment
in a `handoff` snapshot mirrors the CSV columns **minus** the opaque `anchor`
fingerprint and `resolved` (the snapshot is already filtered to actionable
rows), with `area` as a nested object (or `null`) — no nested
JSON-in-a-string to parse:

```jsonc
{"event":"ready","seq":0,"ts":"…","repo":"/abs/dir","csv":"/abs/dir/.prereview/comments.csv"}
{"event":"handoff","seq":1,"ts":"…","comments":[
  {"id":"01J…","kind":"line","file":"main.go","from_line":42,"to_line":42,
   "side":"new","body":"rename this","url":"","area":null,
   "created_at":"…","anchor_status":"ok"}
]}
{"event":"session_end","seq":2,"ts":"…"}
```

`events.jsonl` is append-only and reset on each fresh launch (same as the DONE
marker). The CSV stays the authoritative store; the stream is a convenience
layer over it.

## Atomicity guarantees

`comments.csv` is rewritten on every add/edit/delete/resolve via:

1. Write to `comments.csv.tmp`
2. `fsync` the tmp file
3. `rename(tmp, comments.csv)` (atomic on POSIX)
4. `fsync` the parent directory

Reading the CSV at any time is safe — you'll see either the
pre-mutation or post-mutation state, never a torn write.

## Behavioral quirks

- **Untracked files** appear in the file list as added (`[A]` badge). Commenting on them works the same as on tracked files. They're rendered as if every line were a new addition.
- **No-git mode** (the path is a single file or a non-git directory). There is no git base: every file is listed as added (`[A]`), `--base` is ignored, and the base picker is hidden. A non-git directory is walked recursively, skipping `.git/`, `.prereview/`, dotfiles/dotdirs, and files over the 1 MB render cap. Everything else — comments, CSV schema, atomicity, re-anchoring on hand-off — is identical to git mode. This is the path for reviewing Claude plans and other loose docs.
- **File-list scope.** By default the drawer lists only files that differ from the base (the common review case, and the only sane default on a large repo). A "Changed N · show all M" toggle at the top of the drawer switches to the full tracked-file list. When *no* file differs from the base (clean tree) the scope automatically falls back to all files so the list is never empty, and the toggle is hidden. Comment processing is unaffected — the CSV contains whatever the user commented on, changed or not.
- **Diff vs File view.** The viewer has two modes (toggle in the top bar). *Diff* (default) shows only changed hunks — changed lines plus 3 lines of context, with long unchanged runs collapsed to a "··· N unchanged lines ···" marker. *File* shows the entire current file, no diff, deletions omitted. Line numbers are identical in both, so a comment anchors to the same line regardless of which mode it was made in. This is presentational only; it doesn't affect the CSV.
- **Markdown files** render by default (`.md`/`.markdown`). The reviewer can comment on a rendered block (heading, paragraph, list, code fence…); the comment anchors to that block's **real source line range**, so CSV rows for Markdown look exactly like any other line comment (`from_line`/`to_line` are true source lines) and are interchangeable with comments made in the raw view. A "Rendered ⇄ Raw" toggle switches to the source line view. Embedded raw HTML is not rendered (safe by default). Nothing about this changes the CSV contract.
- **Unchanged files**, when shown (toggle set to "all", or clean-tree fallback), appear with no badge. They render as plain context lines; comments on them anchor to the working-tree line numbers.
- **Deleted files** (in base, absent from working tree) are omitted from the file list — there's nothing to scroll through. Use a different `--base` if you specifically need to review deletions.
- **Binary files** render as "Binary file — cannot display". The skill should treat binary file rows in the CSV (if any) as informational, not actionable.
- **Region comments** (`kind=region`, from `--external`) reference a **URL + rectangle**, not a file + line — the `file` column is empty and there are no line numbers. Treat them as **informational/context** (a human's visual concern about a page), not as a file-edit directive.
- **Very large files** (>1 MB) render with a "file too large to review" placeholder rather than the full content. Comments on those files are still accepted (anchored to line numbers the user knows), but the skill should be conservative — the diff context the LLM saw is just the placeholder.
- **Resolved comments** stay in the CSV with `resolved=true`. The skill should skip them as directives but may include them as context (e.g., "the user has already addressed this similar concern").
- **Comment re-anchoring.** prereview captures a content fingerprint when a comment is made. If the doc is edited afterwards (including by *you*, between writing the file and the user handing off), prereview re-locates each comment on the live file: it auto-corrects `from_line`/`to_line` and marks `anchor_status=moved` when it can do so confidently, or marks `anchor_status=outdated` (line numbers left stale) when it cannot. **The handed-off CSV is already re-anchored** (relocation runs on hand-off). So: trust `ok`/`moved` line numbers; treat `outdated` like resolved — don't act on its line numbers (the user must re-anchor it in the UI, or you may use its `body` as context only).
