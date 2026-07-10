package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/livetemplate/prereview/internal/review"
)

// runSuggest implements `prereview suggest [--out <dir>] [--file <f>]`: the coding
// agent (via the skill) submits proposed edits so the live review UI renders them
// as inline suggestion boxes the reviewer can accept / reject / revise (issue #98).
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
// --out is the directory whose .prereview/ holds the review — the REPO path
// prereview prints at launch — mirroring the processed subcommand.
func runSuggest(args []string) error {
	fs := flag.NewFlagSet("suggest", flag.ContinueOnError)
	out := fs.String("out", "", "directory whose .prereview/ holds the review (the REPO printed at launch); defaults to the current directory")
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
				"  --out must match the review's directory (the REPO line).\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	var raw []byte
	var err error
	if *file != "" {
		raw, err = os.ReadFile(*file)
		if err != nil {
			return fmt.Errorf("read %s: %w", *file, err)
		}
	} else {
		raw, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	}

	sugs, err := parseSuggestionsInput(raw)
	if err != nil {
		return err
	}
	if len(sugs) == 0 {
		fs.Usage()
		return fmt.Errorf("no suggestions in payload")
	}

	dir, err := storeDir(*out)
	if err != nil {
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
	return nil
}

// parseSuggestionsInput decodes the agent's payload — a JSON array, a single
// object, or JSONL (whitespace/newline-separated objects) — into normalized
// Suggestions. Each is validated (file + a line range are required) and given a
// stable id when the agent omitted one, so it round-trips through the loader.
func parseSuggestionsInput(raw []byte) ([]review.Suggestion, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty payload")
	}

	var parsed []review.Suggestion
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &parsed); err != nil {
			return nil, fmt.Errorf("parse JSON array: %w", err)
		}
	} else {
		// A stream of one-or-more concatenated / newline-delimited objects. The
		// decoder consumes them one at a time, covering both a single object and
		// JSONL without a separate code path.
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		for {
			var s review.Suggestion
			if derr := dec.Decode(&s); derr == io.EOF {
				break
			} else if derr != nil {
				return nil, fmt.Errorf("parse JSON object: %w", derr)
			}
			parsed = append(parsed, s)
		}
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
