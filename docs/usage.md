# Using prereview

**prereview** is a local review tool: you read a diff (or any files, Markdown,
images, or a live local site) in the browser, leave comments, and hand the batch
to your coding agent to act on. Everything is local — comments live in a plain
`.prereview/comments.csv` next to what you're reviewing. No account, no cloud.

This page is a quick tour of the app. Press <kbd>?</kbd> anytime for the full
list of keyboard shortcuts.

## Reviewing

**Comment on a line.** Tap a line to anchor a comment; tap another line to
extend the range, or tap the first again to reseat it. Type, then **Save**. The
gutter numbers are permalinks — the URL hash tracks your selection, so you can
copy the link to reopen or share exactly what you were looking at.

**Comment on part of a line.** Select a word, phrase, or multi-line span (drag
with a mouse, long-press on a phone, or use the keyboard caret) and a floating
**Comment** button appears — the comment anchors to exactly those characters and
the span is highlighted when you come back to it.

**Comment on a whole file** with the **Comment on file** button — useful for
binary, deleted, or unchanged files where there's no line to click. The file
drawer shows *changed files only* by default; the **show all** toggle reveals
the full tree when you want to comment on something that didn't change.

**Markdown and HTML render by default.** Tap a rendered block — a heading,
paragraph, list item — to select its *source* lines, so the comment still
anchors to real line numbers and round-trips with the raw view. The
**Preview ⇄ Raw** toggle switches to source; long documents get a
table-of-contents sidebar.

**Annotate an image region.** On an image, drag a rectangle to select an area
and comment on it. The box is stored as fractions of the image, so it survives
re-encoding and resizing.

**Annotate a live local site.** Started with `--external <url>` (see below),
prereview proxies a running dev server so you can drag a box on any part of the
live page and comment; the annotation re-pins as the page scrolls.

**See every comment in one place.** **All comments** (in the **View ▾** menu, or
press <kbd>a</kbd>) lists every comment across all files — line, text, file, and
region kinds — each with a jump back to its source. **Show / hide resolved** keeps
addressed comments out of the way (or hide a single resolved one to declutter
without turning the whole group off).

**Search across files.** Press <kbd>⌘</kbd><kbd>K</kbd> (or <kbd>Ctrl</kbd><kbd>K</kbd>)
to open a search palette that matches both file names and line contents across
the changed files — toggle it to search every file. Picking a result opens the
file and jumps to the line, revealing it even if it's inside a folded region.

## Sending work to your agent

prereview is built to feed a coding agent. In **agent mode** (`--agent`, which
an agent's `/prereview` command sets for you) the toolbar shows a **Queue**
button (with an **End session** button) instead of **Quit**:

1. You leave comments as you read.
2. Each one **streams to the agent** the moment you save it — the agent reads it,
   edits the file, and marks it **done**.
3. Prefer to batch? Open the **Queue** dropdown and hit **⏸ Pause** — comments
   are held until you **▶ Resume**, which releases them together.
4. Click **End session** when you're finished; the agent stops watching.

`comments.csv` is always the source of truth (RFC-4180, one row per comment), and
in agent mode prereview also emits a continuous JSON event stream the agent
consumes across many rounds — see the CLI reference below.

While the agent works, its **live status** (working / done) shows in the toolbar,
each comment it addresses gets a **done** badge, and the **Queue** dropdown tracks
what's queued, done, and awaiting the agent. **Reply** on any comment for a
two-way thread, and every batch the agent finishes is captured as a **file
version** (with a changelog) in the **Versions** panel — view, diff, or restore
it.

## Suggested edits

The loop also runs the other way: your agent can *propose* edits and you decide
on them. Ask it to review something and suggest edits (e.g. "review the doc for
ambiguity and suggest edits in prereview" — or use the file header's **Ask for
suggestions** menu of ready-made prompts); it submits them with `prereview
suggest`, and each appears inline as a **before → after** box, visually distinct
from a comment.

On each box:

- **Accept** — take the edit. The agent applies it and the box shows *applied*
  (**Revert** undoes an applied edit — the agent restores the original).
- **Reject** — drop it.

Want a different take? **Reply** on the suggestion's thread with what should
change; the agent reworks it and re-submits (a reworked suggestion comes back
for a fresh look).

Your decisions reach the agent the same way comments do — continuously, no
hand-off step. prereview never edits your files itself; the agent does, so an
accepted edit shows up as a normal diff you can commit. Use **Show / hide
suggestions** (or press <kbd>s</kbd>) to toggle the boxes while you read.

## Checks

Prose files (`.md`, `.markdown`, `.txt`, `.rst`, `.adoc`) get **checks**: rules
that flag wording, run every time the file loads. The file header shows
**Checks n/m**, how many of the enabled checks passed.

**In a file**, a bar above the text lists the checks that fail on it
("Failing: Filler intensifiers 2 · All checks"):

- **Tap a check** to see its findings: each flagged span gets a wavy underline and
  a card under its line. Only one check shows at a time, so a rule with many hits
  doesn't unfold the whole file. In the rendered Markdown or HTML view, picking a
  check shows the source while it's picked.
- The bar becomes **finding 1 of n** with **‹ ›** to step through the findings in
  this file (it wraps; it never changes file), and **×** to go back to the list.
- **Queue as comment** on a card turns the finding into an ordinary comment on
  that span, so your agent fixes it from the queue like any other.

**The Checks page** (the toolbar's **Library** button, or **Checks n/m** /
**All checks** for the open file) lists every check across every changed file:

- **Search** check names, descriptions, categories and the words a finding
  flagged. The status chips (**Failing**, **Errors**, **Passing**, **Off**), the
  source picker and the **category** list narrow it further; categories where
  every check passes fold into one line until you ask to see them.
- **Tap a check** to see its findings grouped by file, with **Show in file** and
  **Queue** on each, and its rule actions: **Edit a copy** (built-ins), **Open in
  editor** (your rules) and **Turn off** (for you, in every repo).
- A rule's category is its `category:` key; without one it groups as Built-in,
  Yours, or its Vale style (`Microsoft.Passive` → Vale · Microsoft).

prereview ships three built-in rules: weasel words, filler intensifiers, and
sentence length. Your own rules live in `~/.config/prereview/checks/` as YAML
files in [Vale's](https://vale.sh/docs/topics/styles/) rule format. prereview runs
the `existence`, `substitution` and `occurrence` types itself, plus a `vocab` list
(`accept:` terms flag the wrong case, `reject:` terms flag any use). A file named
like a built-in (`weasel-words.yml`) replaces it; **Edit a copy** on a built-in's
row puts one there for you. Edits to these files show up on the open page without
a reload, and a rule that doesn't parse shows its error on its row.

```yaml
# ~/.config/prereview/checks/house-terms.yml
extends: substitution
name: House terms
message: "Use '%s' instead of '%s'."
ignorecase: true
files: ["*.md", "docs/*.txt"]   # optional; defaults to the prose types above
category: Terminology            # optional; groups it on the Checks page
swap:
  e-mail: email
  utilize: use
```

A rule can also run a program: `extends: command` with `command: [argv…]` (and
an optional `timeout:`, default `5s`). It gets the file's path as its last
argument and the text on stdin, and prints a JSON array of
`{"line", "from_col", "to_col", "message", "severity"}` (1-based lines, 0-based
character columns). Command rules only run from your own checks folder, never
from a reviewed repo.

If a repo has a `.vale.ini`, the Checks page offers **Trust this repo's Vale
config** in its Vale category.
A Vale config can make vale start other tools (an XML transform runs
`xsltproc`), so it never runs just because you opened the review. Once you trust
the repo (remembered per repo, for you), its rules run through
[vale](https://vale.sh) when it's installed; without vale the page says the
config isn't applied.

**Open in editor** on a rule's row opens the file in VS Code, Cursor, Zed, or a
custom URL (`{path}`, `{line}`, `{host}`); pick one under **Open rule files in**
at the top of the page.
Set **SSH host** when your browser is on another device than the review, so the
link opens the file over the editor's remote-SSH support. With no editor chosen,
the row shows the file's path to copy.

`prereview check <file>…` runs the same checks from the terminal and exits 1 when
any fails.

### Judges

A **judge** is a rubric your coding agent answers: one yes/no question about a
whole document plus what passing looks like ("Does it lead with what the reader
must do?"). prereview never calls a model itself; judges need a review started with
`--agent`. Put one in your checks folder as a Markdown file:

```markdown
<!-- ~/.config/prereview/checks/reader-first.md -->
---
kind: judge
name: Written for the reader
category: Reader-first      # optional; groups it on the Checks page
files: ["*.md"]             # optional; defaults to the prose types
---
Does the text lead with what the reader must do or will notice, before how it works?
Pass when the first paragraph says what changes for the reader.
```

- **Run judges** in the file bar asks your agent to judge the open file against every
  judge that applies. Verdicts appear live: pass or fail with a critique, and
  **Show in file** when the critique quotes a passage.
- A failing judge joins the bar's list (tagged *judge*). Pick it to see its critique,
  queue it as a comment, or answer **Agree** / **Disagree**. Your answers are how a
  judge earns trust; the Checks page shows how often each one agrees with you.
- A verdict goes out of date when the file or the rubric changes; the bar then
  offers **Re-run judges**. Judges are advisory: they count in **Checks n/m** once
  they have a current verdict, but nothing is blocked on them.

**Rule history.** Every edit to a rule file in your checks folder is kept as a
version. Expand one of your checks on the Checks page to see its history, newest
first, with the lines each version changed and **Restore** on the older ones (a
restore is itself a new version, so you can undo it the same way).

## Learnings

The **Library** opens on **Learnings**: lessons your coding agent drew from your
reviews, waiting for you to accept. prereview never calls a model; the agent
proposes, and nothing takes effect until you accept. The toolbar's Library button
shows how many are waiting.

- Each card says where the lesson applies (its **scope**, e.g. "Technical docs")
  and cites the **evidence**: the comments, replies and accept/reject decisions it
  rests on, quoted, with where they were made. A lesson can't be proposed without
  evidence that exists.
- **Accept**, **Edit** (the text and scope), **Reject** or **Delete**. A
  proposal that contradicts an accepted lesson says so ("Replaces an older
  learning"); newer wins, so accepting it marks the old one **superseded**, which
  you can **Restore**. The status chips and the scope picker filter the list.
- **Learn after each review** (on by default): when your agent goes idle and the
  review holds five or more new comments, replies or decisions since it last read
  it, prereview asks the agent to read them and propose lessons. Turn it off on the
  Learnings tab.
- **Read past reviews**: the first time you run this version, prereview looks for
  past reviews under your home folder (skipping dependency, VCS and cache folders)
  and lists the ones with comments, ticked. **Read with my agent** asks your agent
  to read exactly the ticked ones; untick any you'd rather keep out. **Not now**
  hides the offer on this machine; the Learnings tab's **Read past reviews** brings
  it back. Reading needs an agent (`prereview --agent`).
- **Several reviews at once** share one library: accept a lesson in one and every
  other open review shows it within a second.
- **Another machine**: **Export learnings** downloads them as a file; **Import** on
  the other machine merges it. Newer changes win, and importing the same file twice
  adds nothing.
- **A learning can carry a check.** When your agent proposes a lesson that can be
  checked, it can draft the checker (banned words, a preferred spelling) or judge
  (a yes/no rubric) with it; the card shows it before you accept.
- **Review in prereview** (the Library header) opens a review of the library folder
  itself (`~/.config/prereview`) in a new tab, so you can comment on your rule files
  and have your agent change them, with the same version history.

## Skills

Accepted learnings become **skills your agents load**. The Library's **Skills** tab
shows them:

- Every accepted learning becomes one line of a skill prereview writes for you:
  `learned-writing`, or a skill of its own for a scope (when five accepted learnings
  share one, the tab offers **Create skill**). The checkers and judges learnings
  carry go into your checks folder as `learned-…` checks.
- The skill is **installed for every agent set up on this machine**:
  `~/.claude/skills` (Claude Code), `~/.agents/skills` (Codex, Gemini CLI, opencode),
  `~/.codex/skills`, and `~/.cursor/rules` (as a Cursor rule). An agent whose folder
  doesn't exist is skipped — prereview never creates it. Each card says where the
  skill is installed, or why not.
- **Edit the learnings, not the skill.** If you do edit a skill's `SKILL.md`,
  prereview stops regenerating it (its installs follow your edit) until you choose
  **Regenerate from learnings**. Every regeneration is a version: the card's history
  shows what each learning changed, with **Restore**.
- **Your own skills** found in those folders can be **imported by reference**:
  prereview reads them and never writes into their folders. Graders in a skill's
  `evals/` that can check a document become checks (an `llm` grader becomes a
  judge, a `regex` with `match: not_contains` a checker); the rest, which grade an
  agent's run, are listed as not used. Learnings whose scope is the skill's name
  form its **overlay**: **Export patch** gives you a diff that adds them to the
  skill, for you to apply.
- **Learnings imported from another machine** arrive proposed: a file never
  changes your skills or checks until you accept it here.

## Themes & modes

The toolbar's palette button cycles the **color scheme** (Solarized, Gruvbox,
Catppuccin) and the mode button cycles **Light / Dark / System**. Both the code
syntax colors and the rest of the UI recolor instantly, with no reload. System
mode follows your OS setting.

For everything you can do from the keyboard — navigating files, moving the
selection, opening the composer — press <kbd>?</kbd> to open the keyboard
shortcuts.

## CLI reference

The review target is the trailing **positional path** (default: current
directory), so a bare `prereview` just works. **Flags must come before the
path** — `prereview --agent ./docs`, not `prereview ./docs --agent`.

```bash
prereview                                # current dir (git repo or not)
prereview ./PLAN.md                      # a single file
prereview ./design-docs                  # a non-git directory — every file shown whole
prereview --base origin/main ../service  # a different repo, diffed against a ref
prereview --agent                        # agent mode: stream the queue for an LLM
prereview --agent --replace              # take over an already-running session
prereview --external http://localhost:5173 --out ./review   # annotate a live local site
```

The mode is auto-detected from the path: a git repo shows real `git diff` hunks
against `--base`; a non-git directory or single file is shown whole (every line
commentable), with no diff and no base picker. Each single file gets its own
store, so reviews of two files in one directory run independently.

| Flag | Default | Notes |
|---|---|---|
| `--base <ref>` | `HEAD` | Git ref to diff against (`HEAD~1`, `main`, a tag, a SHA). Git mode only. |
| `--port <n>` | `0` | TCP port; `0` = a random free port. |
| `--host <ip>` | `127.0.0.1` | Bind address. On a remote box with Tailscale, auto-binds the tailnet IP so a phone can reach it — never public. Avoid `0.0.0.0`. |
| `--out <dir>` | the review path | Directory to create the store under (its `.prereview/`). Required with `--external`. The resolved store is printed as the `STORE` line. |
| `--agent` | off | Run under a coding agent: stream the review queue as JSON events and show the **Queue** (Pause/Resume) + **End session** UI instead of **Quit**. (`--skill`/`--stream` are deprecated aliases.) |
| `--replace` | off | Stop an already-running server for this store and take over. |
| `--external <url>` | — | Annotate a live local site instead of files. Requires `--out`; ignores the path and `--base`. |

Run-and-exit actions (they don't start the server): `--version`,
`--install-skill` / `--client`, `--update`, `--uninstall`, `--no-update`.

The agent side uses subcommands you rarely run by hand: `prereview watch` (read
the event stream), `prereview comments --json` (list actionable comments),
`prereview done` (mark comments done), `prereview status`, `prereview suggest`
(propose edits), `prereview reply` (thread reply), and `prereview applied` /
`reverted`. See
[`docs/cli.md`](https://github.com/livetemplate/prereview/blob/main/docs/cli.md).

> [!TIP]
> The full reference — every flag, mode, and combination — lives in
> [`docs/cli.md`](https://github.com/livetemplate/prereview/blob/main/docs/cli.md).
> Driving prereview from a specific agent is covered in
> [`docs/integrations.md`](https://github.com/livetemplate/prereview/blob/main/docs/integrations.md).
