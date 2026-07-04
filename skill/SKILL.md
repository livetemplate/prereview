---
name: prereview
description: Launch an interactive review session over the working tree (or any file/directory) — code diffs, Markdown, HTML, images, and live local sites, by line, block, or region. The user leaves comments in a browser and hands off batches to you; you read each batch and apply the fixes. You can also submit suggested edits (`prereview suggest`) that render inline for the user to accept / reject / revise.
triggers:
  - prereview
  - review my changes
  - review this file
  - review before push
  - leave comments on diff
  - review before commit
  - suggest edits in prereview
  - review the doc and suggest edits
---

# prereview

Launches a web UI for the user to review the working tree (or any file/directory) and leave comments — on a diff line or range, a rendered Markdown/HTML block, a region of an image, or a box on a live local site (`--external`). Binding is automatic: `127.0.0.1` on a local machine, and — on a remote (SSH) box — this host's **Tailscale IP**, so the user can reach it from a phone over the tailnet without exposing it publicly (`--host` overrides).

**Default to streaming (`--stream`).** It's the one way to read comments for skill use: the UI shows two buttons — **Hand off →** (send the current comments and keep reviewing) and **End session** (finish) — and prereview emits a JSON event you consume on each Hand off. This is iterative (the user can review your work and leave more) and gives you ready-to-use JSON, so you **never hand-write a CSV parser**. The CSV at `.prereview/comments.csv` stays the on-disk source of truth; a non-streaming one-shot fallback exists (see [Fallback](#fallback-one-shot-handoff---skill-without---stream)) but is not the default — don't present the two as co-equal options.

## Usage

### 1. Launch the binary in the background, with `--stream`

```bash
cd <repo>
prereview --stream "$(pwd)" &
# stdout: READY http://127.0.0.1:PORT          (or http://100.x.y.z:PORT on a remote box)
#         ALT   http://host.tailnet.ts.net:PORT   (0+ extra reachable URLs; only on a tailnet)
#         REPO  /abs/dir/whose/.prereview/holds/the/CSV+events
#         {"event":"ready","seq":0,...}             (then JSON event lines)
```

`--stream` **implies `--skill`** — the UI streams comments to you continuously (a **⏸ Pause / ▶ Resume** toggle holds/releases the queue) and offers **End session**. (Without either flag the UI shows a "Quit" button and nothing is emitted — that's standalone mode, not for skill use.)

The review path is the **positional argument** (here `"$(pwd)"`); it defaults to the current directory if omitted. Flags must come **before** the path (Go's flag parser stops at the first non-flag). `--base` defaults to `HEAD` (working tree vs last commit); pass `--base main` (before the path) for branch-vs-base review, `--base HEAD~3` for last-3-commits review, etc.

After `READY`, prereview prints a second line `REPO <dir>` — the directory whose `.prereview/` holds the CSV, the event log, and the DONE marker. **Always poll/read relative to that `REPO` directory**, not the raw path argument: they're identical for a git repo, but differ for a single file (see below).

**Reviewing files outside a git repo** (e.g. a Claude plan, a loose doc): the path accepts more than a git repo. Pass a **single file** or a **non-git directory** and prereview reviews it with no diff — every line is "new" and commentable, the base picker is hidden, `--base` is ignored:

```bash
prereview --stream ~/.claude/plans/some-plan.md &   # one file
prereview --stream ~/.claude/plans &                 # whole dir, recursively
```

For a single file the `.prereview/` store lives in the file's **parent** directory (so sibling files in that directory share one `comments.csv`, disambiguated by the `file` column) — which is exactly what the printed `REPO` line points at. Re-anchoring works the same as for git files: if an LLM rewrites the doc before the user hands off, comments follow their sentences. The clean-tree / restart-fresh guidance below applies unchanged (skip the `git` probe when there's no git repo).

**Already running for this repo? Restart it fresh.** If a prereview skill server is already running for this same repo (you launched one earlier this session, or the user re-invoked the skill), do **not** start a second one — duplicate servers fight over the same `.prereview/comments.csv`. Stop the existing one *for this repo* and relaunch:

```bash
pgrep -af "prereview .*$repo" | awk '{print $1}' | xargs -r kill
rm -f "$REPO/.prereview/DONE"
```

`$repo` in the `pgrep` match is the literal path argument the server was launched with (a path, a dir, or a single file — it matches the process's own argv); `$REPO` in the `rm` is the printed `REPO` directory (the file's parent for a single-file review). Matching on `$repo` targets only this repo's server — unrelated prereview servers (a different repo, test leftovers) are left alone, and it avoids the `pkill -f prereview` self-match trap. Comments are auto-saved (and the current composer draft is persisted), so killing the running server loses nothing. The event log resets on each fresh launch, and removing a stale `DONE` marker keeps a one-shot fallback server from looking already-handed-off.

**Clean working tree → review the whole branch.** Before launching, if you did *not* set an explicit `--base` (so it would default to `HEAD`) and `git status --porcelain` is empty, the `HEAD` diff is empty and the session has nothing to review. In that case launch with the empty tree as the base so every file on the current branch appears as added and any line is commentable:

```bash
base=HEAD
[ -z "$(git -C "$repo" status --porcelain)" ] && base="$(git -C "$repo" hash-object -t tree /dev/null)"
prereview --stream --base "$base" "$repo" &
```

An explicitly requested base (`--base main`, `HEAD~3`, a tag, …) is always honored as-is — never override it, even if its file list happens to be empty.

### 2. Tell the user to review — with a **clickable** link

The first stdout line is `READY <url>` — the canonical, always-reachable URL (loopback locally; the Tailscale IP on a remote box). Zero or more `ALT <url>` lines may follow with friendlier equivalents, notably the MagicDNS hostname.

Present the URL as a **Markdown link**, never bare text. The user is frequently on the mobile Claude app, where a bare URL can't be tapped and copy-pasting is painful — a `[url](url)` link is one tap. Tell them the flow up front: their comments stream to you **automatically** as they go (no hand-off button); **⏸ Pause** the agent to batch up work, **▶ Resume** to release it; **End session** when done:

> I've opened a review session — tap to open: **[http://100.x.y.z:PORT](http://100.x.y.z:PORT)**
> (hostname: [http://host.tailnet.ts.net:PORT](http://host.tailnet.ts.net:PORT))
> Just leave comments as you go — I'll pick them up automatically and address them. Hit **⏸ Pause agent** if you want to batch up a few first, and **End session** when you're done.

When an `ALT` MagicDNS hostname is present, make **that** the headline link (stable and readable); otherwise use the `READY` URL. Always wrap as `[url](url)` — including any `ALT` you also surface. Comments auto-save on every add/edit/delete — the user doesn't need to "save" before handing off.

### 3. Consume the stream — drain to the LATEST snapshot, exit ONLY on `session_end`

The reviewer's comments/suggestions now **stream to you continuously** as they
work — there is no explicit "Hand off" button. The server emits a fresh event
whenever the queue changes (debounced), each appended to
`<REPO>/.prereview/events.jsonl` (one JSON object per line; also printed to
stdout). `<REPO>` is the directory from the printed `REPO` line.

**Every `snapshot`/`handoff` event is a FULL snapshot of the still-actionable
set — a superset, not a delta.** So when several have queued up while you were
working, only the **latest** matters. Don't process them one at a time: **drain
to EOF and act on the last snapshot**, advancing your cursor past all of them.

```bash
# n = next line to read (1-based); start at 1.
total=$(wc -l < <REPO>/.prereview/events.jsonl)
if [ "$n" -le "$total" ]; then
  # Events are waiting: read lines n..EOF at once, keep the LAST snapshot, and
  # jump the cursor past every line consumed.
  batch=$(tail -n +"$n" <REPO>/.prereview/events.jsonl)
  n=$((total + 1))
else
  # Nothing waiting → block (no busy-poll) until the next event lands.
  batch=$(tail -n +"$n" -f <REPO>/.prereview/events.jsonl | head -n 1)
  n=$((n + 1))
fi
```

From `batch`: if any line is `{"event":"session_end"}`, **stop** (see below).
Otherwise take the **last** `snapshot`/`handoff` line and act on its `comments`
as one whole (§4) — ignore the earlier ones, they're subsumed. Run the blocking
read with a **long Bash timeout** (e.g. 600s).

**Always re-arm.** The blocking read is live only *while running*, and between
rounds nothing is listening — but because `tail -n +"$n"` starts *at* line `n`,
anything that landed while you were away comes back instantly on the next read.
After every exit (idle timeout, or having done other work), re-run the drain for
the current `n`. **The loop's only exit is `session_end`** — never stop after a
snapshot; the review is continuous.

**Frozen-view contract (important):** you apply fixes to files on disk as you go,
but the reviewer's diff **does not auto-refresh** — it stays frozen until they
click Refresh. So the same comment can reappear in later snapshots with
**re-anchored** line numbers (the server re-anchors every snapshot against live
disk). Trust the latest snapshot's numbers and **dedupe by `id`**; don't be
thrown that a comment you already handled is still present (it leaves the set
only when the reviewer resolves it, or its anchor is edited away → dropped).

- `{"event":"ready",...}` — session is live; tell the user to review.
- `{"event":"handoff","seq":N,"comments":[…],"suggestions":[…]}` — a snapshot
  (the wire keeps the `handoff` name). `comments` *is* the complete actionable
  set; read it as one whole and drive a single coherent change per §4, never
  row-by-row. **Dedupe by `id`.** `comments` is always present (`[]` when empty).
  `suggestions` carries the reviewer's **decisions on your proposed edits**
  (accept / reject / revise) — a full snapshot too; act on it per
  [Suggested edits](#suggested-edits-prereview-suggest). Right after reading,
  echo your status so the reviewer's UI shows you're working — see
  [Echo your status](#echo-your-status-so-the-reviewer-sees-progress).
- `{"event":"session_end","seq":N}` — stop consuming. The review is over (the
  server also exits right after, so a backgrounded launch's job completes too).

Each comment in a handoff carries `id`, `kind`, `file`, `from_line`, `to_line`,
`side`, `body`, `url`, `area` (nested object or `null`), `created_at`,
`anchor_status`. The snapshot is already filtered to actionable rows (no
`resolved=true`, no `anchor_status=outdated`) and omits the opaque `anchor`
fingerprint. Interpret `kind` exactly as in [Comment kinds](#comment-kinds) below
(`line`/`file`/`area`/`region`).

### 4. Act on the comments

**Read all the open comments in the handoff as one set before you edit anything — never act on them in isolation.** A `handoff` snapshot is the complete set of still-actionable comments, and the user expects them addressed together as a related whole, not as a queue of independent directives. First read the entire set, then look across it for:

- **Themes** — several comments pointing at one underlying concern imply a single consistent fix applied everywhere, not N ad-hoc edits.
- **Relationships & ordering** — a broader/structural comment reframes the local ones under it (a `kind=file` "rename this type" changes how you apply a `kind=line` comment that references it; a `kind=area`/`kind=region` note sets direction for the line comments in that file/page). Settle the structural decisions first, then the local edits.
- **Conflicts** — when two comments pull in opposite directions, reconcile them into one coherent decision and surface the conflict to the user, rather than silently letting the last one win.

Then drive a **single, coherent change** across the affected files: each comment is an *input* to that holistic change, not a standalone instruction. Use `from_line`/`to_line`/`side` only to locate each anchor; the *intent* comes from reading the `body` in the context of the full open-comment set. See [Comment kinds](#comment-kinds), [Resolved comments](#resolved-comments), and [Re-anchoring](#re-anchoring) for how to interpret each row.

When you've processed the batch, mark yourself done (`prereview_status done`, below), report back, and go read the next event (§3). The session ends — and the backgrounded server exits — when the user clicks **End session**; you don't need to kill it yourself.

### Echo your status (so the reviewer sees progress)

While you work a handoff, write a tiny status file that the running review UI
watches and shows live in the browser — **across every open tab** — so the user
knows you picked up their comments and when you're done (instead of staring at a
silent page). This is a one-way *echo*, completely separate from the §3 event
read; skipping it never breaks the review, but always do it — it's what makes the
handoff feel responsive.

Write `<REPO>/.prereview/llm-status.json` (same `<REPO>` dir as `events.jsonl`)
with a `state` of `working` (you're applying a batch) or `done` (batch finished).
Write it **atomically** (temp file + `mv`) so the server never reads a half-written
file:

```bash
# usage: prereview_status <working|done> [short message]
prereview_status() {
  local dir="<REPO>/.prereview"
  printf '{"state":"%s","message":"%s","updated_at":"%s"}\n' \
    "$1" "${2:-}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    > "$dir/.llm-status.tmp" && mv "$dir/.llm-status.tmp" "$dir/llm-status.json"
}
```

- **On each `handoff`** (right after you read it in §3, before you start editing):
  `prereview_status working "Applying your review"`. Keep the message short and
  plain (no quotes/newlines — it's echoed verbatim into JSON). **Do not put a
  comment count in it** — the user can hand off again while you work (queuing more
  comments), so any number goes stale; write a brief description of what you're
  doing (e.g. `"Refactoring auth.go"`) or omit the message entirely.
- **When the batch is done** (after §4, before you read the next event):
  `prereview_status done`. On the next handoff, set `working` again.

The file resets on each fresh launch, so a new session starts with no stale
status. You don't write anything on `session_end` — the server is shutting down.

### Mark each comment you addressed (so the reviewer sees a "worked on" badge)

Separately from the whole-batch `prereview_status` echo above, tell prereview
which **specific** comments you handled, so the review UI badges each of them
**worked on**. As you apply each comment's change (or once, listing the batch's
ids at the end), run:

```bash
# usage: prereview processed --out <REPO> <comment-id> [<comment-id>...]
prereview processed --out "<REPO>" 01J... 01K...
```

`<REPO>` is the directory prereview printed at launch (the same one the
`prereview_status` helper writes into); the `<comment-id>` values are the `id`
column/field of the comments you addressed. This appends to
`<REPO>/.prereview/processed.jsonl` (append-only — never hand-edit it or
`comments.csv`), and the badge appears live across every open tab. It's a
one-way signal that you acted on the comment: the human still **resolves**
comments themselves, so keep acting only on unresolved rows.

## Suggested edits (`prereview suggest`)

Comments flow **user → you**. Suggestions flow the other way: **you propose an
edit, the user accepts / rejects / asks you to revise it.** Reach for this when the
user asks you to *suggest edits* rather than just review — e.g. "review the doc to
remove ambiguity and **suggest edits in prereview**", "propose tighter wording",
"suggest fixes for these functions". Each suggestion renders as an inline
suggestion box (a before→after mini-diff, visually distinct from a comment) that
the user acts on, and their decision comes back to you on the next Hand off.

### Submitting suggestions (you → user)

Run `prereview suggest` with a JSON payload — a single object, a JSON array, or
newline-delimited objects — on stdin or via `--file`. It appends to
`<REPO>/.prereview/suggestions.jsonl`; the running review UI picks them up **live**
(no restart), so the boxes appear in the user's browser as you write them.

```bash
prereview suggest --out "<REPO>" <<'JSON'
[
  {"id":"s1","file":"docs/readme.md","from_line":12,"to_line":12,
   "original":"The API might returns an error.",
   "proposed":"The API may return an error.",
   "note":"subject–verb agreement"}
]
JSON
```

Fields per suggestion:

- `id` — **stable, your choice.** Re-submitting the same `id` *revises* that
  suggestion (last write wins); a new `id` adds one. Use a fresh `id` per distinct
  edit. (Omit it and prereview generates one, but then you can't revise it — so
  always set your own.)
- `file` — repo-relative path (same `file` value as the comments use).
- `from_line` / `to_line` — 1-based line range on the **new** side the edit
  replaces. `to_line` defaults to `from_line`.
- `original` — the exact current text at those lines. This is the anchor: prereview
  re-locates it if the file changed, and marks the suggestion `outdated` if it's
  gone — so paste it verbatim.
- `proposed` — the replacement text (multi-line is fine). For a pure insertion,
  leave `original` empty.
- `note` — optional one-line rationale, shown on the box.

`<REPO>` is the directory prereview printed at launch (same as `prereview_status` /
`prereview processed`). Never hand-edit `suggestions.jsonl` — always append via the
subcommand.

### Applying the reviewer's decisions (user → you)

On each Hand off, the event's `suggestions[]` is a **full snapshot of every decided
suggestion** (dedupe by `id`, exactly like comments). Each entry:

```
{"id","file","from_line","to_line","side","verdict","note","original","proposed","anchor_status"}
```

Act by `verdict`:

- **`accept`** — apply the edit: replace `original` with `proposed` at
  `file`:`from_line`–`to_line`. You own the file write (prereview never edits the
  user's files). Once applied, the suggestion re-anchors as `outdated` and **drops
  off future handoffs** automatically — so you won't re-apply it.
- **`reject`** — the user declined it. **Drop it** — do not apply, and do not
  re-submit the same edit. (It keeps reappearing in the snapshot; dedupe by `id`
  and skip it, like a comment you've already handled.)
- **`revise`** — the user wants a different take; `note` says how. Rework the edit
  and **re-submit via `prereview suggest` with the SAME `id`** and new `proposed`
  text. That revision resets the decision (the box goes back to undecided) and
  replaces the old snapshot entry, so the user reviews your new proposal.

Every emitted entry has `anchor_status` **`ok`** or **`moved`** — both carry
**trustworthy** `from_line`/`to_line` (a `moved` suggestion's lines were already
re-anchored to follow the content after the file changed, so apply at the numbers
given). A suggestion whose target text vanished (`outdated`) is **never** emitted —
so once you apply an `accept`, its `original` is gone from the file and it simply
stops appearing on later handoffs (that's the "drops off future handoffs" above);
you never receive an outdated decision to second-guess.

After processing, echo `prereview_status done` and go read the next event (§3), the
same loop as comments.

## Comment data reference

These rules apply to both the streaming snapshot and the [fallback](#fallback-one-shot-handoff---skill-without---stream) CSV — a streaming `comments[]` entry mirrors the CSV columns (minus the opaque `anchor` and `resolved`, which the snapshot has already filtered on).

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
  `anchor_status` are all zero/empty; the `area` field holds a
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
applies). **Skip resolved rows** — treat them as historical context, not
actionable directives. Only act on rows with `resolved=false`. (The
streaming snapshot has already dropped them for you.)

### Re-anchoring

prereview captures a content fingerprint per comment and re-locates it
if the doc changes (including edits *you* make before the user hands
off — relocation also runs on hand-off, so the data you receive is
already re-anchored):

- `ok` / empty — line numbers are trustworthy.
- `moved` — the doc changed; prereview already auto-corrected
  `from_line`/`to_line` to follow the content. Still trustworthy.
- `outdated` — the anchored content changed or vanished and prereview
  could **not** confidently re-place it; `from_line`/`to_line` are
  stale. **Skip these like `resolved=true`** — don't act on the line
  numbers (use `body` as context only). (The streaming snapshot has
  already dropped them for you.)

### CSV columns

The CSV at `.prereview/comments.csv` is the on-disk source of truth. You only
parse it directly in the [fallback](#fallback-one-shot-handoff---skill-without---stream)
flow; streaming hands you JSON. **Columns** (load-bearing — order is the contract):

```
id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url,from_col,to_col,hidden
```

- `id`: opaque per-comment identifier
- `from_line` / `to_line`: 1-indexed; equal for single-line comments. **`0` when `kind=file`, `kind=area`, or `kind=region`** (no line anchor).
- `side`: `new` | `old` (which side of the diff the line is on). Empty when `kind=file`, `kind=area`, or `kind=region`.
- `body`: RFC-4180 quoted; preserves newlines
- `created_at`: RFC-3339 UTC
- `resolved`: `true` | `false` — see "Resolved comments" above
- `anchor`: internal JSON fingerprint — **do not parse or act on it**. Empty when `kind=file`, `kind=area`, or `kind=region`.
- `anchor_status`: `ok` | `moved` | `outdated` | *(empty)* — see "Re-anchoring" above. Always empty for `kind=file` / `kind=area` / `kind=region` (nothing to drift).
- `kind`: `line` | `text` | `file` | `area` | `region` | *(empty)* — see "Comment kinds" above. Empty means `line` for pre-migration rows. `text` = a character range within a line (`from_col`/`to_col`).
- `area`: JSON blob `{"x":0.1,"y":0.2,"w":0.3,"h":0.15}` (0..1 fractions) when `kind=area` (of the image) or `kind=region` (of the live page's document); empty otherwise.
- `url`: the proxied page (app-relative, e.g. `/pricing`) when `kind=region` (`--external` live-site review); empty for every file-based kind.
- `from_col` / `to_col`: 0-based rune offsets delimiting a `kind=text` character range; `0` for every other kind.
- `hidden`: `true` | `false` — a reviewer-only view flag; **ignore it** (never affects whether a row is actionable).
- Older CSVs may have fewer trailing columns (7–15); index by position and default missing ones to empty.

Multi-line bodies use standard CSV quoting; use `encoding/csv` or any RFC-4180-compliant parser.

## Fallback: one-shot handoff (`--skill` without `--stream`)

For a **single, non-iterative pass** (or older tooling), launch with `--skill`
instead of `--stream`. The UI shows one **"Hand off → Claude"** button; clicking
it writes `.prereview/DONE` containing the path to the CSV, and the server keeps
running. There's no event stream and no End-session button — you poll once, read
the CSV, and act. Prefer streaming; reach for this only when a single round is
genuinely all you need.

```bash
# Launch (everything in §1 — base, non-git, restart-fresh — applies; swap --stream for --skill):
prereview --skill "$(pwd)" &

# Poll for the DONE marker, then read the CSV:
while [ ! -f <REPO>/.prereview/DONE ]; do sleep 1; done
cat "$(cat <REPO>/.prereview/DONE)"
```

Parse the CSV per [CSV columns](#csv-columns), filter to actionable rows
yourself (skip `resolved=true` and `anchor_status=outdated` — both stay in the
CSV as historical record and may inform the read as context), then **act on them
holistically exactly as in §4** — read the whole open set first, then drive one
coherent change. When done, clean up (Hand off does *not* stop the server here):

```bash
kill %1                       # stop the background prereview server
rm <REPO>/.prereview/DONE     # so the next session starts fresh
```

## Modes

- **Stream mode** (`--stream`, implies `--skill`) — **the default for skill use**: two buttons — "Hand off →" (emits a `handoff` JSON event each click; session stays open) and "End session" (emits `session_end` and shuts the server down). Consume the JSON event stream continuously until `session_end` — see §3.
- **Skill mode** (`--skill` without `--stream`) — the one-shot [fallback](#fallback-one-shot-handoff---skill-without---stream): top-bar button is "Hand off → Claude". Clicking writes `.prereview/DONE` and keeps the server running so the user can keep editing.
- **Standalone** (neither flag): top-bar button is "Quit", which gracefully shuts the server down. No handoff. Comments are still auto-saved to `.prereview/comments.csv` — the user can read them manually or invoke the skill later to process them.

## Notes

- The CSV at `.prereview/comments.csv` is the source of truth and is rewritten atomically on every change. Reading it at any time is safe.
- Untracked files appear in the file list as added (`[A]`). Commenting on them works the same as on tracked files.
- Non-git targets (single file or non-git directory) show every file as added (`[A]`) — there is no diff and no base. `--base` is ignored and the base picker is hidden; everything else (comments, CSV, re-anchoring, hand-off) works identically.
- Binding is automatic: `127.0.0.1` on a local machine; on a remote (SSH) box with no explicit `--host`, this host's **Tailscale IP**, so a phone on the tailnet can reach it without exposing the source diff on the public internet. **Do not pass `--host 0.0.0.0` as a "make it reachable" shortcut** — that binds every interface, including any public IP. If a remote box has no tailnet, prereview stays on `127.0.0.1` and prints a warning telling you to pass an explicit `--host` (e.g. a private LAN IP).
