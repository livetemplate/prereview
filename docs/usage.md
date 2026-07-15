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

**See every comment in one place.** The **All comments** chip lists every
comment across all files — line, text, file, and region kinds — each with a jump
back to its source. **Show / hide resolved** keeps addressed comments out of the
way (or hide a single resolved one to declutter without turning the whole group
off).

**Search across files.** Press <kbd>⌘</kbd><kbd>K</kbd> (or <kbd>Ctrl</kbd><kbd>K</kbd>)
to open a search palette that matches both file names and line contents across
the changed files — toggle it to search every file. Picking a result opens the
file and jumps to the line, revealing it even if it's inside a folded region.

## Sending work to your agent

prereview is built to feed a coding agent. In **skill mode** (`--skill`, which
an agent's `/prereview` command sets for you) the primary button is
**Hand off →** instead of **Quit**:

1. You leave a batch of comments.
2. You click **Hand off →** — prereview writes `.prereview/DONE`.
3. The agent notices, reads `.prereview/comments.csv`, and applies the fixes.
4. You review the result and hand off the next batch.

`comments.csv` is always the source of truth (RFC-4180, one row per comment), so
any agent can consume it. With `--stream`, each hand-off instead emits a JSON
snapshot on a continuous event stream for a multi-round LLM loop — see the CLI
reference below.

While the agent works a batch, its **live status** (working / done) shows in the
toolbar, and each comment it addresses gets a **worked on** badge — so you can
see progress without leaving the page.

## Suggested edits

The hand-off also runs the other way: your agent can *propose* edits and you
decide on them. Ask it to review something and suggest edits (e.g. "review the
doc for ambiguity and suggest edits in prereview"); it submits them with
`prereview suggest`, and each appears inline as a **before → after** box —
visually distinct from a comment.

On each box:

- **Accept** — take the edit.
- **Reject** — drop it.

Want a different take? **Reply** on the suggestion's thread with what should
change; the agent reworks it and re-submits (a reworked suggestion comes back
for a fresh look).

Decisions are **pending until you hand off** — exactly like comments. On the
next **Hand off →** they ship to the agent, which applies the accepted edits
and drops the rejected ones. prereview never edits your files
itself; the agent does, so an accepted edit shows up as a normal diff you can
commit. Use **Show / hide suggestions** (or press <kbd>s</kbd>) to toggle the
boxes while you read.

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
path** — `prereview --skill ./docs`, not `prereview ./docs --skill`.

```bash
prereview                                # current dir (git repo or not)
prereview ./PLAN.md                      # a single file
prereview ./design-docs                  # a non-git directory — every file shown whole
prereview --base origin/main ../service  # a different repo, diffed against a ref
prereview --skill                        # LLM hand-off mode
prereview --skill --stream               # multi-round JSON event stream for an LLM
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
| `--skill` | off | Show **Hand off →** (and write `.prereview/DONE`) instead of **Quit**. |
| `--stream` | off | Emit a continuous JSON event stream for an LLM. Implies `--skill`. |
| `--external <url>` | — | Annotate a live local site instead of files. Requires `--out`; ignores the path and `--base`. |

Run-and-exit actions (they don't start the server): `--version`,
`--install-skill` / `--client`, `--update`, `--uninstall`, `--no-update`.

The agent side uses two subcommands you rarely run by hand: `prereview suggest`
(submit proposed edits) and `prereview processed` (mark comments worked-on). See
[`docs/cli.md`](https://github.com/livetemplate/prereview/blob/main/docs/cli.md).

> [!TIP]
> The full reference — every flag, mode, and combination — lives in
> [`docs/cli.md`](https://github.com/livetemplate/prereview/blob/main/docs/cli.md).
> Driving prereview from a specific agent is covered in
> [`docs/integrations.md`](https://github.com/livetemplate/prereview/blob/main/docs/integrations.md).
