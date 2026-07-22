You are applying a code review captured by the `prereview` tool. The reviewer
queues comments in a browser; you read the queue and apply the fixes.

Setup — only if a prereview server is not already running for this repo:

    prereview --agent "$(pwd)" &

The first stdout line is `READY <url>`. Give that URL to the reviewer as a
clickable Markdown link and tell them to leave comments and click **End session**
when finished. A second line, `REPO <dir>`, names the directory whose
`.prereview/` holds the data — read everything relative to it.

Read the actionable comments as JSON (no CSV parsing) whenever the reviewer says
they have commented, or has clicked End session:

    prereview comments --out <REPO> --json

That is the complete set of still-actionable comments (resolved / outdated /
draft already excluded). Apply them as ONE coherent change, not row by row: read
them all first, look for shared themes and conflicts, then edit. Each entry has
`file` + `from_line`/`to_line` to locate the anchor and `body` for intent.
Interpret `kind`: `line`/`text` = an edit at those lines; `file` = whole-file
guidance; `area` = a rectangle on an image (`area` = {x,y,w,h} fractions);
`region` = a spot on a live page (`url`), which is feedback, not a file edit.

After every edit that addresses a comment, mark it done (the id is validated
against the review — an unknown id fails):

    prereview done --out <REPO> <id>

Re-run `prereview comments --json` for more (the reviewer can keep commenting);
dedupe by `id`. Stop when the reviewer clicks End session. If your harness can
block on a stream, `prereview watch --out <REPO> --since <seq>` delivers each new
batch and exits on the terminating `end` event — otherwise just re-read
`prereview comments --json`.

If a comment asks for a comprehension quiz (the reviewer clicked "Quiz me"), do
not edit files and do not suggest edits - reply with a quiz instead:

    prereview quiz --out <REPO> --file quiz.json

Write 3-5 multiple-choice questions grounded strictly in that file's diff. Each
needs `options` (2+), a 0-based `answer`, a `why` explaining it, and
`from_line`/`to_line`/`side` locating it. Tag each with a `probe`: change-type,
localization, consequence, rationale, or `decision` - the last meaning "what did
you decide that the reviewer never asked for" (an unrequested dependency, a
changed default, a skipped edge case). Include at least one `decision` question
when the diff contains such a choice, and none when it genuinely does not.

Anchor each with `kind`: "line" (default) needs a real from_line, "file" is for a
question about the change as a whole or about something absent. Never guess line
numbers - prereview flags a cited line that is not in the diff.

Then `done` the request comment and `reply` a one-line summary. The reviewer can
reply to an individual question to query or challenge it; answer with
`prereview reply "<quizID>:<questionID>" --body "..."`, and re-submit the quiz
with the same id if they are right that a question needs fixing.

Status echo: tell the review UI what you're doing so it shows a live pill across
every open tab — `working` while you apply a batch, `done` when finished:

    prereview status --out <REPO> working "Applying your review"
    prereview status --out <REPO> done

Keep the message short and plain (or omit it); do not put a comment count in it
(the queue can grow while you work). The status resets on each fresh launch.
