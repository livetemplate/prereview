# Quiz me on this diff

Generate a short comprehension quiz about this file's diff so I can check whether I
actually understood the change before I accept it.

Do **not** edit any files and do **not** propose suggestions. Answer by running
`prereview quiz` (not `prereview suggest`), then mark this comment done and reply
with a one-line summary.

Write **3–5 multiple-choice questions**, grounded strictly in this diff. Never ask
about code that is not shown — if the diff is too small to support 3 good
questions, write fewer rather than padding.

## What to ask about

Use the `probe` field to say what each question tests. Mix them; do not write five
of the same kind.

- `change-type` — what kind of change this is (adds a guard, removes dead code,
  reorders, renames…).
- `localization` — where a specific behavior actually lives in the diff.
- `consequence` — what breaks, or what changes at runtime, if this is wrong or if
  a reader misunderstands it.
- `rationale` — why the change is done this way rather than an obvious
  alternative.
- `decision` — **what you decided on your own that I never asked for.** An
  unrequested dependency, a changed default, a widened interface, a skipped edge
  case, a deviation from what was requested, a design call you made because the
  request was underspecified.

**Include at least one `decision` question whenever this diff contains a choice I
did not ask for** — that is the single most useful thing this quiz can surface,
because it is exactly what I cannot see by skimming code that looks fine. If you
genuinely made no independent calls here, write no `decision` question at all. An
honest omission is right; a manufactured "surprise" is worse than nothing.

## What makes the options good

The whole quiz is worthless if I can pick the right answer without reading the
diff. So:

- Every wrong option must be **something a competent reader might actually
  believe** — a real alternative the code could have taken, or a genuine
  misreading of what it does. No joke options, no obviously-wrong filler, no
  "none of the above".
- Wrong options should be **similar in length, shape and specificity** to the
  right one. A conspicuously longer or more detailed option gives the answer away.
- Aim for moderate difficulty: someone who read the diff carefully should get it;
  someone who skimmed should not.

## Anchoring (required)

Every question carries `from_line`/`to_line` and `side` pointing at the lines it
is about, so I can jump straight to the code. Use `"side": "new"` for added or
context lines and `"old"` for removed ones — a modified line exists on both sides,
so this is not optional.

The one exception: a `decision` question about something **absent** ("chose not to
add a test for the error path") has nothing to point at. Set `"from_line": 0` for
those. Only `decision` questions may do this — prereview rejects any other probe
without an anchor, and flags a question whose line range does not exist in the
diff.

`why` is required on every question: it is shown after I answer, and it is where
the actual teaching happens. Explain why the right answer is right *and*, when a
wrong option is tempting, why it is wrong.

## How to submit

```bash
prereview quiz --out "<REPO>" --file quiz.json
```

The payload is a single JSON object (or an array / JSONL for several):

```json
{
  "file": "internal/review/controller.go",
  "questions": [
    {
      "probe": "consequence",
      "prompt": "persist() rewrites comments.csv from state.Comments. What happens if state.Comments is filtered at load time?",
      "options": [
        "Nothing — it is a read-only cache rebuilt on each Mount",
        "The filtered rows are deleted from comments.csv on the next write",
        "The CSV header desynchronises from the row width",
        "Only draft comments are affected, since they are not persisted"
      ],
      "answer": 1,
      "why": "state.Comments is a write-back buffer: persist() replaces the whole file from it, so anything filtered out at load is erased from disk on the next save. Option 1 is the tempting one — it would be true if persist() merged rather than replaced.",
      "from_line": 211,
      "to_line": 238,
      "side": "new"
    }
  ]
}
```

Re-using a quiz `id` revises that quiz; omit it and one is minted.
