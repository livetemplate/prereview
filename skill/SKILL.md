---
name: prereview
description: Launch an interactive per-line code review session over the working tree. The user leaves comments in a browser; you read them from a CSV when they hit "Hand off → Claude".
triggers:
  - prereview
  - review my changes
  - per-line code review
  - leave comments on diff
  - review before commit
---

# prereview

Launches a local web UI bound to `127.0.0.1` (or the configured host) so the user can leave per-line comments on the working-tree diff. The UI shows a **"Hand off → Claude"** button when launched with `--skill`; clicking it writes `.prereview/DONE` containing the path to the CSV. Poll for that file, then read the CSV.

## Usage

### 1. Launch the binary in the background, with `--skill`

```bash
cd <repo>
prereview --skill --repo "$(pwd)" --base HEAD &
# stdout: READY http://127.0.0.1:PORT
```

The `--skill` flag is critical — without it the UI shows a "Quit" button instead, and no DONE marker is ever written.

`--base` defaults to `HEAD` (working tree vs last commit). Pass `--base main` for branch-vs-base review, `--base HEAD~3` for last-3-commits review, etc.

### 2. Tell the user to review

> "I've opened a review session at <url>. Click 'Hand off → Claude' when you're done and I'll read your comments."

Comments auto-save on every add/edit/delete — the user doesn't need to "save" before handing off.

### 3. Poll for the DONE marker

```bash
while [ ! -f <repo>/.prereview/DONE ]; do sleep 1; done
csv_path=$(cat <repo>/.prereview/DONE)
```

### 4. Read the CSV

```bash
cat "$csv_path"
```

**Columns** (load-bearing — order is the contract):

```
id,file,from_line,to_line,side,body,created_at
```

- `id`: opaque per-comment identifier
- `from_line` / `to_line`: 1-indexed; equal for single-line comments
- `side`: `new` | `old` (which side of the diff the line is on)
- `body`: RFC-4180 quoted; preserves newlines
- `created_at`: RFC-3339 UTC

Multi-line bodies use standard CSV quoting; use `encoding/csv` or any RFC-4180-compliant parser.

### 5. Act on the comments

Process each row as a directive: edit the named file at the given lines per the comment body. After all comments are processed, optionally clean up:

```bash
kill %1                 # stop the background prereview server
rm <repo>/.prereview/DONE  # so the next session starts fresh
```

## Modes

- **Skill mode** (`--skill`): top-bar button is "Hand off → Claude". Clicking writes `.prereview/DONE` and keeps the server running so the user can keep editing.
- **Standalone** (no `--skill`): top-bar button is "Quit", which gracefully shuts the server down. No DONE marker. Comments are still auto-saved to `.prereview/comments.csv` — the user can read them manually or invoke the skill later to process them.

## Notes

- The CSV at `.prereview/comments.csv` is the source of truth and is rewritten atomically on every change. Reading it at any time is safe.
- Untracked files appear in the file list as added (`[A]`). Commenting on them works the same as on tracked files.
- The server binds to `127.0.0.1` by default. Pass `--host 0.0.0.0` to expose it on the LAN (useful for Tailscale-via-phone testing).
