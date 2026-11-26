# prereview reference

Companion to [SKILL.md](./SKILL.md). SKILL.md tells the agent **what to do**;
this file is the lookup for **what every value means**.

## Contents

- [CLI flags](#cli-flags)
- [Agent subcommands](#agent-subcommands)
- [stdout protocol](#stdout-protocol)
- [Exit codes](#exit-codes)
- [Filesystem layout](#filesystem-layout)
- [Agent mode](#agent-mode) — the event stream (`ready` / `snapshot` / `files_changed` / `end`)
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
| `--external` | — | no | Annotate a live local website: reverse-proxies the URL on a second origin and overlays region annotation. With a directory `[path]` the session reviews it too, one queue for both; without one it's live-only, requires `--out` and ignores `--base`. See [External mode](#external-mode). |
| `--out` | the review path | with `--external` and no path | Directory to create the store under (its `.prereview/` holds `comments.csv`). Defaults to the review path; required with `--external` and no path. The resolved store is printed as the `STORE` line — that, not this, is the `--out <STORE>` argument the [agent subcommands](#agent-subcommands) take. |
| `--version` | — | no | Print build version and exit. |
| `--update` / `--uninstall` | — | no | Download+install the latest release, or remove the binary. prereview re-syncs the installed skill on every launch (see `skill_updated` in [Agent mode](#agent-mode)). It updates itself on launch only with `PREREVIEW_AUTO_UPDATE=1`; `PREREVIEW_NO_UPDATE=1` or `--no-update` turns that off. |

## Agent subcommands

Besides launching a review, `prereview` has verb subcommands the coding agent uses to
read from and write to a running review's store. Each takes `--out <STORE>` (the
printed `STORE` directory; defaults to the current dir). Run any with `-h` for its own
flags, or `prereview help` for the top-level list.

| Subcommand | Purpose |
|---|---|
| `prereview watch [--since <seq>]` | The **one** queue reader (see [Agent mode](#agent-mode)). Prints every event after `--since`, blocks for the next when caught up, returns a batch so the agent can act, and exits on the `end` event. Each line carries a `seq`; loop by re-running with the highest seq seen. Never `tail`/`head` the log. |
| `prereview comments [--json] [--all]` | List the review's comments from a stable interface (no CSV hand-parsing). Defaults to the actionable set; `--all` includes resolved/outdated/draft. `--json` emits the same shape and order as a `snapshot`. |
| `prereview done [--file <f>\|-] [--all-open] <id>...` | Mark comments, or question sets you asked, **done** (badge them in the UI). Ids come from args, `--file`/stdin (bare ids, a JSON array, or JSONL objects with an `id`), or `--all-open` (the whole actionable set of comments). Each explicit id is **validated** against the comments and question sets — an unknown id fails with a non-zero exit and is not recorded. |
| `prereview status <working\|done> [message]` | Echo the agent's status to the review UI (a live pill across every open tab): `working` while applying a batch, `done` when finished. Writes `llm-status.json` atomically. |
| `prereview suggest [--file <f>]` | Submit proposed edits rendered as inline suggestion boxes (append to `suggestions.jsonl`). See [Suggested edits](#suggested-edits). |
| `prereview import [--file <f>]` | Bring in comments left on a **remote copy** of a reviewed file (Google Docs, Notion, a wiki…), so they're queued like the reviewer's (append to `imports.jsonl`). Each object needs `remote_id`, `source`, `file` and `body`; `quote` is the text the comment is on, and `author`, `url` (http or https), `resolved` and `replies` (each `{remote_id, author, body}`) are optional. **Validated before anything is written**: a missing field, or a file that isn't in the review, fails the command. The server anchors each on its quote (in the source, then in the rendered Markdown), or on the whole file when the quote isn't found once. Importing a comment again adds only its new replies, as `reviewer` entries in its thread with the remote author as `name`, and resolves it once `resolved` is true; one resolved on its first import is skipped. See SKILL.md → *Comments from a remote copy*. |
| `prereview ask [--file <f>\|-] [--check]` | Ask the reviewer to **choose from options** before you build (append to `questions.jsonl`): `{"id"?, "for"?, "file", "kind"?, "from_line", "to_line"?, "side"?, "steps": [{"id"?, "header"?, "prompt", "options": [{"label", "detail"?}], "recommended"?, "multi"?}]}`. One to four `steps` (questions), each with two to four options; `recommended` is a 0-based option index; `multi` lets the reviewer pick any number. `kind` is `line` (default), `text` or `file`. `for` puts the set in a thread, as your reply. It takes a comment's or suggestion's id, or a quiz question's `<quiz-id>:<question-id>`. The set counts as your reply: `ask` adds an agent entry (`Asked: <first prompt>`) to that thread. A newer set in the same thread replaces an older one the reviewer has not sent, and the older card loses its options. **Validated before anything is written**: a bad count, an empty label, an out-of-range `recommended`, an unknown `for` id, or an id that is already a comment, suggestion or quiz question fails with a non-zero exit. `--check` validates and asks nothing. Re-asking an open set's `id` revises it. The reviewer's answers arrive in a snapshot's `questions` (see [Agent mode](#agent-mode)); `done <id>` closes the set. See SKILL.md → *Ask with options*. |
| `prereview questions [--json] [--all]` | List the question sets you asked, for an agent that polls instead of using `watch`. Defaults to the sets to act on, as a snapshot's `questions` carries them (without `anchor_status`, which only the running review computes); `--all` lists every set, unanswered ones with empty `answers`, and marks a set a newer one replaced with `replaced: true`. `--json` emits the same shape as the snapshot. |
| `prereview quiz [--file <f>\|-]` | Submit a **comprehension quiz** about the change for the reviewer to answer (append to `quiz.jsonl`). A quiz on the whole change leaves the quiz's `file` empty and sets `file` on every question; a quiz with a `file` is about that file. Each question needs >= 2 `options`, a 0-based `answer`, a `why`, and `from_line`/`to_line`/`side`; `probe` is one of change-type / localization / consequence / rationale / decision. **Validated before anything is written** — an out-of-range answer, a missing explanation, or a non-`decision` question without a line anchor fails with a non-zero exit naming the question. The server additionally flags a question whose cited line is not in the diff. |
| `prereview explain [--file <f>\|-]` | Submit a **walkthrough of the whole change**, answering an explain request (append to `explain.jsonl`): `{"id"?, "summary"?, "concerns"?: [{"id", "title", "mechanical"?}], "steps": [{"title", "body"?, "concern"?, "risks"?: [{"kind", "note"?}], "impact"?: [{"file", "from_line", "to_line"?, "contains"?, "note"?}], "anchors": [{"file", "from_line", "to_line"?, "side"?}, {"file", "kind": "file"}]}], "diagrams"?: [{"id"?, "title"?, "mermaid", "anchors"?}]}`. Steps go in reading order; each needs a `title` and at least one anchor. A concern's steps must be next to each other; a risk `kind` is one of `auth`, `concurrency`, `data-migration`, `error-handling`, `security`, `performance`, `public-api`, and a step in a `mechanical` concern can't carry one. The changes only mechanical steps cite fold in the diff. Impact anchors point outside the diff and are checked against the working tree (with `contains`, against the cited text); they and diagram anchors never count as explaining a change. `side` is `new` (default) or `old`; `to_line` defaults to `from_line`. **Validated before anything is written**: a step with no anchors, a path outside the repo, a reversed range or the reserved id `not-explained` fails with a non-zero exit naming the step. The server marks an anchor whose line isn't in the review ⚠, and lists the changed hunks no step cites as a last "Not explained" step. Re-submit an `id` to revise. Checked as `explain.md` against the reviewer's checks (findings on stderr, exit 0). |
| `prereview learn wait [--out <STORE>] [--timeout 9m]` | The background learner's loop step (see SKILL.md → *Start the background learner*): claim the next **learning job** and print `job <id>`, a blank line, then the job. It prints `idle` at the timeout, or `stop` once the review `--out` names has ended. `prereview learn finish <id>` closes the job. |
| `prereview learn propose --job <id> --text "…" --evidence <store>:<id> [--evidence …] [--scope "…"] [--glob <p>]… [--replaces <id>] [--produces instruction,checker,judge] [--checker <rule.yml\|->] [--judge <rubric.md\|->] [--shortcut-label "…" --shortcut-body "…" [--shortcut-scope line\|file]]` | Propose one **learning** drawn from the reviewer's reviews, answering a learning job. It appends to `~/.config/prereview/learnings.jsonl`. The learning is *accepted* when the job was queued with auto-accept on, otherwise *proposed* for the reviewer to accept in Library → Learnings. Without `--job` it is always proposed. Every `--evidence` must resolve to a reviewer signal in one of the job's stores, or nothing is written: a comment `id`, the reviewer's reply as `<target_id>@<at>`, or a decided suggestion's `id`. Request comments, the agent's replies, undecided suggestions and quiz questions are never evidence. `--checker`/`--judge` attach a drafted rule YAML / judge rubric, validated first (a checker can't be `extends: command`). Accepted, it lands in the reviewer's checks folder as `learned-<id>`. With no `files:` it's scoped to the lesson's `--glob`, or to `reply.md`, `suggestion.md`, `quiz.md` or `explain.md` on a lesson about the agent's own text. `prereview learn list [--status s] [--json]` prints the library. |
| `prereview judge --bench <id> --run <run> --case <case> --judge <id> --content-hash <h> --rubric-hash <h> --verdict pass\|fail --critique "…" [--model <id>]` | Record one judge's verdict on one **benchmark case**, answering a benchmark request (`"request":"benchmark"`) (append to the benchmark's `runs.jsonl` in the library). Copy each command from the request; the hashes must match the run's pins. Pass `--model`: the run pins the judging model. |
| `prereview judge --file <f> --judge <id> --content-hash <h> --rubric-hash <h> --verdict pass\|fail --critique "…" [--quote "…"] [--model <id>]` | Record one judge's **verdict** on one file, answering a "Run judges" request (append to `judgments.jsonl`). One call per judge; **copy the command from the request** — it carries the file's and the rubric's hashes, recorded with the verdict so the review shows it as out of date once either changes. Validated first: the judge must exist and apply to the file, the path must be review-relative, the verdict is `pass` or `fail`, and a critique is required. |
| `prereview reply [--answered] <id> (--body "…"\|--file <f>\|-)` | Post a thread reply on a comment, suggestion, question set **or quiz question** (the latter addressed as `<quizID>:<questionID>`), so the reviewer sees what you did (and can reply back to steer). Validated against the review's comments, suggestions, question sets and quizzes. `--answered` also settles a **comment** with no edit (it reads "answered" instead of "done"); it fails on a suggestion or quiz id. `--check` checks the reply and posts nothing. A posted reply, like `suggest`, `quiz` and `explain`, is checked against the reviewer's checks (as `reply.md`, `suggestion.md`, `quiz.md`, `explain.md`; a suggestion's proposed text under its own file), with findings on stderr and exit 0. See [Threads](#threads). |
| `prereview applied <suggestion-id>...` | Ack that you applied an **accepted** suggestion's edit to the file, so the UI marks it "applied" and drops it from the snapshot. Ids come from a snapshot's `suggestions[]` (`verdict=accept`); validated against suggestions. Idempotent. |
| `prereview reverted <suggestion-id>...` | Ack that you **reverted** an applied suggestion — restored its `original` text after the reviewer asked to undo the accept (delivered as `verdict=revert`). Nets the suggestion back out of "applied" → the card returns to undecided. Validated against suggestions. Idempotent. |

## stdout protocol

On startup, prereview prints these lines to stdout, in order:

```
READY http://<host>:<port>/?token=<token>
ALT   http://<host>:<port>/?token=<token>     (zero or more; only when on a tailnet)
PROXY http://<host>:<port>     (external mode only; the proxy origin the UI frames)
REPO  <absolute review-root directory>
STORE <absolute store directory>
```

`READY` is the **first line** and carries the canonical, always-reachable URL
(loopback locally; the Tailscale IP on a remote box). Its `token` is made fresh at
each launch, and a page opened without it answers 403, so pass the URL on whole. It is the only line the skill and
the e2e harness machine-parse — match the literal prefix `READY ` and take the rest.
Zero or more `ALT` lines follow with additional reachable forms (chiefly the MagicDNS
hostname); they are purely additive and parsers may ignore them. In external mode one
extra `PROXY <url>` line follows the `ALT` lines (before `REPO`) — additive too;
`READY` remains the canonical UI url.

The last two lines carry the two paths, and they are **not** the same thing:

- `REPO` is the directory the reviewed files are under — equal to the path argument
  for a git repo or non-git directory, and the file's **parent** directory for a
  single-file review. Resolve a comment's `file` against it, not against the raw path
  argument.
- `STORE` is where this review's annotations and event log live, and the value every
  subcommand takes as `--out`. It is `<REPO>/.prereview` for a repo or directory
  review and a per-target subdirectory of it for a single-file one (#199) — so pass
  the printed line through rather than constructing it.

All other output is slog-formatted and goes to stderr — including the "remote box, no
tailnet" fallback warning.

In **agent mode** (`--agent`), after the preamble prereview emits one JSON object per
line (JSONL) to stdout — a `ready` event, a `snapshot` per queue mutation, then a
single `end` — and mirrors the same lines to `<STORE>/events.jsonl`. The
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

In agent mode, clicking **End session** or **Approve and end** emits the `end`
event (with `"approved": true` for the second) and shuts the server down. A
backgrounded launch's job then exits on its own; the agent does not need to kill it.

## Filesystem layout

Everything prereview writes lives under `<STORE>/`, the directory from the stdout
`STORE` line:

```
<STORE>/
├── comments.csv                ← source of truth, rewritten atomically on every change
├── events.jsonl                ← agent mode only (--agent); append-only JSON event log, reset each launch
├── processed.jsonl             ← INBOUND: per-comment "done" markers (`prereview done`); append-only
├── answered.jsonl              ← INBOUND: comments settled by a reply with no edit (`prereview reply --answered`); append-only
├── llm-status.json             ← INBOUND: agent-status echo, watched by the server; reset each launch
├── agent-cursor.json           ← INBOUND: written by `watch` / `comments --json` — who is reading the queue; reset each launch
├── suggestions.jsonl           ← INBOUND: the agent's proposed edits (`prereview suggest`); append-only, durable
├── imports.jsonl               ← INBOUND: remote comments queued by `prereview import`; append-only, durable
├── imports-ledger.jsonl        ← what the server imported (remote id → comment id, source); server-owned, durable
├── suggestion-decisions.jsonl  ← reviewer's accept/reject verdicts; server-owned, rewritten atomically, durable
├── quiz.jsonl                  ← INBOUND: comprehension quizzes (`prereview quiz`); append-only, durable
├── explain.jsonl               ← INBOUND: walkthroughs of the change (`prereview explain`); append-only, durable
├── first-seen.json             ← the diff fingerprint each walkthrough was first seen against; server-owned, durable
├── judgments.jsonl             ← INBOUND: judge verdicts (`prereview judge`); append-only, latest per file+judge
├── judge-labels.jsonl          ← the reviewer's Agree/Disagree on verdicts (server-owned)
├── viewed.json                 ← files the reviewer marked viewed, with each one's content hash; server-owned, durable
├── last-look.json              ← what each file held when the reviewer last had it open; server-owned, durable
├── quiz-answers.jsonl          ← reviewer's quiz answers; server-owned, rewritten atomically, durable
├── questions.jsonl             ← INBOUND: question sets you asked (`prereview ask`); append-only, durable
└── question-answers.jsonl      ← reviewer's answers to them; server-owned, rewritten atomically, durable
```

The INBOUND files are written by the **agent** and read by the server (the reverse of
`comments.csv`). `suggestion-decisions.jsonl` is the reviewer's reply — server-owned —
and its verdicts ship back to the agent in each `snapshot`'s `suggestions` array (see
[Agent mode](#agent-mode)). The agent writes `llm-status.json` via `prereview status <working|done> [message]`
(atomically) — the file is `{"state":"working"|"done","message":"…","updated_at":"<RFC3339>"}`;
the server polls it (~0.75s) and pushes the status to every open browser tab. Missing/blank `state` is idle. `agent-cursor.json`
is written for you by `prereview watch` (a heartbeat every ~3s while it blocks, then the
delivered `seq` when it returns) and by `prereview comments --json` (a one-shot read) —
`{"mode":"watch"|"poll","state":"waiting"|"delivered","delivered_seq":N,…}`. The Queue
derives from it whether an agent is connected, has picked the queue up, or has gone quiet
with work outstanding; never write it by hand. `events.jsonl`, `llm-status.json`, `agent-cursor.json`, and
`processed.jsonl` reset on each fresh launch; `suggestions.jsonl` /
`suggestion-decisions.jsonl` are durable across launches.

For a git repo or non-git directory the store is `<path argument>/.prereview/`. A
single-file review gets its **own** store, one per reviewed file, under the parent
directory's `.prereview/files/`. Reviews of two files in one directory are independent
sessions that can run at the same time, and neither can see the other's queue (#199).
The store is created eagerly on startup. Add `.prereview/` to the repo's `.gitignore` —
one entry still covers everything.

A single-file review is also independent of a **directory** review of the same
directory: they are two stores, so comments left in one do not appear in the other.

Upgrading from a prereview that predates #199: single-file reviews used to share the
parent directory's `comments.csv`. The first launch on the new layout **copies** the
reviewed file's rows — with their done markers and thread replies — into that file's
own store; the old shared file is left untouched.

## Agent mode

`--agent` streams the review queue for a coding-agent consumer. prereview emits a JSON
event log — to stdout (live) and
`<STORE>/events.jsonl` (durable replay) — that the agent reads continuously
with **`prereview watch --since <seq>`** (the one reader; do not hand-roll `tail`/`head`,
which drops events whenever the tail isn't running). Consume it until the `end` event.

Four event types:

- **`ready`** (seq 0) — emitted once, after the stdout preamble. Fields:
  `event, seq, ts, repo, csv`, plus optional `paused` and `skill_updated`.
  - `skill_updated:true` means this launch **refreshed the installed Claude Code skill**
    to match the (possibly upgraded) binary — so the agent's loaded skill is now
    stale. The agent must re-read the skill from its install path
    (`~/.claude/skills/prereview/SKILL.md`) before continuing, and tell the user to
    reload the skill in their agent. (A matching stderr note prints too.)
  - `paused:true` means the reviewer started with the queue paused.
- **`snapshot`** — emitted (debounced) per queue mutation. A **FULL snapshot of the
  still-actionable queue** (a superset, not a delta): act only on the **latest** and
  **dedupe by `id`**. Fields: `event, seq, ts, comments[], suggestions[]`. `comments`
  and `suggestions` are always present (`[]` when empty). The snapshot is pre-filtered
  to what needs you: unresolved, non-outdated, non-draft comments. It **also** carries
  any comment the reviewer just replied on, whatever its state (resolved, outdated, or
  already done; see [Threads](#threads)). An item you
  replied on last drops out until the reviewer speaks again. While the reviewer has the
  queue **paused** (batching), no snapshot is emitted — mutations still persist, but
  `watch` blocks — and **resume** emits ONE coalesced snapshot of everything queued.
  `questions` (omitted when empty) holds the question sets you asked (`prereview ask`)
  that need you. That is each set the reviewer sent and you haven't marked `done`, and
  each set whose thread ends with the reviewer. Each is `{id, for?, file, from_line,
  to_line, side, resolved?, answers: [{step, prompt, picked?: [option labels], other?:
  "typed answer"}], thread?}`. `resolved: true` means the reviewer closed the set:
  build nothing new for it, and `done` it. `answers` is `[]` when
  the reviewer replied without sending. Done marks reset on a fresh launch, so a set you
  closed comes back once after a relaunch, like a done comment.
  `comments[]` is in **queue order, oldest first** (an edited comment keeps its place):
  to tell which of two comments is later, use array position, not the
  second-resolution `created_at`, which ties.
  `write_with` (omitted when empty) maps each kind of text you write into the review,
  `reply`, `suggestion`, `quiz` and `explain`, to the generated skills to load before
  writing it: `learned-writing`, then the kind's own skill (`learned-agent-replies`,
  `learned-suggestions`, `learned-quiz-questions`, `learned-walkthroughs`) once a
  lesson exists for it. It's
  read fresh for every snapshot, so a skill the background learner adds mid-review
  shows up in the next one. Live-site reviews (`--external`) carry it too.
- **`files_changed`** — reviewed files changed on disk under you: the reviewer (or
  another tool) edited them, or the reviewer **restored a version**. One event per
  settled run of edits, not per write. Fields: `event, seq, ts, files[], version,
  cause`, plus `agent_watching` when true. `files` are the paths whose content moved
  (including a file that dropped out of the review). `version` is the new version's
  seq, absent when no version was recorded. `cause` is `edit` or `rollback`. **Re-read every listed file before your next edit
  to it** — line numbers and any copy you loaded are stale. `agent_watching:true`
  means your `watch` was blocked for every edit in the run, so none of them were
  yours. Without it the run may include your own edits: if the listed files are
  only ones you just changed yourself, carry on. Not queue work: it carries no
  comments and arrives even while the queue is paused. A run you close with
  `status done` is treated as your own batch and sends nothing.
- **`end`** — emitted once on **End session** or **Approve and end**. The **only**
  terminator: stop consuming. The server shuts down right after (so a backgrounded
  launch's job completes too). **`approved: true`** means the reviewer chose Approve
  and end: they approved the plan or change. Without it, ask them (SKILL.md, agent
  loop step 6).

Every event has `event`, a monotonic `seq`, and an RFC-3339 `ts`. Each comment in a
`snapshot` mirrors the CSV columns **minus** the opaque `anchor` fingerprint and
`resolved`, with `area` as a nested object (or `null`) — no nested JSON-in-a-string.
Each comment carries: `id`, `kind`, `file`, `from_line`, `to_line`, `from_col`,
`to_col`, `side`, `body`, `url`, `area`, `created_at`, `anchor_status`, `text` (the
exact selected substring, for `kind=text`), `thread` (the conversation, present
only when non-empty — see [Threads](#threads)), and `request` (present only on a row
prereview queued as a request to you — see the `request` column below). A comment
imported from a remote copy also carries `source`: `{name, url, author, quote}`, where
`quote` is the text it was on there. See [Comment data](#comment-data) for how to
interpret them.

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
{"event":"files_changed","seq":2,"ts":"…","files":["docs/readme.md"],"version":3,"cause":"edit","agent_watching":true}
{"event":"end","seq":3,"ts":"…","approved":true}
```

`events.jsonl` is append-only and reset on each fresh launch. The CSV stays the
authoritative store; the stream is a convenience layer over it.

## Threads

A **thread** is the two-way conversation on a comment or suggestion (#149). It rides on
each snapshot item as a `thread` array (present only when non-empty), oldest first; each
entry is `{ "author": "agent" | "reviewer", "body": "…", "at": "<RFC3339>" }`.

- **You → reviewer:** `prereview reply --out "<STORE>" <id> --body "…"` appends an
  `agent` entry the reviewer sees under the card. Post one after addressing a comment to
  say what you changed (alongside the `done` mark). Validated against comments +
  suggestions, like `done`. If you changed **nothing** (an answer, or a question back),
  use `reply --answered` instead of `done`: the comment reads **answered** until the
  reviewer resolves it or replies to push back.
- **Reviewer → you:** the reviewer replies under the card; that `reviewer` entry re-arms
  the snapshot. When an item's **last thread entry is `reviewer`**, they're steering you
  — respond to it, then `reply`.
- **Unread model (why items appear/disappear).** The snapshot carries an item only when
  it is **fresh** (no thread yet, unresolved) or its **last entry is the reviewer's**.
  Reply, and it drops out until the reviewer speaks again — you never re-answer yourself.
  A **resolved, outdated, or already-done** comment reappears iff the reviewer replied on
  it (reopening the conversation). The reply overrides the suppression but never changes
  `resolved`/`outdated`/`done` on its own. Marking `done` again won't settle it: only
  your `reply` drops it back out.

Suggestion threads work the same way — a `thread` on a `suggestions[]` entry — with one
exception: a **decided** suggestion stays in `suggestions[]` whatever its thread says, until
it is applied or goes outdated. A decided entry whose thread ends with your reply is one
you already handled.

## Suggested edits

The agent proposes edits with `prereview suggest` (a JSON payload — a single object, a
JSON array, or newline-delimited objects — on stdin or via `--file`). It appends to
`<STORE>/suggestions.jsonl`; the running UI picks them up live (no restart).

**Prompt entry point (#147).** A whole-file chip in the reviewer's file comment box
(#275) sends a prebuilt prompt as an ordinary `kind=file` comment whose body asks for
suggestions. The agent answers it with `prereview suggest` (proposing, not editing the
file), then `done`s the prompt and `reply`s a one-line summary — see the SKILL's
*Suggested edits → Prompts*. Users add their own prompts as `.md` files in
`~/.config/prereview/prompts/` (first `# Heading` = the chip's label, the rest = the
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
- `for` — optional: the id of the comment this edit follows from. Every suggestion
  naming the same comment shows as one group on it, with Accept all / Reject all, so
  use it when one comment's change takes several edits (see SKILL.md → *Changes that
  ripple*). It must name a comment in this review, or `suggest` refuses the payload.

**Proposing alternatives (a group).** To offer the user a *choice* between ways to edit
the same text, submit multiple suggestions with the **same** `file`/`side`/`from_line`/
`original` and different `proposed` (each a distinct `id`). prereview groups them: the
user accepting one **auto-rejects the rest**, so you get exactly one `accept` and the
others as `reject` — no need for them to reject each by hand.

- **Same `original` is what makes a group.** When the alternatives change different
  amounts of text, quote the **union** span as `original` in every one, and have the
  narrower alternative repeat the text it leaves alone in its `proposed`.
- **`to_line` is derived, except for an insertion.** prereview recomputes `to_line` from
  `original`'s line count, so a wrong hint can't split a group. A pure insertion
  (`original: ""`) keeps the `to_line` you send, so insert-alternatives must agree on it.
  `suggest` warns on stderr when one payload has two suggestions starting on the same line
  that won't group.
- When to offer a choice unasked, and how to close it out, is in SKILL.md → *Offer
  alternatives when the choice is the reviewer's*.

**Applying the reviewer's decisions.** Each `snapshot`'s `suggestions[]` is a full
snapshot of every decided suggestion (dedupe by `id`). Each entry:

```
{"id","for","file","from_line","to_line","side","verdict","note","original","proposed","anchor_status","thread"}
```

`for` is present only on a suggestion that follows from a comment.

Act by `verdict`:

- **`accept`** — apply the edit: replace `original` with `proposed` at
  `file`:`from_line`–`to_line`. You own the file write (prereview never edits the user's
  files). Then **`prereview applied "<id>"`** to ack it — that flips the card to "applied"
  and drops it from future snapshots (re-acking is harmless).
- **`reject`** — the user declined it. **Drop it** — do not apply, do not re-submit.
  (It keeps reappearing; dedupe by `id` and skip it, like a handled comment. If its
  `thread` ends with your reply, you already handled it.)
- **`revert`** — the user wants an edit you already applied UNDONE. The file holds your
  applied `proposed`; **restore `original` over it** (the inverse), then
  **`prereview reverted "<id>"`** to ack. That nets it back out of "applied" and returns
  the card to undecided. Idempotent per `id`.

Every emitted entry has `anchor_status` **`ok`** or **`moved`** — both carry
trustworthy `from_line`/`to_line` (a `moved` suggestion was already re-anchored). An
`outdated` suggestion is **never** emitted, so once you apply an `accept` it
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

File: `<STORE>/comments.csv`. RFC-4180 quoted; UTF-8. It's the on-disk source
of truth, but you rarely need to parse it — `prereview comments --json` returns the same
shape as a snapshot.

### Header (load-bearing — column order is the contract)

```
id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url,from_col,to_col,hidden,enqueued,request,check
```

Older CSVs may have 7–18 columns; columns 0–7 are stable, so index by position and
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
| `request` | enum | `judge`, `benchmark`, `quiz`, `explain`, `extraction`, *(empty)* | Set when prereview, not the reviewer, queued the row as a request to you: run a file's or a benchmark's judges, write a quiz, or walk them through the change. The body says exactly what to do. `extraction` (read past reviews) appears only on rows from before learning moved to background jobs (`prereview learn wait`). Empty for every reviewer comment. These are never reviewer signals: don't learn from them. |
| `check` | string | `filler-intensifiers`, *(empty)* | The ID of the check or judge whose finding or critique the reviewer queued as this comment (the body already names it). **Ignore it** — it links the comment to its finding card in the review. Empty for every other comment. |

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
**frozen** (no content re-anchoring, like `area`). Without a path it requires `--out`
(no repo to default the store to) and ignores `--base`. Region comments are informational
feedback about a page, not file-edit directives.

With a directory path as well (`prereview --agent --external <url> <dir>`), one session
reviews both: the snapshot carries `kind=region` comments beside the line and file
comments, from the one `STORE`. A region comment has no `file`; read its `url` and
`area`. A single-file path is refused.

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
- **Files over 1 MB** render with a "file too large to review" placeholder;
  comments are still accepted but the agent saw only the placeholder — be conservative.
