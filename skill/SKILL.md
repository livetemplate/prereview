---
name: prereview
description: Launch an interactive review session over the working tree (or any file/directory) — code diffs, Markdown, HTML, images, and live local sites, by line, block, or region. The user leaves comments in a browser; you read them from a CSV when they hit "Hand off → Claude" and apply the fixes.
triggers:
  - prereview
  - review my changes
  - review this file
  - review before push
  - leave comments on diff
  - review before commit
---

# prereview

Launches a web UI for the user to review the working tree (or any file/directory) and leave comments — on a diff line or range, a rendered Markdown/HTML block, a region of an image, or a box on a live local site (`--external`). Binding is automatic: `127.0.0.1` on a local machine, and — on a remote (SSH) box — this host's **Tailscale IP**, so the user can reach it from a phone over the tailnet without exposing it publicly (`--host` overrides). The UI shows a **"Hand off → Claude"** button when launched with `--skill`; clicking it writes `.prereview/DONE` containing the path to the CSV. Poll for that file, then read the CSV.

## Usage

### 1. Launch the binary in the background, with `--skill`

```bash
cd <repo>
prereview --skill "$(pwd)" &
# stdout: READY http://127.0.0.1:PORT          (or http://100.x.y.z:PORT on a remote box)
#         ALT   http://host.tailnet.ts.net:PORT   (0+ extra reachable URLs; only on a tailnet)
#         REPO  /abs/dir/whose/.prereview/holds/the/CSV+DONE
```

The `--skill` flag is critical — without it the UI shows a "Quit" button instead, and no DONE marker is ever written.

The review path is the **positional argument** (here `"$(pwd)"`); it defaults to the current directory if omitted. Flags must come **before** the path (Go's flag parser stops at the first non-flag). `--base` defaults to `HEAD` (working tree vs last commit); pass `--base main` (before the path) for branch-vs-base review, `--base HEAD~3` for last-3-commits review, etc.

After `READY`, prereview prints a second line `REPO <dir>` — the directory whose `.prereview/` holds the CSV and DONE marker. **Always poll/read relative to that `REPO` directory**, not the raw path argument: they're identical for a git repo, but differ for a single file (see below).

**Reviewing files outside a git repo** (e.g. a Claude plan, a loose doc): the path accepts more than a git repo. Pass a **single file** or a **non-git directory** and prereview reviews it with no diff — every line is "new" and commentable, the base picker is hidden, `--base` is ignored:

```bash
prereview --skill ~/.claude/plans/some-plan.md &   # one file
prereview --skill ~/.claude/plans &                 # whole dir, recursively
```

For a single file the `.prereview/` store lives in the file's **parent** directory (so sibling files in that directory share one `comments.csv`, disambiguated by the `file` column) — which is exactly what the printed `REPO` line points at. Re-anchoring works the same as for git files: if an LLM rewrites the doc before the user hands off, comments follow their sentences. The clean-tree / restart-fresh guidance below applies unchanged (skip the `git` probe when there's no git repo).

**Already running for this repo? Restart it fresh.** If a prereview `--skill` server is already running for this same repo (you launched one earlier this session, or the user re-invoked the skill), do **not** start a second one — duplicate servers fight over the same `.prereview/comments.csv`. Stop the existing one *for this repo* and relaunch:

```bash
pgrep -af "prereview --skill.*$repo" | awk '{print $1}' | xargs -r kill
rm -f "$REPO/.prereview/DONE"
```

`$repo` in the `pgrep` match is the literal path argument the server was launched with (a path, a dir, or a single file — it matches the process's own argv); `$REPO` in the `rm` is the printed `REPO` directory (the file's parent for a single-file review). The `prereview --skill.*$repo` match targets only this repo's server — unrelated prereview servers (a different repo, test leftovers) are left alone, and it avoids the `pkill -f prereview` self-match trap. Comments are auto-saved (and the current composer draft is persisted), so killing the running server loses nothing. Removing a stale `DONE` marker keeps the fresh server from looking already-handed-off.

**Clean working tree → review the whole branch.** Before launching, if you did *not* set an explicit `--base` (so it would default to `HEAD`) and `git status --porcelain` is empty, the `HEAD` diff is empty and the session has nothing to review. In that case launch with the empty tree as the base so every file on the current branch appears as added and any line is commentable:

```bash
base=HEAD
[ -z "$(git -C "$repo" status --porcelain)" ] && base="$(git -C "$repo" hash-object -t tree /dev/null)"
prereview --skill --base "$base" "$repo" &
```

An explicitly requested base (`--base main`, `HEAD~3`, a tag, …) is always honored as-is — never override it, even if its file list happens to be empty.

### 2. Tell the user to review — with a **clickable** link

The first stdout line is `READY <url>` — the canonical, always-reachable URL (loopback locally; the Tailscale IP on a remote box). Zero or more `ALT <url>` lines may follow with friendlier equivalents, notably the MagicDNS hostname.

Present the URL as a **Markdown link**, never bare text. The user is frequently on the mobile Claude app, where a bare URL can't be tapped and copy-pasting is painful — a `[url](url)` link is one tap:

> I've opened a review session — tap to open: **[http://100.x.y.z:PORT](http://100.x.y.z:PORT)**
> (hostname: [http://host.tailnet.ts.net:PORT](http://host.tailnet.ts.net:PORT))
> Click **"Hand off → Claude"** when you're done and I'll read your comments.

When an `ALT` MagicDNS hostname is present, make **that** the headline link (stable and readable); otherwise use the `READY` URL. Always wrap as `[url](url)` — including any `ALT` you also surface. Comments auto-save on every add/edit/delete — the user doesn't need to "save" before handing off.

### 3. Poll for the DONE marker

`<REPO>` below is the directory from the printed `REPO` line (equals the path argument for a git repo; the file's parent directory for a single-file review):

```bash
while [ ! -f <REPO>/.prereview/DONE ]; do sleep 1; done
csv_path=$(cat <REPO>/.prereview/DONE)
```

### 4. Read the CSV

```bash
cat "$csv_path"
```

**Columns** (load-bearing — order is the contract):

```
id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url
```

- `id`: opaque per-comment identifier
- `from_line` / `to_line`: 1-indexed; equal for single-line comments. **`0` when `kind=file`, `kind=area`, or `kind=region`** (no line anchor).
- `side`: `new` | `old` (which side of the diff the line is on). Empty when `kind=file`, `kind=area`, or `kind=region`.
- `body`: RFC-4180 quoted; preserves newlines
- `created_at`: RFC-3339 UTC
- `resolved`: `true` | `false` — see "Resolved comments" below
- `anchor`: internal JSON fingerprint — **do not parse or act on it**. Empty when `kind=file`, `kind=area`, or `kind=region`.
- `anchor_status`: `ok` | `moved` | `outdated` | *(empty)* — see "Re-anchoring" below. Always empty for `kind=file` / `kind=area` / `kind=region` (nothing to drift).
- `kind`: `line` | `file` | `area` | `region` | *(empty)* — see "Comment kinds" below. Empty means `line` for pre-migration rows.
- `area`: JSON blob `{"x":0.1,"y":0.2,"w":0.3,"h":0.15}` (0..1 fractions) when `kind=area` (of the image) or `kind=region` (of the live page's document); empty otherwise. See "Comment kinds" below.
- `url`: the proxied page (app-relative, e.g. `/pricing`) when `kind=region` (`--external` live-site review); empty for every file-based kind.
- Older CSVs may have fewer trailing columns (7–12); index by position and default missing ones to empty.

Multi-line bodies use standard CSV quoting; use `encoding/csv` or any RFC-4180-compliant parser.

### Comment kinds

- `line` (or empty for legacy rows) — comment is anchored to a line range
  via `from_line` / `to_line` / `side`. Treat as before: edit the named
  file at those lines per the comment body.
- `file` — comment applies to the whole file. `from_line` / `to_line` are
  both `0` and `anchor` / `anchor_status` are empty; don't look at them.
  Treat the comment as guidance for the entire file (e.g., "rename to
  foo.go", "this binary shouldn't be in the PR", "good test coverage"):
  act on the file as a whole, or, if the body suggests a specific edit,
  use semantic understanding of the body to locate where in the file to
  apply it.
- `area` — comment overlays a rectangular region on a binary image
  (PNG, JPEG, SVG, etc.). `from_line` / `to_line` / `side` / `anchor` /
  `anchor_status` are all zero/empty; the `area` column (#12) holds a
  JSON blob `{"x":0.1,"y":0.2,"w":0.3,"h":0.15}` where each value is a
  0..1 fraction of the image's natural dimensions. Treat the body as
  guidance for that rectangular region (e.g., "this label is wrong",
  "this part of the diagram needs to be re-drawn"). Convert fractions
  to pixels by multiplying by the image's natural width / height — the
  image file itself is the source of truth for those numbers, so
  re-encoding at different dimensions still highlights the same
  logical region.
- `region` — comment overlays a rectangle on a **live page** from
  `--external` live-site review. It points at a URL + page region, not a
  file: the `file` column is empty and there are no line numbers, so
  treat it as context/feedback about a page, not an actionable file edit.

### Resolved comments

A comment marked `resolved=true` is one the human reviewer has already
declared addressed (e.g., they fixed it themselves, or it no longer
applies). **The skill should skip resolved rows** — treat them as
historical context, not actionable directives. Only act on rows with
`resolved=false`.

### Re-anchoring

prereview captures a content fingerprint per comment and re-locates it
if the doc changes (including edits *you* make before the user hands
off — relocation also runs on hand-off, so the CSV you receive is
already re-anchored):

- `ok` / empty — line numbers are trustworthy.
- `moved` — the doc changed; prereview already auto-corrected
  `from_line`/`to_line` to follow the content. Still trustworthy.
- `outdated` — the anchored content changed or vanished and prereview
  could **not** confidently re-place it; `from_line`/`to_line` are
  stale. **Skip these like `resolved=true`** — don't act on the line
  numbers (use `body` as context only).

### 5. Act on the comments

Process each row where `resolved=false` **and `anchor_status` is not `outdated`** as a directive: edit the named file at the given lines per the comment body. Skip rows with `resolved=true` (already addressed by the human) and rows with `anchor_status=outdated` (line numbers no longer reliable — the human must re-anchor them in the UI). Both are kept as historical record. After all actionable comments are processed, optionally clean up:

```bash
kill %1                  # stop the background prereview server
rm <REPO>/.prereview/DONE   # so the next session starts fresh
```

## Streaming handoff (`--stream`) — multi-round, no re-invocation

The default flow above is **one-shot**: poll `DONE` once, read the CSV, act,
stop. For an **iterative** review — the user leaves comments, you address them,
the user reviews your work and leaves more — launch with `--stream` instead.
prereview then emits a continuous JSON event log you consume across many rounds
until the user explicitly ends the session. Bonus: the events are ready-to-use
JSON, so you **never hand-write a CSV parser**.

### Launch

```bash
prereview --skill --stream "$(pwd)" &
# stdout: the same READY/ALT/REPO preamble as above, then JSON event lines:
#         {"event":"ready","seq":0,...}
```

`--stream` implies `--skill`. The UI shows two buttons: **Hand off →** (send the
current comments as a round; the session stays open) and **End session**
(finish). Tell the user this two-button flow up front: Hand off after each batch
(you'll process it and report back), End session when they're fully done.

### Consume the stream — block for each event, exit ONLY on `session_end`

Events are appended to `<REPO>/.prereview/events.jsonl` (one JSON object per
line; the same events also print to stdout). Read the **next** event with a
**blocking** read so you wait across rounds without busy-polling — a single
tool call that returns the instant the user clicks Hand off (or End session),
and otherwise just waits:

```bash
# n = next line to read (1-based); start at 1, increment after each event.
tail -n +"$n" -f <REPO>/.prereview/events.jsonl | head -n 1
```

`head -n 1` returns as soon as line `n` exists — immediately if it's already
there (catch-up / replay after a context reset), or whenever the user's next
click appends it — then exits and stops `tail`. Parse the returned JSON,
increment `n`, and read the next one. Run it with a **long Bash timeout** (e.g.
600s); if it times out because the user is idle, just re-issue it for the same
`n`. **The loop's only exit is `{"event":"session_end"}`** — do **not** stop
after the first handoff; a review is iterative.

- `{"event":"ready",...}` — session is live; tell the user to review.
- `{"event":"handoff","seq":N,"comments":[…]}` — the user clicked Hand off.
  Process `comments`, then tell the user this round is done and to leave more or
  click **End session**. Each handoff is a **full snapshot** of everything still
  actionable, so **dedupe by `id`** across rounds — a comment reappears only
  while it's unresolved. `comments` is always present (`[]` when nothing is
  actionable).
- `{"event":"session_end","seq":N}` — stop consuming. The review is over (the
  server also exits right after, so a backgrounded launch's job completes too).

Each comment in a handoff carries `id`, `kind`, `file`, `from_line`, `to_line`,
`side`, `body`, `url`, `area` (nested object or `null`), `created_at`,
`anchor_status`. The snapshot is already filtered to actionable rows (no
`resolved=true`, no `anchor_status=outdated`) and omits the opaque `anchor`
fingerprint. Interpret `kind` exactly as in the CSV section above
(`line`/`file`/`area`/`region`).

## Modes

- **Skill mode** (`--skill`): top-bar button is "Hand off → Claude". Clicking writes `.prereview/DONE` and keeps the server running so the user can keep editing.
- **Stream mode** (`--stream`, implies `--skill`): two buttons — "Hand off →" (emits a `handoff` JSON event each click; session stays open) and "End session" (emits `session_end` and shuts the server down). Consume the JSON event stream continuously until `session_end` — see "Streaming handoff" above.
- **Standalone** (no `--skill`): top-bar button is "Quit", which gracefully shuts the server down. No DONE marker. Comments are still auto-saved to `.prereview/comments.csv` — the user can read them manually or invoke the skill later to process them.

## Notes

- The CSV at `.prereview/comments.csv` is the source of truth and is rewritten atomically on every change. Reading it at any time is safe.
- Untracked files appear in the file list as added (`[A]`). Commenting on them works the same as on tracked files.
- Non-git targets (single file or non-git directory) show every file as added (`[A]`) — there is no diff and no base. `--base` is ignored and the base picker is hidden; everything else (comments, CSV, re-anchoring, hand-off) works identically.
- Binding is automatic: `127.0.0.1` on a local machine; on a remote (SSH) box with no explicit `--host`, this host's **Tailscale IP**, so a phone on the tailnet can reach it without exposing the source diff on the public internet. **Do not pass `--host 0.0.0.0` as a "make it reachable" shortcut** — that binds every interface, including any public IP. If a remote box has no tailnet, prereview stays on `127.0.0.1` and prints a warning telling you to pass an explicit `--host` (e.g. a private LAN IP).
