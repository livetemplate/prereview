package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/livetemplate/prereview/internal/review"
)

// runProcessed implements `prereview processed [--out <dir>] <id>...`: the coding
// agent (via the skill) calls it after addressing a comment so the running review
// UI badges it "worked on". It APPENDS one JSON line per id to
// <store>/.prereview/processed.jsonl — an append-only, agent-owned log the server
// watches. Kept a pure append (never rewrites) so it never races the server's
// comments.csv writes: the server stays the sole writer of the CSV.
//
// --out is the directory whose .prereview/ holds the review — the REPO path
// prereview prints at launch — so the mark lands in the same store the server
// watches (mirrors the skill's prereview_status <REPO> convention). Defaults to
// the current directory.
func runProcessed(args []string) error {
	fs := flag.NewFlagSet("processed", flag.ContinueOnError)
	out := fs.String("out", "", "directory whose .prereview/ holds the review (the REPO printed at launch); defaults to the current directory")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview processed [--out <dir>] <comment-id> [<comment-id>...]\n\n"+
				"  Mark one or more review comments as addressed so the live review UI\n"+
				"  badges them \"worked on\". Run by the coding agent after it applies a\n"+
				"  handoff; --out must match the review's directory (the REPO line).\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	ids := fs.Args()
	if len(ids) == 0 {
		fs.Usage()
		return fmt.Errorf("no comment id given")
	}
	root := "."
	if *out != "" {
		root = *out
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve dir: %w", err)
	}
	dir := filepath.Join(absRoot, ".prereview")
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
	// append-only line format loadProcessedIDs reads.
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
