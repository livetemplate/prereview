# Using prereview

**prereview** is a local review tool. You read a diff, or any files, Markdown,
images or a live local site, in the browser and leave comments, and your coding
agent acts on them. Everything is local — comments live in a plain
`.prereview/comments.csv` next to what you're reviewing. No account, no cloud.

This page is a quick tour of the app. Press <kbd>?</kbd> anytime for the full
list of keyboard shortcuts.

Your own prompts, checks, shortcuts and learnings live in one library folder:
`~/.config/prereview` on Linux and `~/Library/Application Support/prereview` on
macOS (`PREREVIEW_LIBRARY_DIR` moves it). The paths below use the Linux form.

## Reviewing

**Comment on a line.** Tap a line to anchor a comment; tap another line to
extend the range, or tap the first again to reseat it. Type, then **Save**. The
gutter numbers are permalinks — the URL hash tracks your selection, so you can
copy the link to reopen or share exactly what you were looking at.

**Comment on part of a line.** Select a word, phrase or multi-line span by
dragging with a mouse, long-pressing on a phone, or with the keyboard caret, and a
floating **Comment** button appears. The comment anchors to exactly those
characters, and the span stays highlighted when you come back to it.

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
prereview proxies a running dev server, so you can drag a box on any part of the
live page and comment. Add a directory (`--external <url> .`) to review its files in
the same session: the **Live** toggle in the header (or <kbd>l</kbd>) switches
between them, and both kinds of comment share one queue. The annotation re-pins as the page scrolls. Once the agent has
worked on or answered a box, it turns dashed with a ✓ until you resolve it, and the
**Annotations** panel shows its reply, with a **Reply** button to push back. Boxes on
an image comment work the same way.

**See every comment in one place.** **All comments** (in the **View ▾** menu, or
press <kbd>a</kbd>) lists every comment across all files, of every kind, each with
a jump back to its source. **Show / hide resolved** keeps
addressed comments out of the way (or hide a single resolved one to declutter
without turning the whole group off). **Resolve all** resolves every listed
comment that's still open, and its notice offers **Undo** until you close the
list.

**Comments from a remote copy.** A reviewed file may be published somewhere people
comment: a GitHub pull request, Google Docs, Notion, a wiki. Your agent then offers
to bring those comments into the review with `prereview import`, and does when you
ask. Each shows where it came from and who wrote it, linked to
the original, on the text it was left on (or on the whole file when that text has
changed). Replies there show in its thread under their authors' names, and resolving
it there resolves it here. They're queued for your agent like your own comments, but
never used to learn your preferences.

**One-tap comments.** The comment box on a line, a text selection or a block has a
row of chips. Each is an instruction you give often: *Drop this*, *Why is this here?*,
*What does this mean?*, *Make this shorter* and more. A tap adds the chip's text after whatever
you've typed, and you edit it before saving. The chips you tap most come first. Add
your own as Markdown files in `~/.config/prereview/shortcuts/` (the format is under
`PREREVIEW_SHORTCUTS_DIR` in
[`docs/cli.md`](https://github.com/livetemplate/prereview/blob/main/docs/cli.md#environment)).

**Step through what's open.** While anything is waiting on you (an unresolved
comment, an undecided suggestion, an unanswered quiz question), a bar at the top
of the file reads "N open". **‹** and **›** (or <kbd>p</kbd> and <kbd>n</kbd>)
step through those items in document order across files, opening each one's file
and scrolling it into view. The chip next to the count limits the steps to
comments, suggestions or quiz questions, and **×** hides the bar for the session.

**Search across files.** Press <kbd>⌘</kbd><kbd>K</kbd> (or <kbd>Ctrl</kbd><kbd>K</kbd>)
to open a search palette. It matches file names and line contents across the
changed files, and a toggle searches every file. Picking a result opens the
file and jumps to the line, revealing it even if it's inside a folded region.

## Sending work to your agent

prereview is built to feed an agent. In **agent mode** (`--agent`, which
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

**Is the agent there?** The dot on the **Queue** button answers it. A solid dot
means an agent is watching your queue; a hollow one means nothing has picked it
up yet. Open the dropdown for the detail, such as "Agent connected — watching
your queue" or, when an agent has gone quiet, a hint to ask it to run
`prereview watch` again. While it works, its status (working / done) shows there
too.

**Where each comment stands.** The Queue dropdown and each card show it:

- **queued**: sent, not yet picked up;
- **worked on** and then **done**: the agent edited the file for it;
- **answered**: the agent replied without an edit (a question, or a choice for you
  to make). Read the reply, then resolve it or reply back;
- **likely addressed**: the agent edited exactly the lines the comment is on.
  Check the change and resolve it;
- **blocked**: the comment's text is gone from the file, so it needs re-anchoring.

**Reply** on any comment for a two-way thread; a reply you send goes back to the
agent. When the reviewed files change on disk, prereview says so at the top of the
file and offers **Refresh diff** once the edits settle. The agent is told which of
your files changed under it.

## Versions

Every batch the agent finishes is saved as a **version** of each file it changed,
with the agent's own changelog. Open **Versions** in the file header to read the
list, **View** or **Diff** a version against the current file, or **Restore** it.
Versions work the same in git repos, plain folders and single files, and nothing is
committed.

## Suggested edits

The loop also runs the other way: your agent can *propose* edits and you decide
on them. Ask it to review something and suggest edits, for example "review the doc
for ambiguity and suggest edits in prereview", or use the file header's **Ask for
suggestions** menu of ready-made prompts. It submits them with `prereview
suggest`, and each appears inline as a **before → after** box, visually distinct
from a comment.

To add your own prompts to that menu, put one Markdown file per prompt in
`~/.config/prereview/prompts/`. The file format is under `PREREVIEW_PROMPTS_DIR`
in [`docs/cli.md`](https://github.com/livetemplate/prereview/blob/main/docs/cli.md#environment).

On each box:

- **Accept** — take the edit. The agent applies it and the box shows *applied*
  (**Revert** undoes an applied edit — the agent restores the original).
- **Reject** — drop it.

Want a different take? **Reply** on the suggestion's thread with what should
change; the agent reworks it and re-submits (a reworked suggestion comes back
for a fresh look).

**Follow-on edits.** Some changes reach past their own lines: drop a paragraph and a
summary or a cross-reference elsewhere goes stale. The agent proposes the change and each
follow-on edit as suggestions linked to your comment, which shows "Follow-on edits: N
open" with **Accept all** and **Reject all**. Each linked box has a *for your comment*
link back. Accept all leaves any set of alternatives for you to pick. The *Drop this*
shortcut asks for exactly this.

**Alternatives.** When a comment leaves a choice that is yours to make (tone, audience,
scope), the agent may offer two versions as linked boxes ("alternative 1 of 2")
instead of picking one. It marks your comment *answered*. Accepting one rejects the other. Reject
both and the agent asks which direction you want; reply on the comment and it works from
your reply instead. The Queue panel and the End-session dialog count the choices still
awaiting your pick.

Your decisions reach the agent the same way comments do — continuously, no
hand-off step. prereview never edits your files itself; the agent does, so an
accepted edit shows up as a normal diff you can commit. Use **Show / hide
suggestions** (or press <kbd>s</kbd>) to toggle the boxes while you read.
**Hide** on one box sets just that one aside: it leaves "1 hidden suggestion ·
Show" on its line, and **Show** brings it back. The suggestion stays open for the
agent either way.

## Understand

**Understand** in the file header (or <kbd>z</kbd>) opens a page for getting to
grips with a change before you accept it. It has a walkthrough of the whole change,
its flow diagrams when the agent drew any, and a quiz on the change.

**Walkthrough.** **Explain this change** asks your agent to walk you through the
change in the order it makes sense to read: usually the data first, then the logic,
its callers and the tests. Each step says what changed and why, and links to the
lines it cites. prereview checks every cited line against the diff, in every file,
and marks one it can't find with ⚠. When the files change after the walkthrough was
written, the page says so, and **Explain again** asks for a fresh one.

**Start the walkthrough** (or **Walk from here** on a step) opens the step's file at
the cited line. The step bar on top shows the step and why it matters; **‹** and
**›** move to the next cited place, file to file, and **×** puts the bar away. Each
step's title also marks its first line in the diff. **A–Z · sort in walkthrough
order** in the file drawer lists the files in the order the walkthrough reads them.

**Not explained.** prereview lists every changed hunk no step cites as the last
step, so walking to the end shows you what the walkthrough left out. The page says
how many there are.

**Concerns, risks and impact.** The agent can group the steps by what the change
does, each group under its own heading. A group it marks mechanical, such as a
rename, folds in the diff behind **Show N mechanical lines**: tap to read them, or
turn **Fold mechanical changes** off in the View menu. A change any other step
cites, or no step cites, never folds. Steps can carry risk chips (concurrency,
security, …) and an **Affects** list of code outside the diff the step touches,
such as a caller or a test; prereview checks those lines exist.

**Diagrams.** When the walkthrough includes flow diagrams, a Diagrams tab draws
them, each with links to the lines it cites.

**Quiz.** The Quiz tab asks your agent for a short multiple-choice quiz on the
whole change, listed by file. Each question also appears on the lines it asks about
(a question about a whole file sits at its top), and the open-items bar steps
through them file by file. Choosing an answer shows whether it's right and why. Every question cites a line range that prereview checks, so a question about
code that isn't there is marked instead of shown as fact.

To add your own walkthrough or quiz prompts, put Markdown files in
`~/.config/prereview/explain/` or `~/.config/prereview/quizzes/` (see
`PREREVIEW_EXPLAIN_DIR` and `PREREVIEW_QUIZZES_DIR` in
[`docs/cli.md`](https://github.com/livetemplate/prereview/blob/main/docs/cli.md#environment)).

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
  that span, so your agent fixes it from the queue like any other. A queued
  finding then shows only as its comment.
- **Ignore** says it's fine here: the finding stops counting (a check with nothing
  left passes) and stays dismissed while that line is unchanged. The Checks page
  shows "1 ignored · show again" to bring it back.
- **Hide** folds the card for now so you can read; the underline and the count
  stay, and "1 hidden · show" on the bar brings it back.

**The Checks page** (the toolbar's **Library** button, or **Checks n/m** /
**All checks** for the open file) lists every check across every changed file:

- **Search** check names, descriptions, categories and the words a finding
  flagged. The status chips (**Failing**, **Errors**, **Passing**, **Off**), the
  source picker and the **category** list narrow it further; categories where
  every check passes fold into one line until you ask to see them.
- **Tap a check** to see its findings grouped by file, with **Show in file** and
  **Queue** on each. Its rule actions are **Edit a copy** (built-ins), **Open in
  editor** (your rules) and **Turn off** (for you, in every repo).
- **Queue all** and **Ignore all** at the top of an open check act on every
  failure it counts, in every file, including those past the list's limit. The
  same pair above the list acts on every check listed. A search and the filters
  narrow both, as they narrow the list. Queue all skips what's already queued and
  a judge's failure with no passage to point at.
- **Ignore all** is a toggle: once nothing in its scope is left open, it reads
  **Ignored N**, and pressing it brings them all back.
- **Ignore** on a judge's verdict marks it *ignored*, and pressing **Ignored**
  brings it back. An ignored verdict stops counting as a failure until the file
  or the judge's rubric changes, so running judges again on the same text
  doesn't bring it back. It doesn't count as Disagree.
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

A **judge** is a rubric your agent answers: one yes/no question about a
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
- The request shows on the file as one line, such as "Asked your agent to run this
  file's judges". **Read more** opens the prompt your agent received. A benchmark
  run, a quiz and **Explain this change** show their requests the same way.
- A failing judge joins the bar's list (tagged *judge*). Pick it to see its critique,
  queue it as a comment, or answer **Agree** / **Disagree**. Your answers are how a
  judge earns trust; the Checks page shows how often each one agrees with you.
- A verdict goes out of date when the file or the rubric changes; the bar then
  offers **Re-run judges**.
- **A judge starts advisory**: it shows its verdict and critique (tagged *judge ·
  advisory*) but doesn't count in **Checks n/m** or turn the button red. It earns its
  place once it agrees with you often enough: at least 10 of your answers for its
  current rubric, with 8 of the last 10 agreeing. Your answers count across every
  review on this machine. Edit the rubric and it starts over (a changed rubric is a
  different judge). The Checks page shows each judge's standing and flags one that
  is *flaky* (the same text got both verdicts) or *doesn't discriminate* (every
  verdict you answered was the same). It also lists the verdicts you disagreed
  with, with the critique, so you can fix the rubric. Nothing is ever blocked on a judge.

**Benchmarks** (Library → Benchmarks) tell you whether a change to a check, a
rubric or your agent's model made things better or worse:

- **New benchmark** from the open file makes it the first case: a snapshot, so later
  edits to the file don't move the benchmark. It's scored by every check that
  applies to the file; change its checks and weights in its `benchmark.yml` (Open in
  editor). **Add the open file as a case** adds another snapshot.
- **Run benchmark** runs the checkers at once and asks your agent for the judges in
  one request (it needs `prereview --agent`). The score is the weighted pass rate;
  an errored check counts as missing, not as a fail, and is shown with **!**.
- The score shows the mean ± spread of the last three comparable runs. Every run
  pins each check's version, each case's text and the judging model; runs that
  differ are marked *not comparable* in the runs table and kept out of the score.

**Rule history.** Every edit to a rule file in your checks folder is kept as a
version. Expand one of your checks on the Checks page to see its history, newest
first, with the lines each version changed and **Restore** on the older ones. A
restore is itself a new version, so you can undo it the same way.

## Learnings

The **Library** opens on **Learnings**: lessons your agent drew from your
reviews. prereview never calls a model; the agent proposes them. With **Accept
automatically** on (below) they're in your skills as they arrive, and Learnings
opens on **Accepted**, where they are ("added for you"). When something waits for
your Accept (an import, or everything with the switch off), it opens on
**Proposed** instead, and the toolbar's Library button counts it.

- Each card says where the lesson applies (its **scope**, e.g. "Technical docs")
  and cites the **evidence**: the comments, replies and accept/reject decisions it
  rests on, quoted, with where they were made. A lesson can't be proposed without
  evidence that exists, and only what you said or decided counts. The agent's
  replies, suggestions and quiz questions are context: they show what you reacted
  to, but a lesson can't rest on them.
- **Accept**, **Edit** (the text and scope), **Reject** or **Delete**. A
  proposal that contradicts an accepted lesson says so ("Replaces an older
  learning"); newer wins, so accepting it marks the old one **superseded**, which
  you can **Restore**. The status chips and the scope picker filter the list.
- **Shortcuts**: when you give the same instruction in three or more comments, the
  agent can propose a lesson that adds it to your comment box as a chip ("Shortcut
  it adds" on the card). Accepting it adds the chip at once. The **Shortcuts** tab
  lists every chip (built-in, from your folder, or learned) with how often you've
  used it, and **Hide** takes one out of the comment box without deleting it.
- **Learn after each review** (on by default): when your agent goes idle with five
  or more new comments, replies or decisions in the review, prereview hands them
  to your agent's **background learner**. That's a helper agent it starts with the
  review. The request never lands on a file and doesn't
  interrupt the agent doing your edits. While it reads, the Learnings tab says
  so; if no learner is running, the toolbar's Library button shows a warning dot.
  Turn it off on the Learnings tab.
- **Accept automatically** (on by default): what the learner finds goes straight
  into your skills. A notice in every open review says so ("2 learnings added",
  or the learning itself) and stays until you close it. **Review** opens them;
  **Undo** rejects them, so they leave your skills and the learner won't propose
  them again. Turn it off on the Learnings tab and each waits for your Accept.
  Learnings you import always wait.
- **Read past reviews**: the first time you run this version, prereview looks for
  past reviews under your home folder (skipping dependency, VCS and cache folders)
  and lists the ones with comments, ticked. **Read with my agent** has the
  background learner read exactly the ticked ones; untick any you'd rather keep out. **Not now**
  hides the offer on this machine; the Learnings tab's **Read past reviews** brings
  it back. Reading needs an agent (`prereview --agent`).
- **Several reviews at once** share one library: accept a lesson in one and every
  other open review shows it within a second.
- **Another machine**: **Export learnings** downloads them as a file; **Import** on
  the other machine merges it. Newer changes win, and importing the same file twice
  adds nothing.
- **A learning can carry a check.** When your agent proposes a lesson that can be
  checked, it can draft the checker (banned words, a preferred spelling) or judge
  (a yes/no rubric) with it. The card shows it before you accept.
- **Review in prereview** (the Library header) opens a review of the library
  folder itself (`~/.config/prereview`) in a new tab. You can comment on your rule
  files there and have your agent change them, with the same version history.

## Skills

Accepted learnings become **skills your agents load**. The Library's **Skills** tab
shows them:

- Every accepted learning becomes one line of a skill prereview writes for you:
  `learned-writing`, or a skill of its own for a scope (when five accepted learnings
  share one, the tab offers **Create skill**). The checkers and judges learnings
  carry go into your checks folder as `learned-…` checks. Each runs only where its
  lesson applies: its glob (a lesson for `docs/**` checks only those files), or the
  agent's replies, suggestions or quiz questions for a lesson about those.
- The skill is **installed for every agent set up on this machine**:
  `~/.claude/skills` (Claude Code), `~/.agents/skills` (Codex, Gemini CLI, opencode),
  `~/.codex/skills`, and `~/.cursor/rules` (as a Cursor rule). An agent whose folder
  doesn't exist is skipped — prereview never creates it. Each card says where the
  skill is installed, or why not.
- **Agents are told to use them.** An agent loads a skill only when it decides the
  task matches the skill's description, so it could write a doc without yours. While
  you have a learned skill, prereview adds a short block to each agent's memory file,
  the one it reads at the start of every session: `~/.claude/CLAUDE.md`,
  `~/.codex/AGENTS.md`, `~/.gemini/GEMINI.md`, and `~/.config/opencode/AGENTS.md` if
  you already have one. The block names your skills and says to load the matching one
  before writing prose, then run `prereview check`. It sits between
  `<!-- prereview:learned-skills -->` markers; your text around it is never touched,
  and if you edit inside it, prereview leaves it alone.
- **Claude Code also checks each edit.** prereview adds one hook to
  `~/.claude/settings.json` that runs `prereview check --hook` after every Write or Edit.
  For a prose file, it runs your checks and shows Claude the findings on the lines it
  just wrote. Your other settings and hooks keep their values and order, and the
  previous file is kept as `settings.json.prereview-bak`.
- **The agent's own text learns too.** A lesson from your reply on an agent's reply,
  suggestion or quiz question gets its own skill from the first one. The skills are
  `learned-agent-replies`, `learned-suggestions` and `learned-quiz-questions`, each
  extending `learned-writing`, so they never apply to your docs. Each snapshot tells
  your agent which skills to load before writing each kind. `prereview reply`,
  `suggest` and `quiz` check what the agent sent and show it any findings, after
  posting. The built-in checks (filler words, sentence length, weasel words) don't
  run on this text, because they're tuned for documents, and a suggestion's proposed
  text is checked as part of its file.
- The tab's **Tell agents to use these skills** switch removes the block and the hook
  (and puts them back), and its pills say where each went. Both also go away when you
  have no learned skills left.
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
directory), so a bare `prereview` just works. Flags can go before or after it.

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
| `--out <dir>` | the review path | Directory to create the store under (its `.prereview/`). Required with `--external` and no path. The resolved store is printed as the `STORE` line. |
| `--agent` | off | Run under an agent: stream the review queue as JSON events and show the **Queue** (Pause/Resume) + **End session** UI instead of **Quit**. (`--skill`/`--stream` are deprecated aliases.) |
| `--replace` | off | Stop an already-running server for this store and take over. |
| `--external <url>` | — | Annotate a live local site. With a directory path, also review it in the same session; without one, requires `--out`. |

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
