# Integrations — driving prereview from any LLM CLI

prereview captures **your** review as structured comments, then an LLM applies
the fixes. The capture side is the same everywhere; only the *agent* that reads
the comments differs. This page documents the open protocol every agent consumes
and gives a verified recipe per supported CLI.

> **TL;DR** — `prereview --install-skill --client=<id>` installs the right
> command/skill/rules file for your agent. Run it with no `--client` to pick
> from a menu. Claude Code gets the live streaming loop; every other agent uses
> a one-shot-per-batch flow.

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

When you run `prereview`, it writes everything to a `.prereview/` directory
(printed as the `REPO` line on stdout). Two things matter to an agent:

### 1. `.prereview/comments.csv` — the source of truth

RFC-4180 quoted, one row per comment, 16 columns:

```
id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind,area,url,from_col,to_col,hidden
```

An agent applying a review reads this file and acts on every row where
**`resolved` is not `true`** and **`anchor_status` is not `outdated`** — that
filtered set is the complete list of still-actionable comments. `kind` is `line`
(default), `text` (a character range within a line; `from_col`/`to_col` are the
0-based rune offsets), `file`, `area` (an image rectangle, `area` column holds
`{x,y,w,h}` fractions), or `region` (a live-site rectangle anchored to `url`).
`hidden` is a reviewer-only view flag — ignore it. Older CSVs may have fewer
trailing columns; index by position and default missing ones to empty. Full
column docs: [skill/reference.md](../skill/reference.md).

This file alone is enough to drive a fix — no stream required. That is what
makes prereview agent-agnostic.

### 2. `--stream` — a JSON event stream (optional, Claude-style loop)

With `--stream`, prereview emits one JSON object per line to stdout and to
`.prereview/events.jsonl`:

- `{"event":"ready",...}` — session is live.
- `{"event":"handoff","seq":N,"comments":[…],"suggestions":[…]}` — the user clicked
  **Hand off →**. `comments` is a full snapshot of every still-actionable comment
  (already filtered as above); `suggestions` is the user's decisions on edits *you*
  proposed (see [Suggesting edits](#suggesting-edits)). Both arrays are always
  present (`[]` when empty); dedupe by `id`.
- `{"event":"session_end","seq":N}` — the user clicked **End session**; the
  server exits.

Streaming lets one long-lived agent process round its way through many hand-offs
without re-invocation. **This requires the agent to background the server and
block-read the stream across rounds** — which, in practice, only Claude Code
does reliably (see [Why most agents use one-shot mode](#why-most-agents-use-one-shot-mode)).

### Without `--stream` — the `DONE` marker (one-shot)

In plain `--skill` mode the UI shows **Hand off →** / **Quit**. Clicking Hand
off writes `.prereview/DONE`. An agent launches the server, tells you the URL,
waits until you've commented and clicked Hand off, then reads `comments.csv` and
applies the open comments. This is the portable flow every non-Claude agent
uses.

### Suggesting edits

The protocol also runs the other way: an agent can **propose** edits the user
accepts, rejects, or asks to revise.

- **Submit** proposals with `prereview suggest` (a JSON payload on stdin or
  `--file`) — they append to `.prereview/suggestions.jsonl` and render inline as
  before → after boxes. Each: `{id, file, from_line, to_line, side, original,
  proposed, note}`; re-use an `id` to revise that suggestion.
- **Receive** the user's decisions in each `--stream` hand-off's `suggestions[]`
  array (one entry per decided, non-outdated suggestion): `{id, file, from_line,
  to_line, side, verdict, note, original, proposed, anchor_status}`. Act by
  `verdict`: **`accept`** → apply the edit (replace `original` with `proposed` at
  those lines — *you* write the file); **`reject`** → drop it; **`revise`** →
  rework it and re-submit with the same `id` and new `proposed`. Only `ok`/`moved`
  statuses are emitted (both have trustworthy line numbers); an applied accept
  drops off later hand-offs on its own.

`prereview processed <id>…` marks comments **worked on** (a UI badge), the same
append-only, agent-writes / server-reads shape.

## Why most agents use one-shot mode

prereview's streaming loop asks the agent to (a) run the server in the
background and (b) block-read a file across many turns. Among current CLIs only
Claude Code supports both robustly. The others either can't detach a background
process or hang on a blocking read (verified against open issues in each repo),
and aider has no streaming/watch mode at all. So for every non-Claude agent the
integration is **one-shot-per-batch**:

1. You run prereview (the installed command does this) and review in the browser.
2. You click **Hand off →** (or finish and the agent reads on your next turn).
3. The agent reads `.prereview/comments.csv`, applies the unresolved rows as one
   coherent change, and reports back.
4. Comment more and repeat, or click **End session** / quit.

The human drives the loop turn-by-turn instead of the agent blocking on a
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
discovered the integration, read `comments.csv`, and fixed the file); Claude
Code is the reference. **codex** loaded the skill from *both* candidate dirs and
attempted to act, but neither the test ChatGPT account (no entitlement) nor a
small local model (can't drive codex's tool harness) completed the apply.
**gemini** discovered and expanded `/prereview` in headless mode (the apply
needs a valid key/login). **cursor-agent** is install/format-verified only.
Please confirm in your own setup and open an issue if anything misbehaves.

### Claude Code  ✅ tested end-to-end

- **Installs:** `~/.claude/skills/prereview/SKILL.md` + `reference.md`
- **Mode:** streaming, multi-round (the full live loop).
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
- **Mode:** one-shot per batch (Codex's shell tool is synchronous; backgrounded
  / blocking commands hang — [openai/codex#7187](https://github.com/openai/codex/issues/7187)).
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
- **Mode:** one-shot per batch. (Gemini *can* background with `run_shell_command`
  + `&`, but that path is flaky —
  [google-gemini/gemini-cli#13594](https://github.com/google-gemini/gemini-cli/issues/13594) —
  so the installed command uses the one-shot flow.)
- **Invoke:** `/prereview`.

### opencode  ✅ tested end-to-end

> Smoke-tested 2026-06-21 (opencode 1.17.9): `opencode run --command prereview`
> discovered the command, read `comments.csv` + `DONE`, and applied the fix to a
> seeded repo using a free model. Full loop confirmed.

- **Installs:** `~/.config/opencode/commands/prereview.md` (markdown command;
  body is the prompt template).
- **Mode:** one-shot per batch (backgrounded children hang the bash tool —
  [sst/opencode#20902](https://github.com/sst/opencode/issues/20902); for headless
  use, allow shell permissions or pass `--dangerously-skip-permissions`).
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
- **Mode:** one-shot. aider is one-instruction-then-exit by design; it can't
  watch a stream.
- **Invoke:** `~/.config/prereview/aider/prereview-aider.sh <files…>` (run from
  the repo root, after handing off in prereview). Add `--edit-format diff` for
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
- **Mode:** one-shot per batch (`cursor-agent -p` runs non-interactively with
  shell access; long blocking loops are unverified).
- **Invoke:** `cursor-agent -p --force "use prereview to apply the review comments"`.

---

## Bring your own agent

Any agent works if it can read `.prereview/comments.csv` and edit files. The
generic instruction is:

> Run `prereview --skill "$(pwd)" &`. Tell the user the printed `READY <url>`
> and to click **Hand off →** when done. When `.prereview/DONE` appears (or the
> user says they're done), read `.prereview/comments.csv`, take every row with
> `resolved != true` and `anchor_status != outdated`, and apply them as one
> coherent change. Use `file` + `from_line`/`to_line` to locate each comment and
> `body` for intent. Repeat per hand-off; stop when the user is finished.

Drop that into your agent's instruction/command/rules file and you're done. To
also **propose** edits, add: *submit suggestions with `prereview suggest`
(a JSON payload), and on each hand-off apply the `suggestions[]` decisions —
`accept` → make the edit, `reject` → skip, `revise` → re-submit the same `id`
with new `proposed` text.*

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
