# Walk me through this change

Explain this whole change so I can review it in the order it makes sense,
not file by file in alphabetical order.

Do **not** edit files and do **not** propose suggestions. Answer by running
`prereview explain` (not `prereview suggest`), then mark this comment done and
reply with a one-line summary.

Write the steps in the order you would explain the change to a colleague:
usually the data or interface first, then the logic, then its callers, then
the tests. Each step names what changed and why, and what I should check. Cite
the lines each step is about. Cover every change worth reading; prereview flags
any changed lines no step cites. Where they help, group the steps into
`concerns` (a `mechanical` one folds), flag `risks`, list the `impact` outside
the diff, and draw `diagrams`. The payload format is in the prereview skill.
