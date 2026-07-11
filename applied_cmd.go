package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/livetemplate/prereview/internal/review"
)

// runApplied implements `prereview applied [--out <dir>] [--file <f>|-]
// <suggestion-id>...`: the coding agent calls it AFTER it has written an ACCEPTED
// suggestion's edit to disk (#159), so the review UI flips that suggestion from
// "accepted (pending apply)" to "applied" and drops it from the agent's actionable
// snapshot. Appends one JSON line per id to <store>/.prereview/applied.jsonl —
// append-only, agent-owned, mirroring done's processed.jsonl. Idempotent: re-acking an
// id is harmless (the set already holds it), which matters because two snapshots can
// carry the same `verdict:accept` before the ack lands. Ids come from a snapshot's
// `suggestions[]` (the ones with `verdict:accept`).
func runApplied(args []string) error {
	fs := flag.NewFlagSet("applied", flag.ContinueOnError)
	out := fs.String("out", "", "directory whose .prereview/ holds the review (the REPO printed at launch); defaults to the current directory")
	file := fs.String("file", "", "read suggestion ids from this file, or \"-\" for stdin (bare ids, a JSON array, or JSONL objects with an \"id\")")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview applied [--out <dir>] [--file <f>|-] <suggestion-id>...\n\n"+
				"  Acknowledge that you APPLIED an accepted suggestion's edit to the file, so\n"+
				"  the review UI marks it \"applied\" and stops showing it as work. Run by the\n"+
				"  coding agent after writing the edit. Ids come from a snapshot's suggestions[]\n"+
				"  (verdict=accept); each is validated against the review's suggestions.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	ids := fs.Args()
	if *file != "" {
		raw, err := readIDSource(*file)
		if err != nil {
			return err
		}
		fileIDs, err := parseIDsInput(raw)
		if err != nil {
			return err
		}
		ids = append(ids, fileIDs...)
	}
	if len(ids) == 0 {
		fs.Usage()
		return fmt.Errorf("no suggestion id given")
	}
	ids = dedupe(ids)

	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	csvPath := filepath.Join(dir, review.CommentsFileName)
	if err := validateAppliedTargets(csvPath, ids); err != nil {
		return err
	}
	if err := appendMarks(dir, review.AppliedFileName, ids); err != nil {
		return err
	}
	fmt.Printf("marked %d suggestion(s) as applied\n", len(ids))
	return nil
}

// validateAppliedTargets rejects any id not present in the review's suggestions —
// an applied ack targets a suggestion, so a typo'd id fails loudly instead of
// recording a mark that flips nothing. (Distinct from done's validateIDs, which
// validates against comments.csv; applied is suggestion-only.) A missing/empty
// suggestions set means there is nothing to validate against — almost always a
// wrong --out — so it is an error, not a silent pass.
func validateAppliedTargets(csvPath string, ids []string) error {
	valid := map[string]bool{}
	for _, s := range review.LoadSuggestions(csvPath) {
		valid[s.ID] = true
	}
	if len(valid) == 0 {
		return fmt.Errorf("no suggestions found at %s — is --out the review's directory?", filepath.Dir(csvPath))
	}
	var unknown []string
	for _, id := range ids {
		if !valid[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown suggestion id(s): %s (accepted suggestion ids come from a snapshot's suggestions[])", strings.Join(unknown, ", "))
	}
	return nil
}
