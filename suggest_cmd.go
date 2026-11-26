package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
)

// runSuggest implements `prereview suggest [--out <dir>] [--file <f>]`: the coding
// agent (via the skill) submits proposed edits so the live review UI renders them
// as inline suggestion boxes the reviewer can accept / reject (issue #98).
//
// It reads a JSON payload from --file (or stdin) — a single object, a JSON array,
// or newline-delimited objects (JSONL) — and APPENDS one normalized JSON line per
// suggestion to <store>/.prereview/suggestions.jsonl. Like the processed
// subcommand it is a pure append (never rewrites), so it never races the server's
// comments.csv writes and the file is durable across relaunches. Each suggestion
// carries a stable `id`; re-submitting the same id revises that suggestion (the
// server's loader keeps the last write per id), so the agent can update a proposal
// without piling up duplicates.
//
// --out is this review's store — the STORE path
// prereview prints at launch — mirroring the processed subcommand.
func runSuggest(args []string) error {
	fs := flag.NewFlagSet("suggest", flag.ContinueOnError)
	out := fs.String("out", "", "this review's store: the STORE path printed at launch (the reviewed file, or a directory whose .prereview/ holds the review, also work); defaults to the current directory")
	file := fs.String("file", "", "read the JSON payload from this file instead of stdin")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview suggest [--out <dir>] [--file <payload.json>]\n\n"+
				"  Submit LLM-proposed edits so the live review UI renders them as inline\n"+
				"  suggestion boxes. Reads a JSON payload from --file or stdin: a single\n"+
				"  object, a JSON array, or newline-delimited objects (JSONL). Each object:\n\n"+
				"    {\n"+
				"      \"id\":       \"stable-id\",      // optional; re-use to revise a suggestion\n"+
				"      \"file\":     \"docs/readme.md\", // required, repo-relative\n"+
				"      \"from_line\": 12,               // required, 1-based (new side)\n"+
				"      \"to_line\":   12,               // required\n"+
				"      \"original\": \"the exact current text\",\n"+
				"      \"proposed\": \"the replacement text\",\n"+
				"      \"note\":     \"why (optional)\"\n"+
				"    }\n\n"+
				"  --out must match the review's store (the STORE line).\n")
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}

	raw, err := readPayload(*file)
	if err != nil {
		return err
	}
	sugs, err := parseSuggestionsInput(raw)
	if err != nil {
		return err
	}
	if len(sugs) == 0 {
		fs.Usage()
		return fmt.Errorf("no suggestions in payload")
	}
	for _, w := range misgroupWarnings(sugs) {
		fmt.Fprintln(os.Stderr, "prereview suggest: warning:", w)
	}

	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	if err := checkFor(sugs, filepath.Join(dir, "comments.csv")); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, review.SuggestionFileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	// json.Encoder writes one object + newline per call → JSONL, matching the
	// append-only line format loadSuggestions reads.
	enc := json.NewEncoder(f)
	for i := range sugs {
		if err := enc.Encode(sugs[i]); err != nil {
			return fmt.Errorf("append suggestion %s: %w", sugs[i].ID, err)
		}
	}
	fmt.Printf("submitted %d suggestion(s)\n", len(sugs))
	checkAgentText(os.Stderr, library.SuggestionText, suggestionPieces(sugs))
	return nil
}

// parseSuggestionsInput decodes the agent's payload — a JSON array, a single
// object, or JSONL (whitespace/newline-separated objects) — into normalized
// Suggestions. Each is validated (file + a line range are required) and given a
// stable id when the agent omitted one, so it round-trips through the loader.
func parseSuggestionsInput(raw []byte) ([]review.Suggestion, error) {
	parsed, err := decodePayload[review.Suggestion](raw)
	if err != nil {
		return nil, err
	}

	out := make([]review.Suggestion, 0, len(parsed))
	for i := range parsed {
		s := parsed[i]
		if s.File == "" {
			return nil, fmt.Errorf("suggestion %d: missing \"file\"", i+1)
		}
		if s.FromLine < 1 {
			return nil, fmt.Errorf("suggestion %d (%s): \"from_line\" must be >= 1", i+1, s.File)
		}
		if s.ToLine < s.FromLine {
			s.ToLine = s.FromLine
		}
		if s.Side == "" {
			s.Side = "new"
		}
		if s.ID == "" {
			s.ID = review.NewSuggestionID()
		}
		out = append(out, s)
	}
	return out, nil
}

// checkFor refuses a payload whose "for" names a comment the review doesn't have
// (#204): the group would hang off nothing, so nothing is appended.
func checkFor(sugs []review.Suggestion, csvPath string) error {
	fors := make([]string, len(sugs))
	for i, s := range sugs {
		fors[i] = s.For
	}
	return checkForIDs(csvPath, fors)
}

// checkForIDs refuses any non-empty "for" that names no comment in the review.
// Shared by `suggest` and `ask`.
func checkForIDs(csvPath string, fors []string) error {
	var ids []string
	for _, f := range fors {
		if f != "" {
			ids = append(ids, f)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if err := validateIDs(csvPath, ids); err != nil {
		return fmt.Errorf("\"for\": %w", err)
	}
	return nil
}

// misgroupWarnings words review.MisgroupedAtStart's pairs for stderr. suggest is a
// tolerant append, so a misgroup is a warning, never an error (#206).
func misgroupWarnings(sugs []review.Suggestion) []string {
	var out []string
	for _, p := range review.MisgroupedAtStart(sugs) {
		out = append(out, fmt.Sprintf(
			"%s and %s both start at %s:%d but are not alternatives (different \"original\", or different to_line for an insertion): accepting one won't reject the other. To offer a choice, quote the same span in both.",
			p[0].ID, p[1].ID, p[1].File, p[1].FromLine))
	}
	return out
}
