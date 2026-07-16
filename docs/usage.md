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
commentable), with no diff and no base picker.

| Flag | Default | Notes |
|---|---|---|
| `--base <ref>` | `HEAD` | Git ref to diff against (`HEAD~1`, `main`, a tag, a SHA). Git mode only. |
| `--port <n>` | `0` | TCP port; `0` = a random free port. |
| `--host <ip>` | `127.0.0.1` | Bind address. On a remote box with Tailscale, auto-binds the tailnet IP so a phone can reach it — never public. Avoid `0.0.0.0`. |
| `--out <dir>` | the review path | Directory whose `.prereview/` holds the store. Required with `--external`. |
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
