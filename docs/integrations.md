# Integrations — driving prereview from any LLM CLI

prereview captures **your** review as structured comments, then an LLM applies
the fixes. The capture side is the same everywhere; only the *agent* that reads
the comments differs. This page documents the open protocol every agent consumes
and gives a verified recipe per supported CLI.

> **TL;DR** — `prereview --install-skill --client=<id>` installs the right
> command/skill/rules file for your agent. Run it with no `--client` to pick
> from a menu. The agent launches `prereview --agent` and reads the review queue:
> Claude Code (and any harness that can block on a stream) uses the live
> `prereview watch` loop; every other agent polls `prereview comments --json`
> once per turn. Both apply the same comments.

## prereview needs no API key

prereview is a local tool: launching it, capturing comments, and writing
`.prereview/comments.csv` use no network and no credentials. The *agent* that
applies your comments runs its own model to do so — via whatever you've already
set up for that agent (a login, an API key, or a **local model** like
ollama/lmstudio). That model requirement belongs to the agent, not to prereview;
prereview adds nothing on top. Both end-to-end smoke tests were run **keyless**:
opencode with its free hosted models, and aider against a local ollama model —
no API key anywhere.

## The protocol (this is all an agent needs)

When an agent runs `prereview --agent`, it writes everything to a `.prereview/`
directory (printed as the `REPO` line on stdout) and streams a JSON event log.
Three things matter to an agent — the last two are two views of the same queue:

### 1. `.prereview/comments.csv` — the source of truth

RFC-4180 quoted, one row per comment, **17 columns**:

```
id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url,from_col,to_col,hidden,enqueued
```

You rarely parse this directly — `prereview comments --json` (below) returns the
same data pre-filtered. If you do read it, act on every row where **`resolved` is
not `true`** and **`anchor_status` is not `outdated`** — that filtered set is the
complete list of still-actionable comments. `kind` is `line` (default), `text` (a
character range within a line; `from_col`/`to_col` are the 0-based rune offsets),
`file`, `area` (an image rectangle, `area` column holds `{x,y,w,h}` fractions), or
`region` (a live-site rectangle anchored to `url`). `hidden` and `enqueued` are
reviewer/queue view flags — ignore them. Older CSVs may have fewer trailing
columns; index by position and default missing ones to empty. Full column docs:
[skill/reference.md](../skill/reference.md#csv-schema).

### 2. `prereview comments --json` — the portable read (poll)

```bash
prereview comments --out <REPO> --json
```

Returns the still-actionable set (resolved / outdated / draft already excluded) as
JSON — the **same shape as a stream snapshot**, no CSV parsing. Re-run it whenever
the reviewer says they've commented (or clicked **End session**), apply the rows
as one coherent change, and dedupe by `id` across turns. This one command is
enough to drive a fix — no event stream required. That is what makes prereview
agent-agnostic.

### 3. `prereview watch` — the live stream (block)

```bash
prereview watch --out <REPO> --since <seq>
```

In agent mode prereview emits one JSON object per line to stdout and to
`.prereview/events.jsonl`:

- `{"event":"ready","seq":0,...}` — session is live (carries `repo`, `csv`, and
  optional `paused` / `skill_updated`).
- `{"event":"snapshot","seq":N,"comments":[…],"suggestions":[…]}` — the queue
  changed. `comments` is a full snapshot of every still-actionable comment (already
  filtered as above); `suggestions` is the reviewer's decisions on edits *you*
  proposed (see [Suggesting edits](#suggesting-edits)). Both arrays are always
  present (`[]` when empty); dedupe by `id`.
- `{"event":"end","seq":N}` — the reviewer clicked **End session**; the server
  exits. The **only** terminator.

`watch` prints every event after `--since`, blocks for the next when caught up,
returns a batch, and exits on `end` — loop by re-running with the highest `seq`
seen. **Never** hand-roll `tail -f` on the log; `watch` is the one reader (it
handles catch-up + follow + the per-launch log reset). This is the loop one
long-lived agent uses to process many rounds without re-invocation — which, in
practice, only Claude Code does reliably (see
[Why most agents poll instead](#why-most-agents-poll-instead)).

### Marking your work

- **`prereview done --out <REPO> <id>…`** — after each edit that addresses a
  comment, mark it **done** so the reviewer sees a badge (an unmarked comment
  looks ignored). Ids are validated against the review; an unknown id fails. Use
  `--all-open` once you've handled the whole batch.
- **`prereview status --out <REPO> working|done [message]`** — echo a live pill
  across every open browser tab. Keep the message short; the `done` message
  doubles as the file's version changelog entry.
- **`prereview reply --out <REPO> <id> --body "…"`** — post a one-line thread
  reply saying what you changed. The reviewer can reply back to steer you; a
  comment whose last thread entry is the reviewer's comes back in the next
  snapshot for you to address.

### Suggesting edits

The protocol also runs the other way: an agent can **propose** edits the reviewer
accepts, rejects, or later reverts.

- **Submit** proposals with `prereview suggest` (a JSON payload on stdin or
  `--file`) — they append to `.prereview/suggestions.jsonl` and render inline as
  before → after boxes. Each: `{id, file, from_line, to_line, side, original,
  proposed, note}`; re-use an `id` to replace that suggestion.
- **Receive** the reviewer's decisions in each snapshot's `suggestions[]` array
  (one entry per decided, non-outdated suggestion): `{id, file, from_line,
  to_line, side, verdict, note, original, proposed, anchor_status}`. Act by
  `verdict`:
  - **`accept`** → apply the edit (replace `original` with `proposed` at those
    lines — *you* write the file), then **`prereview applied <id>`** to ack it.
    That flips the reviewer's card to "applied" and drops it from the snapshot.
  - **`reject`** → drop it; dedupe by `id` and skip it.
  - **`revert`** → the reviewer undid an edit you already applied; restore the
    `original` text over your `proposed`, then **`prereview reverted <id>`** to ack.
  Only `ok`/`moved` statuses are emitted (both have trustworthy line numbers); an
  applied accept drops off later snapshots on its own.

prereview never edits your files — you do — so an accepted edit shows up as a
normal diff you can commit. Don't end your turn with an unapplied `accept`: once
you stop watching, nothing else will ever apply it.

## Why most agents poll instead

prereview's `watch` loop asks the agent to (a) run the server in the background
and (b) block-read a stream across many turns. Among current CLIs only Claude Code
supports both robustly. The others either can't detach a background process or
hang on a blocking read (verified against open issues in each repo), and aider has
no watch mode at all. So for every non-Claude agent the integration polls
`prereview comments --json` once per turn instead:

1. You run prereview (the installed command does this) and review in the browser.
2. You comment, then tell the agent (or it re-reads on your next turn).
3. The agent runs `prereview comments --json`, applies the actionable rows as one
   coherent change, `prereview done`s each, and reports back.
4. Comment more and repeat; click **End session** when finished.

The reviewer drives the loop turn-by-turn instead of the agent blocking on a
stream. Same comments, same result — just not a single uninterrupted process.

---

## Per-client recipes

`prereview --install-skill --client=<id>` writes these for you. The manual
form is shown so you can see exactly what lands on disk and adapt it.

**What the labels mean.** Based on smoke tests run 2026-06-21:

| Label | Meaning |
|---|---|
| ✅ tested end-to-end | an agent discovered the integration, read the comments, and applied the fix |
| 🟡 integration verified | the agent loads/runs our integration correctly, but a full review wasn't completed in the test env (model auth/entitlement unavailable) |
| 🔶 documented | installs to the right place in the right format, built from the tool's current docs; not yet exercised (no credentials in the test env) |

What was actually run (all keyless — opencode uses free hosted models, aider a
local ollama model): **opencode** and **aider** passed end-to-end (each
discovered the integration, read the comments, and fixed the file); Claude
Code is the reference. **codex** loaded the skill from *both* candidate dirs and
attempted to act, but neither the test ChatGPT account (no entitlement) nor a
small local model (can't drive codex's tool harness) completed the apply.
**gemini** discovered and expanded `/prereview` in headless mode (the apply
needs a valid key/login). **cursor-agent** is install/format-verified only.
Please confirm in your own setup and open an issue if anything misbehaves.

### Claude Code  ✅ tested end-to-end

- **Installs:** `~/.claude/skills/prereview/SKILL.md` + `reference.md`
- **Mode:** live `prereview watch` loop, multi-round (the full continuous loop).
- **Invoke:** `/prereview`, or just *"review my changes"*.

This is the reference integration; see [skill/SKILL.md](../skill/SKILL.md).

### OpenAI Codex CLI  🟡 integration verified — skill loads from both dirs

> Smoke-tested 2026-06-21 (codex 0.130.0): the skill **loaded cleanly from
> both** `~/.codex/skills/` and `~/.agents/skills/`, confirming the dual-write
> is correct. The apply step couldn't run because the test ChatGPT account
> lacked model entitlement — not a prereview issue.

- **Installs:** a `SKILL.md` to **both** `~/.codex/skills/prereview/` and
  `~/.agents/skills/prereview/`. Codex is mid-migration to the agent-agnostic
  `~/.agents/skills` location; different versions scan different dirs, so the
  installer writes both to be safe. Confirm discovery with `/skills` in a fresh
  session.
- **Mode:** poll (`prereview comments --json`) — Codex's shell tool is synchronous;
  backgrounded / blocking commands hang
  ([openai/codex#7187](https://github.com/openai/codex/issues/7187)).
- **Invoke:** mention `$prereview` in your message, or select it via `/skills`.
- **Note:** custom prompts (`~/.codex/prompts/*.md`) are deprecated in favour of
  skills; the installer uses skills.

### Gemini CLI  🟡 integration verified — `/prereview` discovered in headless

> Smoke-tested 2026-06-21 (gemini 0.47.0): `gemini -p "/prereview …"` **resolved
> and expanded the custom command** and started a turn, failing only at the model
> API call (no valid key in the test env) — so discovery works. Two auth notes:
> the **OAuth ("Login with Google") flow needs an interactive terminal** to
> finish (it can't complete the paste-back step in a non-interactive/piped
> session); the simplest path is a free **`GEMINI_API_KEY`** from
> <https://aistudio.google.com/apikey>. Also pass `--skip-trust` (or
> `GEMINI_CLI_TRUST_WORKSPACE=true`) for headless runs in an untrusted folder.

- **Installs:** `~/.gemini/commands/prereview.toml` (a custom command; filename
  → `/prereview`).
- **Mode:** poll (`prereview comments --json`). (Gemini *can* background with
  `run_shell_command` + `&`, but that path is flaky —
  [google-gemini/gemini-cli#13594](https://github.com/google-gemini/gemini-cli/issues/13594) —
  so the installed command polls.)
- **Invoke:** `/prereview`.

### opencode  ✅ tested end-to-end

> Smoke-tested 2026-06-21 (opencode 1.17.9): `opencode run --command prereview`
> discovered the command, read the comments, and applied the fix to a seeded repo
> using a free model. Full loop confirmed.

- **Installs:** `~/.config/opencode/commands/prereview.md` (markdown command;
  body is the prompt template).
- **Mode:** poll (`prereview comments --json`) — backgrounded children hang the
  bash tool ([sst/opencode#20902](https://github.com/sst/opencode/issues/20902);
  for headless use, allow shell permissions or pass
  `--dangerously-skip-permissions`).
- **Invoke:** `/prereview`, or headless `opencode run --command prereview`.

### aider  ✅ tested end-to-end (keyless, local model)

aider has no user-global command registry, so the installer writes an executable
**wrapper script** that runs `aider --message --read … --yes`.

> Smoke-tested 2026-06-21 (aider 0.86.2, keyless via a local ollama model): the
> wrapper applied the seeded review (`a - b` → `a + b`) end-to-end.
>
> **Why a wrapper, not a `--load` script:** testing showed `/code` inside a
> `--load` script is **skipped** in non-interactive mode ("only supported in
> interactive mode") and never applies edits — it just littered a stray file.
> `aider --message` *does* apply edits headlessly, so the wrapper uses that with
> `--read .prereview/comments.csv` for context.
>
> **Two practical notes:** aider needs **Python 3.12** (on 3.13 its `pydub`
> dependency fails — `audioop` was removed from the 3.13 stdlib; install with
> `uv tool install --python 3.12 aider-chat`). And with a **small/local model**,
> add `--edit-format diff` — weak models botch aider's default whole-file format
> (they emit prose like "Updated calc.py" that aider misreads as a filename);
> the SEARCH/REPLACE `diff` format is far more reliable. The wrapper forwards
> `"$@"`, so just append the flag.

- **Installs:** `~/.config/prereview/aider/prereview-aider.sh` (executable)
- **Mode:** poll. aider is one-instruction-then-exit by design; it can't watch a
  stream, so the wrapper reads `.prereview/comments.csv` for context per run.
- **Invoke:** `~/.config/prereview/aider/prereview-aider.sh <files…>` (run from
  the repo root, after commenting in prereview). Add `--edit-format diff` for
  small models; set the model with `--model …` or `AIDER_MODEL`.

### cursor-agent  🔶 documented — install location unconfirmed; run needs a key

Cursor's `.cursor/commands` is documented as an *editor* feature; the CLI's
honoring of it is unconfirmed. The CLI **does** reliably read **project**
`.cursor/rules/*.mdc` and project-root `AGENTS.md`. Research did **not** confirm
that a **global** `~/.cursor/rules/` is loaded by the CLI, so the global install
may sit in a dead location.

- **Installs:** `~/.cursor/rules/prereview.mdc` (global, **unconfirmed** — see
  above).
- **Make it reliable:** copy that file into your **project's**
  `.cursor/rules/prereview.mdc` (confirmed-read), or paste its body into the
  project's `AGENTS.md`.
- **Mode:** poll (`cursor-agent -p` runs non-interactively with shell access;
  long blocking loops are unverified).
- **Invoke:** `cursor-agent -p --force "use prereview to apply the review comments"`.

---

## Bring your own agent

Any agent works if it can run `prereview comments --json` and edit files. The
generic instruction is:

> Run `prereview --agent "$(pwd)" &`. Tell the user the printed `READY <url>`
> (as a clickable Markdown link) and to click **End session** when done. Whenever
> they've commented (or on your next turn), run `prereview comments --out <REPO>
> --json`, take every returned row, and apply them as one coherent change. Use
> `file` + `from_line`/`to_line` to locate each comment and `body` for intent;
> dedupe by `id` across turns. After each edit, `prereview done --out <REPO> <id>`.
> Echo `prereview status --out <REPO> working|done`. Repeat until End session.

Drop that into your agent's instruction/command/rules file and you're done. To
also **propose** edits, add: *submit suggestions with `prereview suggest`
(a JSON payload), and on each read apply the `suggestions[]` decisions —
`accept` → make the edit then `prereview applied <id>`, `reject` → skip,
`revert` → restore `original` then `prereview reverted <id>`.*

## Status summary

✅ tested end-to-end · 🟡 integration verified (full run not completed in test
env) · 🔶 documented (install + format verified; not yet exercised). Smoke tests
run 2026-06-21.

| Agent | Path | Status |
|---|---|---|
| Claude Code | `~/.claude/skills/prereview/` | ✅ tested end-to-end |
| opencode | `~/.config/opencode/commands/prereview.md` | ✅ tested end-to-end (free model) |
| aider | `~/.config/prereview/aider/prereview-aider.sh` | ✅ tested end-to-end keyless (local model; needs Python 3.12; `--edit-format diff` for small models) |
| OpenAI Codex CLI | `~/.codex/skills/` **and** `~/.agents/skills/` | 🟡 skill loads from both dirs; apply needs a capable model (account entitlement, or a strong local model) |
| Gemini CLI | `~/.gemini/commands/prereview.toml` | 🟡 `/prereview` discovered + expanded in headless; apply needs `GEMINI_API_KEY` (OAuth login needs an interactive terminal) |
| cursor-agent | `~/.cursor/rules/prereview.mdc` | 🔶 install only; run needs `CURSOR_API_KEY`; global rules path unconfirmed |
