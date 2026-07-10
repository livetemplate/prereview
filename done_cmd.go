package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/review"
)

// runDone implements `prereview done [--out <dir>] [--file <f>]
// [--all-open] <id>...`: the coding agent (via the skill) calls it after
// addressing a comment so the running review UI badges it "worked on". It
// APPENDS one JSON line per id to <store>/.prereview/processed.jsonl — an
// append-only, agent-owned log the server watches. Kept a pure append (never
// rewrites) so it never races the server's comments.csv writes: the server stays
// the sole writer of the CSV.
//
// Ids come from positional args, from --file (a file, or "-" for stdin: bare
// newline-delimited ids, a JSON array, or JSONL objects with an "id" — so
// `prereview comments --json | jq -r '.[].id' | prereview done --file -`
// just works), or from --all-open (mark the whole current actionable set, the
// "I addressed the batch" shortcut). Explicit ids are VALIDATED against
// comments.csv first — an unknown id fails loudly with a non-zero exit instead
// of silently recording a garbage mark (the root cause of the corruption this
// guards against).
//
// --out is the directory whose .prereview/ holds the review — the REPO path
// prereview prints at launch — so the mark lands in the same store the server
// watches (mirrors the skill's prereview_status <REPO> convention). Defaults to
// the current directory.
func runDone(args []string) error {
	fs := flag.NewFlagSet("done", flag.ContinueOnError)
	out := fs.String("out", "", "directory whose .prereview/ holds the review (the REPO printed at launch); defaults to the current directory")
	file := fs.String("file", "", "read comment ids from this file, or \"-\" for stdin (newline-delimited ids, a JSON array, or JSONL objects with an \"id\")")
	allOpen := fs.Bool("all-open", false, "mark every comment in the current actionable set (the whole batch); cannot be combined with explicit ids or --file")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview done [--out <dir>] [--file <f>|-] [--all-open] <comment-id>...\n\n"+
				"  Mark review comments as addressed so the live review UI badges them\n"+
				"  \"worked on\". Run by the coding agent after it applies a comment; --out\n"+
				"  must match the review's directory (the REPO line). Explicit ids are\n"+
				"  validated against comments.csv — unknown ids fail. Read ids from a stable\n"+
				"  interface with `prereview comments --json`.\n")
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

	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	csvPath := filepath.Join(dir, review.CommentsFileName)

	if *allOpen {
		if len(ids) > 0 {
			return fmt.Errorf("--all-open cannot be combined with explicit ids or --file")
		}
		open, err := review.LoadComments(csvPath, false)
		if err != nil {
			return fmt.Errorf("read comments: %w", err)
		}
		for _, c := range open {
			ids = append(ids, c.ID)
		}
		if len(ids) == 0 {
			fmt.Println("no open comments to mark")
			return nil
		}
	} else {
		if len(ids) == 0 {
			fs.Usage()
			return fmt.Errorf("no comment id given")
		}
		if err := validateIDs(csvPath, ids); err != nil {
			return err
		}
	}
	ids = dedupe(ids)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, review.ProcessedFileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	// json.Encoder writes one object + newline per call → JSONL, matching the
	// append-only line format loadMarkCounts reads.
	now := time.Now().UTC().Format(time.RFC3339)
	enc := json.NewEncoder(f)
	for _, id := range ids {
		if err := enc.Encode(review.ProcessedMark{ID: id, At: now}); err != nil {
			return fmt.Errorf("append mark %s: %w", id, err)
		}
	}
	fmt.Printf("marked %d comment(s) as worked on\n", len(ids))
	return nil
}

// validateIDs rejects any id not present in the review's comments.csv, so a
// typo'd or garbage id fails loudly instead of recording a mark that badges
// nothing. Any present id is accepted, including resolved/outdated ones (marking
// those is harmless). A missing/empty CSV means there is nothing to validate
// against — almost always a wrong --out — so it is an error, not a silent pass.
func validateIDs(csvPath string, ids []string) error {
	rows, err := csv.Read(csvPath)
	if err != nil {
		return fmt.Errorf("read comments: %w", err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("no comments found at %s — is --out the review's directory?", csvPath)
	}
	valid := make(map[string]bool, len(rows))
	for _, r := range rows {
		valid[r.ID] = true
	}
	var unknown []string
	for _, id := range ids {
		if !valid[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown comment id(s): %s (use `prereview comments --json` to list valid ids)", strings.Join(unknown, ", "))
	}
	return nil
}

// readIDSource reads the --file argument: "-" means stdin, anything else is a
// file path. Mirrors the suggest subcommand's stdin/file input.
func readIDSource(file string) ([]byte, error) {
	if file == "-" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return raw, nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return raw, nil
}

// parseIDsInput extracts comment ids from a --file payload, accepting the three
// shapes an agent naturally produces: bare newline-delimited ids
// (`jq -r '.[].id'`), a JSON array (of `{"id":…}` objects or plain strings, e.g.
// piping `prereview comments --json`), or JSONL objects each with an "id".
func parseIDsInput(raw []byte) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("no comment ids in input")
	}
	switch trimmed[0] {
	case '[':
		var elems []json.RawMessage
		if err := json.Unmarshal(trimmed, &elems); err != nil {
			return nil, fmt.Errorf("parse JSON array: %w", err)
		}
		out := make([]string, 0, len(elems))
		for _, e := range elems {
			id, err := idFromJSON(e)
			if err != nil {
				return nil, err
			}
			if id != "" {
				out = append(out, id)
			}
		}
		return out, nil
	case '{':
		// One or more JSON objects (JSONL), each carrying an "id".
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		var out []string
		for {
			var o struct {
				ID string `json:"id"`
			}
			if err := dec.Decode(&o); err == io.EOF {
				break
			} else if err != nil {
				return nil, fmt.Errorf("parse JSON object: %w", err)
			}
			if o.ID != "" {
				out = append(out, o.ID)
			}
		}
		return out, nil
	default:
		var out []string
		for _, line := range strings.Split(string(trimmed), "\n") {
			if s := strings.TrimSpace(line); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	}
}

// idFromJSON pulls an id from one JSON-array element: a bare string, or an
// object with an "id" field (a `prereview comments --json` entry).
func idFromJSON(e json.RawMessage) (string, error) {
	t := bytes.TrimSpace(e)
	if len(t) == 0 {
		return "", nil
	}
	if t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return "", fmt.Errorf("parse array element: %w", err)
		}
		return s, nil
	}
	var o struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(t, &o); err != nil {
		return "", fmt.Errorf("parse array element: %w", err)
	}
	return o.ID, nil
}

// dedupe removes duplicate ids, preserving first-seen order, so a repeated id
// (positional + piped, say) records one mark and the "marked N" count is honest.
func dedupe(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
