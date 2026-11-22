# Quiz me on this change

Generate a comprehension quiz about the whole change, so I can check whether I
actually understood it before accepting it.

Do **not** edit files and do **not** propose suggestions — answer by running
`prereview quiz` (not `prereview suggest`), then mark this comment done and reply
with a one-line summary.

3–7 multiple-choice questions, spread over the files where the change matters
most, grounded strictly in the diff. Leave the quiz's `file` empty and give every
question the `file` it asks about. Anchor each question to the lines it asks about
(`"kind": "line"`), or to its file as a whole (`"kind": "file"`) when it is about
something absent. Include at least one `decision` question — something you chose
that I never asked for — if the change contains one. The full schema and the
probe kinds are in the prereview skill.
