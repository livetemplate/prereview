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
3. Echo your status so the review UI shows you're working (see below), then
   apply the comments as ONE coherent change, not row by row: read them all
   first, look for shared themes and conflicts, then edit. Use the `file` column
   plus `from_line`/`to_line` to locate each anchor and the `body` column for
   intent. Interpret `kind`: `line` is a line-range edit; `file` is whole-file
   guidance; `area` points at a rectangle on an image (`area` column holds
   `{x,y,w,h}` fractions); `region` points at a spot on a live page (`url`
   column), which is feedback, not a file edit.
4. Mark yourself done (below) and report what you changed. The user may comment
   more and hand off again, or click "Quit" to finish; re-read `comments.csv` on
   each hand-off and dedupe by the `id` column.

This is a one-shot-per-batch flow: prereview writes the comments, you read them
when the user hands off. Do not try to background a process and block-read a
stream — read the CSV once per hand-off instead.

Status echo: while you apply a batch, write `<REPO>/.prereview/llm-status.json`
so the running review UI shows what you're doing, live, in every open tab. Write
it atomically (temp file + `mv`), `working` when you start applying and `done`
when finished:

    prereview_status() {  # usage: prereview_status <working|done> [short message]
      dir="<REPO>/.prereview"
      printf '{"state":"%s","message":"%s","updated_at":"%s"}\n' \
        "$1" "${2:-}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        > "$dir/.llm-status.tmp" && mv "$dir/.llm-status.tmp" "$dir/llm-status.json"
    }

Keep the message short and plain (no quotes or newlines), e.g. `"Applying your
review"` — do NOT put a comment count in it (the user can hand off more while you
work, so a number goes stale), or omit the message. The file resets on each fresh
launch, so you never inherit a stale status.
