# prereview CLI reference

Companion to the [README](../README.md). The README shows the common
invocations; this is the full reference for every flag, mode, subcommand, and
combination.

```
Usage: prereview [flags] [path]
```

`path` is the review target — a git repo, a non-git directory, or a single
file. It's the trailing **positional** argument and defaults to the current
directory, so a bare `prereview` just works. Flags can go before or after it
(`prereview --agent ./docs` and `prereview ./docs --agent` are the same), and the
same holds for every subcommand; a bare `--` ends flag parsing.

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
| a single file | **no-git (single file)** | just that file, whole; its store lives under the file's **parent** dir, one per reviewed file |
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
websockets) working with **zero rewriting**, external mode boots **two servers**.
One is the normal prereview UI, the other a reverse proxy on its own port: a
separate origin the UI iframes. Both bind the same `--host`, so both are tailnet-reachable
on a remote box. Stdout gains an extra `PROXY <url>` line (the proxy origin) after
the `ALT` lines and before `REPO`/`STORE`; `READY` is still the UI url.

Annotations anchor to a **URL + region rectangle** rather than a file + line.
They're stored as `kind=region` rows with the page in the `url` column and the
rectangle in the `area` column (0..1 fractions of the page's document, so a re-pin
survives scroll). Like image-area comments they're **frozen** — no content
re-anchoring — and are informational feedback about a page, not file-edit
directives.

`--out` is **required** (there's no repo to default the store to), and `[path]`
and `--base` are **ignored**.

### Agent mode (`--agent`)

Standalone `prereview` is human-only: you review, comments save to
`comments.csv`, and the top-bar button is **Quit**. `--agent` runs the review
**under a coding agent**. It streams the review as a queue of JSON events the
agent consumes with `prereview watch`, and swaps the UI for a **Queue**
(⏸ Pause / ▶ Resume) control plus an **End session** button.

```bash
prereview --agent "$(pwd)"          # what an agent's skill/command runs for you
```

This replaces the old one-shot `--skill` / `--stream` / `.prereview/DONE`
hand-off with a **continuous** model — no re-invocation between rounds, and no
hand-written CSV parser:

- **The queue.** Every comment (and every accepted suggestion) rides a
  `draft → queued → done` lifecycle, with `blocked` and `answered` as the detours. Comments you
  save while **live** are sent to the agent immediately; **Pause** holds them so you
  can batch, and **Resume** releases the whole batch at once. The Queue dropdown
  shows the counts (queued · blocked · answered · done · draft, plus "accepted, awaiting apply"
  and reviewer replies still awaiting the agent) and is the hub for Pause/Resume and
  End session.

  **`blocked`** is enqueued work whose anchor is gone — the commented text was
  rewritten or deleted and prereview will not guess where it moved to. The agent is
  never handed outdated work, so a blocked row cannot progress on its own: re-anchor
  it (the card's **Re-anchor** button) or resolve it. Only a resolve takes a row out
  of the queue; drift never does, because a row that silently disappeared is
  indistinguishable from one the agent finished.

  **`answered`** is a comment the agent settled with a reply and no edit
  (`prereview reply --answered`) — an explanation, or a question back. It is your
  turn: read the reply, then resolve it, or reply to push back (which hands it to the
  agent again). It has no ↺; replying is how you send it back.
- **The event stream.** In agent mode, after the usual `READY`/`REPO`/`STORE`
  preamble prereview emits one JSON object per line to **stdout** and mirrors it to
  `<STORE>/events.jsonl` (append-only, reset each launch). Four event types:
  - **`ready`** (seq 0) — once, after the preamble. Carries `repo`, `csv`, and
    optional `paused` / `skill_updated`.
  - **`snapshot`** — on every queue mutation (debounced). A **full snapshot** of
    the still-actionable queue: `{"event":"snapshot","seq":N,"comments":[…],
    "suggestions":[…]}` (both arrays always present, `[]` when empty). Pre-filtered
    to what needs the agent — unresolved, non-outdated, non-draft, not-yet-`done`
    comments, plus any comment the reviewer just replied on. The consumer dedupes by
    `id`. `prereview comments --json` reports the same set, and both now honour
    `processed.jsonl`, so a comment you marked `done` stops coming back.
  - **`files_changed`** — reviewed files changed on disk under the agent (a
    settled run of edits, or a version restore): `{"event":"files_changed",
    "seq":N,"files":[…],"version":V,"cause":"edit"|"rollback"}`. It adds
    `"agent_watching":true` when a blocking `watch` was attached for every edit
    (so none were the agent's own). The agent re-reads the listed files.
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
| `--public-url <url>` | — | The URL a browser outside this machine uses, such as a sandbox proxy's `https://vm.exe.xyz:4799`. Printed as `READY` and allowed as a WebSocket origin; the bind port defaults to the URL's port. Doesn't change the bind address, and can't be combined with `--external`. See [Behind a proxy or in a sandbox](#behind-a-proxy-or-in-a-sandbox). |
| `--external <url>` | — | Annotate a **live local site** instead of files: reverse-proxies `<url>` on a second origin and overlays region annotation. **Requires `--out`**; ignores `[path]` and `--base`. See [External mode](#external-mode---external). |
| `--out <dir>` | the review path | Store root — the directory under which the store holding `comments.csv` + the event log is created. Available in **every** mode (defaults to the review path, so repo mode is unchanged when omitted); **required** with `--external`. The resolved store is printed as the `STORE` stdout line, which is what the [subcommands](#subcommands) take as `--out`. |
| `--agent` | `false` | Run under a coding agent: stream the review queue as JSON events (consume with `prereview watch`) and show the **Queue** (Pause/Resume) + **End session** UI instead of **Quit**. See [Agent mode](#agent-mode---agent). |
| `--replace` | `false` | prereview refuses to start a second server for the same store (duplicate servers fight over it). `--replace` stops the running one and takes over. Comments auto-save, so nothing is lost. It stops a server only after that server confirms it is the process named in the store's `server.pid`. A server started by an older prereview can't confirm, so stop that one yourself. |
| `--skill` / `--stream` | `false` | **Deprecated aliases of `--agent`** — still enable agent mode, print a deprecation warning. Use `--agent`. |

### Run-and-exit actions

These do one thing and exit — they don't start the server:

| Flag | Effect |
|---|---|
| `--version` | Print the build version. |
| `--install-skill` | Install the prereview integration for one or more coding agents and exit. With no `--client`, shows an interactive menu. Every installed agent's files refresh to match the binary on the next run after any upgrade (`--update`, brew, scoop, `go install`); a file you edited is left alone, and re-running with `--client=<id>` replaces it. See [integrations.md](integrations.md#keeping-the-integration-current). |
| `--client <ids>` | With `--install-skill`, the comma-separated agent(s) to install for: any of `claude,codex,gemini,opencode,aider,cursor`. Empty opens the menu. |
| `--update` | Download and install the latest GitHub release (defers to brew/scoop if one manages the binary). |
| `--uninstall` | Remove the binary (your `.prereview/` review comments are left untouched; defers to brew/scoop). |
| `--no-update` | Never run the on-launch update check, even with `PREREVIEW_AUTO_UPDATE=1` (also `PREREVIEW_NO_UPDATE=1`). The check is off unless `PREREVIEW_AUTO_UPDATE=1` is set. |

## Subcommands

Beyond launching a review, `prereview` has **bare-verb subcommands** the coding
agent uses to read from and write to a running review's store.
They write into the same store the server watches, so their effect shows up live.
You rarely run them by hand — the skill does. Each takes `--out <dir>` — the printed
`STORE` directory (the reviewed file also works, and a directory still means its own
`.prereview/`); defaults to the current dir. Run any with `-h` for its own flags.

```
prereview watch     [--out <dir>] [--since <seq>]
prereview comments  [--out <dir>] [--json] [--all]
prereview done      [--out <dir>] [--file <f>|-] [--all-open] <comment-id>...
prereview status    [--out <dir>] <working|done> [message]
prereview suggest   [--out <dir>] [--file <payload.json>]
- `prereview quiz` - submit a comprehension quiz about a file's diff for the
  reviewer to answer; writes `.prereview/quiz.jsonl`.
prereview reply     [--out <dir>] [--answered] [--check] (--body "…" | --file <f>|-) <id>
prereview applied   [--out <dir>] [--file <f>|-] <suggestion-id>...
prereview reverted  [--out <dir>] [--file <f>|-] <suggestion-id>...
prereview check     [--json] <file>...
```

- **`prereview watch`** — the **one** queue reader. Prints every event after
  `--since <seq>` (default `-1` = from the start), **blocks** for the next when
  caught up, returns a batch so the agent can act, and exits `0` only on the `end`
  event. Loop by re-running with the highest `seq` seen. Reads the durable
  `.prereview/events.jsonl`, and records itself in `agent-cursor.json` (a
  heartbeat while it blocks, the delivered `seq` when it returns) so the
  **Queue** shows the reviewer that something is consuming it. Run it with a long
  timeout (e.g. 600s).
- **`prereview comments`** — list the review's comments from a stable interface
  (no CSV hand-parsing). Defaults to the actionable set; `--all` includes
  resolved/outdated/draft. `--json` emits the **same shape as a `snapshot`**
  (and records the read in `agent-cursor.json` as a pickup), so
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
  `.prereview/agent-replies.jsonl`. `--answered` also settles a **comment** with no
  edit (appends to `.prereview/answered.jsonl`), so it reads "answered" rather than
  "done". `--check` checks the reply and posts nothing. See
  [Threads](../skill/reference.md#threads).
- **`reply`, `suggest` and `quiz` check what they post** against your checks and
  learned checks, then print any findings and the questions of judges scoped to that
  text to stderr. They exit 0 whatever they find. The text is checked as `reply.md`,
  `suggestion.md` (a suggestion's note) or `quiz.md`, so a check's `files:` can pick
  one. A suggestion's proposed text is checked under its own file's path. The
  built-in checks run only on that proposed text.
- **`prereview applied`** — ack that you **applied** an accepted suggestion's edit
  to the file, so the UI flips the card from "accepted" to "applied" and drops it
  from the snapshot. Ids come from a snapshot's `suggestions[]` (`verdict=accept`);
  validated against suggestions. Idempotent. Appends to `.prereview/applied.jsonl`.
- **`prereview reverted`** — ack that you **reverted** an applied suggestion —
  restored its `original` text after the reviewer asked to undo the accept
  (delivered as `verdict=revert`). Nets the suggestion back out of "applied", so
  the reviewer's card returns to undecided. Validated against suggestions.
  Idempotent. Appends to `.prereview/reverted.jsonl`.

- **`prereview judge`** — the coding agent's answer to a "Run judges" request.
  Make one call per judge, copied from the request:
  `prereview judge --out <STORE> --file <f> --judge <id> --content-hash <h>
  --rubric-hash <h> --verdict pass|fail --critique "…" [--quote "…"] [--model <id>]`. Appends the verdict (with the
  request's file and rubric hashes, so the review can tell when it's out of date) to
  the store's `judgments.jsonl`. Validated before anything is written. Answering a
  benchmark run, `--bench <id> --run <run> --case <case>` replaces `--out`/`--file`:
  the verdict goes to the benchmark's `runs.jsonl`, checked against the run's pinned
  case and rubric hashes.
- **`prereview learn wait`** — the background learner's loop step. `prereview learn
  wait [--out <STORE>] [--timeout 9m]` claims the next **learning job** ("Learn after
  each review", or "Read past reviews") and prints `job <id>`, a blank line and the
  job. It prints `idle` at the timeout, or `stop` once the review `--out` names has
  ended. While it waits, the review knows a learner is running. **`prereview learn
  finish <id>`** closes a job.
- **`prereview learn propose`** — the learner's answer to a job, one call per lesson:

  ```
  prereview learn propose --job <id> --text "…" --evidence <store>:<id> [--evidence …]
    [--scope "…"] [--glob <pattern>]… [--replaces <id>]
    [--produces instruction,checker,judge] [--checker <rule.yml|->] [--judge <rubric.md|->]
  ```

  `--checker`/`--judge` attach a
  drafted rule or rubric (validated; never a command check) that lands in your
  checks folder when the lesson is accepted.
  Every `--evidence` must name one of the reviewer's signals in one of the job's
  stores, or nothing is written. A signal is a comment id, the reviewer's thread
  reply as `<target_id>@<at>`, or the id of a suggestion they accepted or rejected. The
  agent's replies, suggestions and quiz questions are context, never evidence. The lesson is appended to `~/.config/prereview/learnings.jsonl`:
  *accepted* when the job was queued with "Accept automatically" on (the reviewer is
  told and can undo it), otherwise *proposed*, waiting in Library → Learnings.
  Without `--job` it is always proposed. **`prereview learn list [--status s]
  [--json]`** prints the library.
- **`prereview check`** — run the review's [checks](usage.md#checks) on files
  from the terminal. It runs the built-in prose rules and your
  `~/.config/prereview/checks/` rules. It also runs the repo's Vale config when
  you've trusted it on the Checks page and `vale` is installed (run it from the
  repo root). It prints each finding as
  `path:line:col: severity: message (check)`. It exits `1` when any check fails or
  errors, so an agent can self-check prose before `prereview done`. `--json` prints every check's result per file. Checks
  you turned off on the Checks page are skipped. Takes no `--out`: it reads files, not a
  review's store.
- **`prereview check --hook`** — the command of the Claude Code hook prereview adds
  while you have learned skills ([Skills](usage.md#skills)). It reads the hook's input
  on stdin and, for a prose file, runs the same checks as `prereview check`. It
  reports only the findings on the lines the edit wrote (the whole file for a Write)
  and exits `2`, which shows them to Claude. It exits `0` when there's nothing to
  report, and `1` on an error, which Claude Code shows you without blocking the edit.

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

`--host` defaults to `127.0.0.1` and is smart on remote boxes. On an SSH host
with a tailnet, prereview binds the host's **Tailscale IP**, so you can open the
review from your phone over the tailnet and never the public internet. A remote box
with no tailnet stays on `127.0.0.1` and prints a stderr warning telling you to
pass an explicit `--host`. Passing `--host` explicitly is an absolute override
(never auto-rebound). **Avoid `0.0.0.0`**: it exposes the source diff on every
interface, including any public IP. The first stdout line is `READY <url>`; extra
`ALT <url>` lines (e.g. the MagicDNS hostname) may follow.

Every printed URL carries `?token=…`, a secret made fresh at each launch. Opening
it sets a cookie for that server and drops the token from the address bar; a
request without the cookie or the token gets 403. A restart makes a new token, so
an open tab needs the new link.

### Behind a proxy or in a sandbox

A sandbox that runs your agent, such as exe.dev or GitHub Codespaces, usually
lets you reach a port through its own HTTPS proxy. Give prereview that address
with `--public-url` (or `PREREVIEW_PUBLIC_URL`):

```bash
prereview --agent --public-url https://<vm>.exe.xyz:4799
```

- **`READY` prints the public URL**, so the link your agent shows you is the one
  that opens from outside. The local URL moves to an `ALT` line.
- **The bind port comes from the URL** (4799 above). A URL with no port of its
  own, such as Codespaces' `https://<name>-4799.app.github.dev`, needs `--port
  4799` as well.
- **The bind address doesn't change.** It stays `127.0.0.1`, or the Tailscale IP
  on a remote box. If the proxy reaches the machine on its network address
  rather than on loopback, add `--host 0.0.0.0`. Do this only behind a private
  proxy: anyone who has the printed link can drive the review.
- **The WebSocket origin check.** A proxy that rewrites the `Host` header, or
  that drops `X-Forwarded-Proto`, fails livetemplate's same-origin check. The
  page then loads but never updates. The server logs `WebSocket upgrade failed …
  request origin not allowed by Upgrader.CheckOrigin`. `--public-url` adds the public URL to the allowed
  origins, together with every URL prereview prints (`READY`, the `ALT` lines,
  and `localhost` on a loopback bind). An address it never printed, such as an
  `ssh -L` forward on a different local port, won't go live while
  `--public-url` is set.

Per sandbox:

| Sandbox | Command | Notes |
|---|---|---|
| exe.dev | `prereview --agent --public-url https://<vm>.exe.xyz:<port>` | exe.dev forwards ports 3000–9999 at `https://<vm>.exe.xyz:<port>/`, and only users with access to the VM can open them ([exe.dev proxy docs](https://exe.dev/docs/proxy)). Its docs don't say whether the proxy connects on loopback; if the page doesn't load, add `--host 0.0.0.0`. |
| GitHub Codespaces | `prereview --agent --port 4799 --public-url https://$CODESPACE_NAME-4799.$GITHUB_CODESPACES_PORT_FORWARDING_DOMAIN` | Forwarded ports are private to you by default. |

`--external` can't run behind a one-port proxy: it serves the live site from a
second, random port. prereview refuses `--external` with `--public-url`.

## Environment

- `PREREVIEW_PROMPTS_DIR` - override the suggestion-prompt overlay directory
  (default `~/.config/prereview/prompts`). Each `*.md` file there adds a row to the
  file header's **Ask for suggestions** menu. The filename minus `.md` is its id,
  the first `# Heading` is the menu title, and the rest is the prompt text that
  pre-fills the comment box. Reuse a built-in's filename (see
  [`internal/review/builtin_prompts/`](../internal/review/builtin_prompts/)) to
  replace it. A file with no text is skipped. prereview re-reads the folder each time a tab connects,
  so reload the tab after editing. End the prompt by asking for `prereview suggest`
  edits, as the built-ins do, so the agent answers with boxes you can accept.
- `PREREVIEW_SHORTCUTS_DIR` - override the comment-shortcut overlay directory
  (default `~/.config/prereview/shortcuts`). Each `*.md` file adds a chip to the
  line, text and block comment boxes. The format matches the prompt folder above:
  the first `# Heading` is the chip's label and the rest is the text it adds. Reuse a
  built-in's filename (see
  [`internal/review/builtin_shortcuts/`](../internal/review/builtin_shortcuts/)) to
  replace it.
- `PREREVIEW_QUIZZES_DIR` - override the quiz-prompt overlay directory
  (default `~/.config/prereview/quizzes`). Drop a `*.md` file there to add your
  own quiz prompt, or reuse a built-in's filename to replace it. The prompt is
  guidance only: prereview still enforces the question schema and still checks
  that every cited line exists in the diff, whatever prompt produced the quiz.

| Var | Effect |
|---|---|
| `PREREVIEW_AUTO_UPDATE=1` | Check for a new release on launch (at most hourly) and install it. Off by default, so prereview makes no network calls. Package-manager installs (Homebrew, Scoop) never self-update. |
| `PREREVIEW_NO_UPDATE=1` | Same as `--no-update`: never run the on-launch update check. |
| `PREREVIEW_PUBLIC_URL` | Same as `--public-url`; the flag wins when both are set. |
| `PREREVIEW_CHECKS_DIR` | Override the checks rule folder (default `~/.config/prereview/checks`). |
| `PREREVIEW_LIBRARY_DIR` | Override the per-user library folder: learnings, sessions, the first-launch answer, and the default `checks`, `prompts` and `quizzes` folders inside it (default `~/.config/prereview`). |
| `PREREVIEW_SCAN_HOME` | Where the first-launch scan looks for past reviews (default your home folder). |
| `PREREVIEW_HOME` | The home folder prereview writes agent integrations under: its own skill (`--install-skill`, the startup sync) and generated skills (default your home folder). |

## Output

`<store>/comments.csv` is the source of truth (RFC-4180, **19 columns**, atomically
written via tmp+fsync+rename). The store is `<store-root>/.prereview/`, where the
store root is the review path by default and `--out` redirects it in any mode (and is
required with `--external`).

A **single-file** review gets its own store per reviewed file, at
`<parent>/.prereview/files/<name>-<hash>/`. That is what lets two files in one
directory be reviewed at the same time. They used to share a store *and* the "one
server per store" lock, so launching a review of a second file evicted the first
(#199). It also means a single-file review and a directory review of the same
directory are separate reviews that do not see each other's comments. Upgrading from
a prereview that predates #199 copies the reviewed file's rows — with their done
markers and thread replies — out of the old shared `comments.csv`, which is itself
left untouched.

The `STORE` line on stdout always points at the resolved store — pass it to a
subcommand's `--out` rather than assembling the path.

The header (column order is the contract; columns are only ever appended, so older
CSVs may have fewer trailing columns — index by position, default missing ones to
empty):

```
id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url,from_col,to_col,hidden,enqueued,request,check
```

`kind` is `line` (default), `text` (a character range within a line, via
`from_col`/`to_col` rune offsets), `file`, `area` (an image rectangle, `area`
holds `{x,y,w,h}` fractions), or `region` (a live-site rectangle from `--external`,
anchored to `url`). `hidden` and `enqueued` are reviewer/queue view flags; `request`
marks a row prereview queued as a request to the agent (`judge`, `benchmark`,
`quiz`; `extraction` on rows from before learning moved to background jobs),
empty for the reviewer's own comments. See
[skill/reference.md](../skill/reference.md#csv-schema) for the full column docs.

Alongside `comments.csv`, `.prereview/` holds a set of sidecar files. Those the
**agent** writes (via the subcommands) the server reads, and vice-versa:

| File | Written by | Purpose |
|---|---|---|
| `comments.csv` | server | the comments (source of truth) |
| `events.jsonl` | server | the agent-mode event log (`ready`/`snapshot`/`files_changed`/`end`); reset each launch |
| `suggestions.jsonl` | **agent** (`prereview suggest`) | proposed edits, rendered as suggestion boxes (durable) |
| `suggestion-decisions.jsonl` | server | your accept/reject/revert verdicts (durable) |
| `processed.jsonl` | **agent** (`prereview done`) | comment ids marked done (durable — NOT reset each launch, so "worked on" survives a relaunch) |
| `answered.jsonl` | **agent** (`prereview reply --answered`) | comment ids settled by a reply with no edit (durable) |
| `applied.jsonl` | **agent** (`prereview applied`) | accepted-suggestion apply acks (durable) |
| `reverted.jsonl` | **agent** (`prereview reverted`) | applied-suggestion revert acks (durable) |
| `agent-replies.jsonl` | **agent** (`prereview reply`) | agent thread replies (durable) |
| `reviewer-replies.jsonl` | server | reviewer thread replies (durable) |
| `llm-status.json` | **agent** (`prereview status`) | the agent's live working/done status (reset each launch) |
| `agent-cursor.json` | **agent** (`prereview watch`, `comments --json`) | who is reading the queue: a watch heartbeat or a one-shot read, plus the last `seq` delivered — drives the Queue's agent line (reset each launch) |
| `versions/` | server | per-file version checkpoints + content blobs and their changelog (durable) |

When an agent finishes a batch that edited files, prereview snapshots a new
**version** of each changed file into `versions/`. The agent's `prereview status
done` message becomes that version's changelog entry, shown in the file's
**Versions** panel (View / Diff / Restore). See
[skill/reference.md](../skill/reference.md#filesystem-layout) for the full layout.
