# prereview reference

Companion to [SKILL.md](./SKILL.md). SKILL.md tells the LLM **what to do**;
this file is the LLM's lookup for **what every value means**.

## CLI flags

| Flag | Default | Required | Description |
|---|---|---|---|
| `--repo` | `.` | yes (for skill use) | Absolute path to the git repository to review. The skill should always pass `--repo "$(pwd)"` to be explicit. |
| `--base` | `HEAD` | no | Git base for diff comparison. `HEAD` = working tree vs last commit; `main` = branch-vs-trunk; `HEAD~3` = last-3-commits view; any rev-spec git accepts works. |
| `--port` | `0` | no | TCP port to listen on. `0` = OS-assigned (random free port — what the skill should normally use to avoid collisions). |
| `--host` | `127.0.0.1` | no | Host/IP to bind on. Default localhost-only. `0.0.0.0` exposes on the LAN (useful for iPhone-over-Tailscale testing). |
| `--skill` | `false` | yes (for skill use) | Show the "Hand off → Claude" top-bar button (writes `.prereview/DONE` on click) instead of "Quit" (gracefully shuts down). Without `--skill`, no DONE marker is ever written and the skill's poll loop never terminates. |
| `--version` | — | no | Print build version and exit. |

## stdout protocol

On startup, prereview prints **one line** to stdout:

```
READY http://<host>:<port>
```

This is the only line the skill should look for. Subsequent output is
slog-formatted (timestamp + level + key-value pairs) and goes to stderr.

If the bind fails or the repo path is invalid, prereview exits non-zero
without printing `READY`.

## Exit codes

| Code | Cause |
|---|---|
| `0` | Graceful shutdown via Quit (standalone) or SIGINT/SIGTERM |
| `1` | Argument validation failed (missing repo, port already in use, etc.) |
| `1` | Runtime error during shutdown |

The skill should `kill %1` rather than relying on the binary to exit
on its own — Hand off doesn't shut the server down, it just writes the
DONE marker.

## Filesystem layout

Everything prereview writes lives under `<repo>/.prereview/`:

```
<repo>/
└── .prereview/
    ├── comments.csv     ← source of truth, rewritten atomically on every change
    └── DONE             ← written ONLY on "Hand off → Claude" (skill mode); contents = absolute path to comments.csv
```

`.prereview/` is created on first comment if it doesn't exist. Add it to
the repo's `.gitignore` (the skill should not commit reviews).

## CSV schema

File: `<repo>/.prereview/comments.csv`. RFC-4180 quoted; encoding is UTF-8.

### Header (load-bearing — column order is the contract)

```
id,file,from_line,to_line,side,body,created_at,resolved
```

### Column details

| Column | Type | Example | Notes |
|---|---|---|---|
| `id` | string (ULID) | `01HMXFGB3PQT8VN7R6W4ZK2YHE` | Opaque, unique per comment. Don't parse for meaning. |
| `file` | string (relative path) | `controller.go`, `internal/foo/bar.go` | Always relative to `--repo`. Forward slashes regardless of OS. |
| `from_line` | int (1-based) | `42` | First line of the comment range. |
| `to_line` | int (1-based) | `42`, `48` | Last line (inclusive). Equal to `from_line` for single-line comments. |
| `side` | enum | `new`, `old` | Which side of the diff the lines are on. `new` = post-change content. `old` = pre-change (deleted lines from the base). Most comments are on `new`. |
| `body` | string | `"Why no error wrap?"` | RFC-4180 quoted; newlines preserved inside the quoted string. |
| `created_at` | RFC-3339 UTC | `2026-05-13T14:23:11Z` | Set once on comment creation; unchanged on edit. |
| `resolved` | bool | `true`, `false` | Lowercase. `true` = human marked the comment as already addressed; **skip these as directives**. |

### Parsing example

Use Go's `encoding/csv` or any RFC-4180-compliant parser. Don't
hand-split on commas — `body` can contain commas, newlines, and quotes.

```go
r := csv.NewReader(f)
rows, _ := r.ReadAll()  // rows[0] is the header
for _, row := range rows[1:] {
    if row[7] == "true" { continue } // skip resolved
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
- **File-list scope.** By default the drawer lists only files that differ from the base (the common review case, and the only sane default on a large repo). A "Changed N · show all M" toggle at the top of the drawer switches to the full tracked-file list. When *no* file differs from the base (clean tree) the scope automatically falls back to all files so the list is never empty, and the toggle is hidden. Comment processing is unaffected — the CSV contains whatever the user commented on, changed or not.
- **Diff vs File view.** The viewer has two modes (toggle in the top bar). *Diff* (default) shows only changed hunks — changed lines plus 3 lines of context, with long unchanged runs collapsed to a "··· N unchanged lines ···" marker. *File* shows the entire current file, no diff, deletions omitted. Line numbers are identical in both, so a comment anchors to the same line regardless of which mode it was made in. This is presentational only; it doesn't affect the CSV.
- **Unchanged files**, when shown (toggle set to "all", or clean-tree fallback), appear with no badge. They render as plain context lines; comments on them anchor to the working-tree line numbers.
- **Deleted files** (in base, absent from working tree) are omitted from the file list — there's nothing to scroll through. Use a different `--base` if you specifically need to review deletions.
- **Binary files** render as "Binary file — cannot display". The skill should treat binary file rows in the CSV (if any) as informational, not actionable.
- **Very large files** (>1 MB) render with a "file too large to review" placeholder rather than the full content. Comments on those files are still accepted (anchored to line numbers the user knows), but the skill should be conservative — the diff context the LLM saw is just the placeholder.
- **Resolved comments** stay in the CSV with `resolved=true`. The skill should skip them as directives but may include them as context (e.g., "the user has already addressed this similar concern").
