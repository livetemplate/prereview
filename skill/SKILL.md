---
name: prereview
description: Launches an interactive browser review of the working tree (or any file, directory, or live local site) — code diffs, Markdown, HTML, images, by line, text range, block, or region. The reviewer queues comments in the browser; the coding agent launches `prereview --agent`, consumes the queue as a JSON event stream with `prereview watch`, and applies the fixes, marking each addressed comment done with `prereview done`. The agent can also submit suggested edits (`prereview suggest`) that render inline for the reviewer to accept or reject. Use when the user asks to review changes before a commit or push, leave comments on a diff or a doc, or have edits suggested in prereview.
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

Launches a web UI for the user to review the working tree (or any file/directory)
and leave comments — on a diff line or text range, a rendered Markdown/HTML block,
a region of an image, or a box on a live local site (`--external`). The reviewer
builds up a **queue** of comments in the browser; you consume that queue as a JSON
event stream and apply the fixes.

Binding is automatic: `127.0.0.1` on a local machine, and — on a remote (SSH) box —
this host's **Tailscale IP**, so the user can reach it from a phone over the tailnet
without exposing it publicly (`--host` overrides).

## Launch

Run in the background with `--agent` (agent mode: a coding agent drives the review).
The review path is the **positional argument** (defaults to the current directory);
flags must come **before** it.

```bash
cd <repo>
prereview --agent "$(pwd)" &
# stdout:
#   READY http://127.0.0.1:PORT        (canonical URL; Tailscale IP on a remote box)
#   ALT   http://host.tailnet.ts.net:PORT   (0+ friendlier equivalents; only on a tailnet)
#   REPO  /abs/dir/whose/.prereview/holds/everything
#   {"event":"ready","seq":0,...}       (events mirror to stdout too; consume with `prereview watch`)
```

After `READY`, prereview prints `REPO <dir>` — the directory whose `.prereview/`
holds the queue store and event log. **Always operate relative to `REPO`**, not the
raw path argument: they're identical for a git repo, but differ for a single file.

`--base` defaults to `HEAD` (working tree vs last commit); pass `--base main` (before
the path) for branch-vs-base review, `--base HEAD~3` for last-3-commits, etc.

**Reviewing files outside a git repo** (a Claude plan, a loose doc): the path also
accepts a **single file** or a **non-git directory**. prereview reviews it with no
diff — every line is "new" and commentable, the base picker is hidden, `--base` is
ignored:

```bash
prereview --agent ~/.claude/plans/some-plan.md &   # one file  → store lives in its PARENT dir
prereview --agent ~/.claude/plans &                # whole dir, recursively
```

For a single file the `.prereview/` store lives in the file's **parent** directory —
exactly what the printed `REPO` line points at. Re-anchoring works the same.

**Clean working tree → handled for you.** When you did *not* pass an explicit
`--base` and the working tree is clean, prereview reviews the whole tree against the
empty base (every file appears added, any line is commentable) — so just
`prereview --agent "$(pwd)" &`. An explicitly requested base (`--base main`,
`HEAD~3`, a tag, …) is always honored as-is.

**Already running for this repo? Take it over with `--replace`.** prereview refuses
to start a second server for the same repo (duplicate servers fight over the same
store). To relaunch, pass `--replace` to stop the old one and take over:

```bash
prereview --agent --replace "$(pwd)" &
```

Comments auto-save, so nothing is lost when the old server is replaced.

## Tell the user — with a clickable link

The first stdout line is `READY <url>`. Zero or more `ALT <url>` lines may follow
with friendlier equivalents (notably the MagicDNS hostname). Present the URL as a
**Markdown link**, never bare text — the user is often on the mobile Claude app,
where a `[url](url)` link is one tap and a bare URL can't be tapped:

> I've opened a review session — tap to open: **[http://100.x.y.z:PORT](http://100.x.y.z:PORT)**
> (hostname: [http://host.tailnet.ts.net:PORT](http://host.tailnet.ts.net:PORT))
> Just leave comments as you go — I'll pick them up automatically and address them.
> Hit **⏸ Pause** to batch a few first, **▶ Resume** to release them, and
> **End session** when you're done.

When an `ALT` MagicDNS hostname is present, make **that** the headline link
(stable and readable); otherwise use the `READY` URL. Always wrap as `[url](url)`.

## The agent loop

Track this as a checklist; run it until you see the `end` event.

1. **Read the `ready` event first.** If `skill_updated:true`, this launch refreshed
   the installed prereview skill to match a self-updated binary, so **the skill you
   have loaded is now stale**: re-read `~/.claude/skills/prereview/SKILL.md` before
   continuing, and tell the user to reload the prereview skill in their agent. If
   `paused:true`, the reviewer started with the queue paused (batching).
2. **Consume the queue** with the one reader — never hand-roll `tail`/`head`:

   ```bash
   # n = the highest seq you've seen; start at -1 (from the beginning).
   prereview watch --out "<REPO>" --since "$n"
   ```

   It prints every event after `$n` (one JSON object per line), then — if none are
   waiting — **blocks** for the next, then **returns** a batch so you can act. Run it
   with a **long Bash timeout** (e.g. 600s).
3. **Act on the latest `snapshot`.** Each `snapshot` is a FULL snapshot of the still-
   actionable queue (a superset, not a delta). When a returned batch holds several,
   act only on the **latest**, and **dedupe by `id`**. See *Act on the comments*.
   While the reviewer has the queue **paused** (batching), no snapshot is emitted —
   your `watch` simply blocks — and **resume** delivers ONE coalesced snapshot of
   everything queued; act on it as a single set. (`ready.paused` tells you only
   whether the session *started* paused.)
4. **Advance the cursor.** Set `n` to the highest `seq` in the batch and re-run
   `watch --since "$n"`. Because the log is durable and `--since` resumes from the
   cursor, anything that lands between rounds comes back instantly — no missed events.
5. **Stop only on `end`.** The `end` event is the ONLY terminator; when you read it,
   stop consuming (the server shuts down right after, so a backgrounded launch's job
   completes on its own). Never stop after a `snapshot` — the review is continuous.

Event fields and comment kinds are in
[reference.md → Agent mode](./reference.md#agent-mode). The snapshot is pre-filtered to
what needs you: unresolved, non-outdated, non-draft comments — **plus** any comment the
reviewer just replied on, whatever its state (resolved, outdated, or already done — see
*Threads*). Each item may carry its `thread`.

## Act on the comments

**Read the whole actionable set as one before you edit anything — never act on the
comments in isolation.** A snapshot is the complete set of still-actionable comments,
and the user expects them addressed together as a related whole. Read the entire set,
then look across it for:

- **Themes** — several comments pointing at one concern imply a single consistent fix,
  not N ad-hoc edits.
- **Relationships & ordering** — a broader/structural comment reframes the local ones
  under it (a `kind=file` "rename this type" changes how you apply a `kind=line`
  comment referencing it). Settle structural decisions first, then local edits.
- **Conflicts** — when two comments pull in opposite directions, reconcile them into
  one coherent decision and surface the conflict to the user rather than letting the
  last one win.

Then drive a **single, coherent change** across the affected files: each comment is an
*input* to that holistic change, not a standalone instruction. Use
`from_line`/`to_line`/`side` to locate each anchor; the *intent* comes from the `body`
read in the context of the full set. Comment kinds (`line`/`text`/`file`/`area`/
`region`), resolved handling, and re-anchoring are detailed in
[reference.md → Comment data](./reference.md#comment-data).

## Echo your status (so the reviewer sees progress)

While you work, echo what you're doing so the running UI shows a live status pill
across every open tab — so the user knows you picked up their comments. This is a
one-way *echo*, separate from the `done` marking below; skipping it never breaks the
review, but always do it:

```bash
prereview status --out "<REPO>" working "Applying your review"   # starting a batch
prereview status --out "<REPO>" done "Tightened the intro and removed a duplicate word"  # finished + changelog
```

Keep the working message short and plain. **Do not put a comment count in it** — the
queue can grow while you work, so any number goes stale. The status resets on each
fresh launch; you write nothing on `end` (the server is shutting down).

**The `done` message is the version changelog (#155).** When you finish a batch that
edited files, prereview snapshots a new version — and your `done` message becomes that
version's changelog entry, shown in the file's Versions panel. So make it a short,
plain-language sentence describing *what you changed to the docs* this batch ("Fixed the
subject–verb agreement in the API section"), not what you were busy doing. One line; no
diff or counts (prereview shows the +add/−del itself). Skip the message on a batch that
changed no files.

## Mark each comment you addressed — REQUIRED

Separately from the whole-batch `prereview status` echo above, tell prereview which
**specific** comments you handled, so the UI badges each **done** — this is how the
reviewer sees you acted on their notes (an unmarked comment looks ignored). **After
every edit that addresses a comment, immediately mark that comment's id — before
moving on.** Don't defer it, and don't skip it.

```bash
# mark specific ids (validated against comments.csv — an unknown id fails non-zero):
prereview done --out "<REPO>" <comment-id> [<comment-id>...]

# or read ids from the stable JSON interface and pipe them (no CSV hand-parsing):
prereview comments --out "<REPO>" --json | jq -r '.[].id' | prereview done --out "<REPO>" --file -

# or, once you've addressed the WHOLE current batch, mark all of it at once:
prereview done --out "<REPO>" --all-open
```

List ids any time with `prereview comments --out "<REPO>" --json` (the **same shape as
a snapshot**). `prereview done` validates each id against `comments.csv` and **fails
loudly** (non-zero exit, naming the unknown ids) rather than recording garbage, so a
typo can't corrupt anything. **Prefer per-id marking** (tie each mark to its edit) over
`--all-open`; reach for `--all-open` only when you genuinely handled the entire batch.
Marking is a one-way signal that you acted; the human still **resolves** comments
themselves, so keep acting only on unresolved rows.

## Threads — say what you did, and respond when the reviewer steers

You and the reviewer hold a **thread** on each comment/suggestion. Both directions:

**You → reviewer.** `done` marks a comment handled; **`reply` says what you actually
changed** — a short note the reviewer sees threaded under the comment (or suggestion),
so they aren't left guessing. After addressing a comment, post a one-line reply
alongside the `done` mark:

```bash
prereview reply --out "<REPO>" <comment-or-suggestion-id> --body "Renamed to userToken and updated the 3 call sites."
```

The id is validated against `comments.csv` and suggestions (an unknown id fails
non-zero, like `done`). Keep it to a sentence or two.

**Reviewer → you.** Each comment/suggestion in a snapshot may carry a **`thread`**
array — the conversation so far, oldest first, each entry `{author, body, at}`. When an
item's **last thread entry is the reviewer's**, they've replied to steer you: read the
thread, address their latest point, and `prereview reply` with what you changed.

**You never re-answer yourself.** The snapshot only carries items that need you — a
**fresh** comment, or one the **reviewer replied on last**. Once you reply, the item
drops out until the reviewer speaks again, so a thread whose last entry is *yours* is
one you're already waiting on and you won't see it. (This is also why a **resolved,
outdated, or already-done** comment can reappear: a reviewer reply reopens the
conversation whatever the comment's state — treat that reply as the new instruction.
`done` does not settle it a second time; only your `reply` flips the last entry back to
you and drops it again.)

## Suggested edits (`prereview suggest`)

Comments flow **user → you**. Suggestions flow the other way: **you propose an edit,
the user accepts or rejects it.** Reach for this when the user asks
you to *suggest edits* rather than just review — e.g. "review the doc and suggest
edits", "propose tighter wording". Each suggestion renders as an inline box (a
before→after mini-diff) that the user acts on; their decision comes back in the next
snapshot's `suggestions[]`.

**Prompts (`#147`).** The reviewer can also send a prebuilt prompt from the file header
("Ask for suggestions"). It arrives as a normal `kind=file` comment whose body asks you
to *suggest* edits (e.g. "review this file's grammar … propose each change as a
`prereview suggest` edit — do not modify files directly"). Treat it as a suggest
request: read the file, propose the edits with `prereview suggest` (**do not edit the
file** — the reviewer accepts/rejects each box), then `done` the prompt comment and
`reply` a one-line summary ("proposed N edits below"). This is the one case where a
comment wants **suggestions, not a direct fix** — the body says so; follow it.

## Comprehension quizzes (`prereview quiz`, #191)

The reviewer can also ask for a **comprehension quiz** about a file's diff, via
"Quiz me" in the file header. Like a prompt it arrives as a `kind=file` comment,
and its body spells out the contract — but it wants **neither a fix nor a
suggestion**. Answer with `prereview quiz`, never `prereview suggest`, and do not
edit any files.

```bash
prereview quiz --out "<REPO>" --file quiz.json    # or pipe JSON on stdin
```

Then `done` the request comment and `reply` a one-liner ("5 questions, 2 about
decisions you didn't ask for").

Write 3-5 multiple-choice questions grounded **strictly in that diff**. Each needs
`options` (>= 2), a 0-based `answer`, a `why` explaining it (shown after the
reviewer answers - this is where the teaching happens), and `from_line`/`to_line`/
`side` pointing at the lines it is about. Tag each with a `probe`:

- `change-type` - what kind of change this is
- `localization` - where a behavior lives in the diff
- `consequence` - what breaks if it is wrong or misread
- `rationale` - why it is done this way and not the obvious alternative
- `decision` - **what you decided on your own that the reviewer never asked for**

That last one matters most. The other four test whether they understood what IS in
the diff; `decision` surfaces what they might have OBJECTED to - an unrequested
dependency, a changed default, a widened interface, a skipped edge case. Include at
least one whenever the diff contains such a choice. If you genuinely made none,
write no `decision` question: an honest omission beats a manufactured surprise.

Anchor each question the way you would anchor a comment, with `kind`:
`"line"` (the default) for a line range, or `"file"` for a question about the
change as a whole — including one about something **absent** ("chose not to add a
test for the error path"), which has no lines to point at. `text`, `area` and
`region` are accepted for parity with comments but are not rendered yet.

A `"line"` question MUST carry a real `from_line`: prereview rejects one without,
and flags a question whose cited line is not in the diff, so **do not guess line
numbers** — read the file first. Use `"kind": "file"` rather than a made-up line.

Make every wrong option plausible - something a careful reader might actually
believe. A quiz you can pass without reading the diff is worthless.

**The reviewer can reply to a question.** A quiz is a conversation, not a verdict:
they may say an option is ambiguous, or that a question is simply wrong. Those
replies arrive in the snapshot's `thread` exactly like a reply on a comment, and
you answer with `prereview reply` — addressing the question by the composite id
`"<quizID>:<questionID>"`, since a question id is only unique within its quiz:

```bash
prereview reply --out "<REPO>" "z1:q3" --body "Fair - option 2 was ambiguous. Revised."
```

If the reviewer is right that a question is wrong, **fix it**: re-submit the quiz
with the same `id` (last write wins) with that question corrected, then reply
saying what you changed.

Submit a JSON payload — a single object, a JSON array, or newline-delimited objects —
on stdin or via `--file`. The running UI picks them up live (no restart):

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

Use a **stable `id`** you choose: re-submitting the same `id` *revises* that suggestion
(last write wins); a new `id` adds one. Full field list and the accept/reject
loop are in [reference.md → Suggested edits](./reference.md#suggested-edits).

On each snapshot, `suggestions[]` is a full snapshot of every decided suggestion
(dedupe by `id`). Act by `verdict`:

- **`accept`** — apply the edit (replace `original` with `proposed` at the given
  lines; you own the file write), then **`prereview applied "<id>"`** to ack it. That
  flips the reviewer's card from "accepted" to "applied" and drops it from the snapshot.
  Idempotent — re-acking an id is harmless (two snapshots can carry the same accept
  before your ack lands, so dedupe by `id` like comments and ack once per id).
- **`reject`** — drop it; do not apply or re-submit. Dedupe by `id` and skip it.
- **`revert`** — the reviewer changed their mind about an edit you already applied. The
  file currently holds your applied `proposed` text; **restore the `original` text over
  it** (the inverse of the accept), then **`prereview reverted "<id>"`** to ack. That
  nets the suggestion back out of "applied" and returns the reviewer's card to undecided.
  Idempotent per `id`, same as `applied`.

After processing, run `prereview status --out "<REPO>" done` and go read the next event.

**Never finish while an `accept` is unapplied.** prereview does not write the user's
files — you do. An accepted edit that you don't apply and ack leaves the document
inconsistent with the review, and once you stop watching, nothing else will ever apply
it. Before you end your turn, drain every `accept` in the latest snapshot: apply it, then
`prereview applied "<id>"`. The reviewer's Queue shows an amber "accepted, awaiting apply"
count for exactly this, and warns them on End session — don't be the reason it's non-zero.

## Notes

- Comments auto-save on every add/edit/delete — the user never needs to "save".
- Non-git targets (single file or non-git directory) show every file as added (`[A]`);
  there is no diff and no base. Everything else works identically.
- **Never pass `--host 0.0.0.0`** as a "make it reachable" shortcut — it binds every
  interface, including any public IP. On a remote box with no tailnet, prereview stays
  on `127.0.0.1` and prints a warning telling you to pass an explicit `--host`.
- The store's `comments.csv` is the on-disk source of truth (rewritten atomically);
  you never need to parse it by hand — use `prereview comments --json`. Column details
  are in [reference.md → CSV schema](./reference.md#csv-schema).
