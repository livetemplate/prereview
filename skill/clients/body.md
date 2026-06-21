You are applying a code review captured by the `prereview` tool. The user marks
what is wrong in a browser; you read those comments and apply the fixes.

Setup — only if a prereview server is not already running for this repo:

    prereview --skill "$(pwd)" &

The first stdout line is `READY <url>`. Give that URL to the user as a clickable
link and tell them to review in the browser and click "Hand off ->" when each
batch is ready. (A second line, `REPO <dir>`, names the directory whose
`.prereview/` holds the data — read everything relative to it.)

When the user has handed off — the file `<REPO>/.prereview/DONE` exists, or the
user tells you they are done:

1. Read `<REPO>/.prereview/comments.csv`.
2. Take every row where `resolved` is not `true` AND `anchor_status` is not
   `outdated`. That filtered set is the complete list of still-actionable
   comments. Skip the rest.
3. Apply them as ONE coherent change, not row by row: read them all first, look
   for shared themes and conflicts, then edit. Use the `file` column plus
   `from_line`/`to_line` to locate each anchor and the `body` column for intent.
   Interpret `kind`: `line` is a line-range edit; `file` is whole-file guidance;
   `area` points at a rectangle on an image (`area` column holds `{x,y,w,h}`
   fractions); `region` points at a spot on a live page (`url` column), which is
   feedback, not a file edit.
4. Report what you changed. The user may comment more and hand off again, or
   click "Quit" to finish; re-read `comments.csv` on each hand-off and dedupe by
   the `id` column.

This is a one-shot-per-batch flow: prereview writes the comments, you read them
when the user hands off. Do not try to background a process and block-read a
stream — read the CSV once per hand-off instead.
