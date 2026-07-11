package main

import "github.com/livetemplate/prereview/internal/review"

// runReverted implements `prereview reverted [--out <dir>] [--file <f>|-]
// <suggestion-id>...`: the coding agent calls it AFTER it has RESTORED an applied
// suggestion's original text to disk (#159 M4.2), undoing an accept the reviewer
// asked to revert (the snapshot delivered it as verdict="revert"). Appends one JSON
// line per id to <store>/.prereview/reverted.jsonl — the mirror of applied.jsonl.
// The review UI nets the two counts: once reverted catches up to applied, the
// suggestion is no longer "applied" and drops back to undecided. Idempotent per id,
// same as `applied` (shares runMarkAck).
func runReverted(args []string) error {
	return runMarkAck("reverted", revertedUsage, review.RevertedFileName, "reverted", args)
}

const revertedUsage = "Usage: prereview reverted [--out <dir>] [--file <f>|-] <suggestion-id>...\n\n" +
	"  Acknowledge that you REVERTED an applied suggestion — restored its original\n" +
	"  text to the file after the reviewer asked to undo the accept (the snapshot\n" +
	"  delivered it as verdict=revert). The review UI then drops it back to undecided.\n" +
	"  Run by the coding agent after restoring the text; each id is validated against\n" +
	"  the review's suggestions.\n"
