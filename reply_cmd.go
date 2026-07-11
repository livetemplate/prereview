package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/review"
)

// runReply implements `prereview reply [--out <dir>] (--body "…" | --file <f>|-)
// <id>`: the coding agent posts a thread entry — its brief "what I did" note, or a
// follow-up reply — on a comment or suggestion, so the reviewer sees it under the
// card (issue #149). It APPENDS one JSON line to <store>/.prereview/
// agent-replies.jsonl — an append-only, agent-owned log the server reads every
// Mount. Kept a pure append (never rewrites) so it never races the server's writes,
// exactly like the done and suggest subcommands.
//
// The id is VALIDATED against comments.csv AND suggestions — a reply targets a
// comment OR a suggestion — so a typo'd id fails loudly with a non-zero exit instead
// of recording a reply that renders nowhere. --out is the directory whose .prereview/
// holds the review (the REPO path prereview prints at launch); defaults to cwd.
func runReply(args []string) error {
	fs := flag.NewFlagSet("reply", flag.ContinueOnError)
	out := fs.String("out", "", "directory whose .prereview/ holds the review (the REPO printed at launch); defaults to the current directory")
	body := fs.String("body", "", "the reply text — a brief note on what you did")
	file := fs.String("file", "", "read the reply body from this file, or \"-\" for stdin (instead of --body)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview reply [--out <dir>] (--body \"…\" | --file <f>|-) <comment-or-suggestion-id>\n\n"+
				"  Post a thread reply on a comment or suggestion so the reviewer sees what\n"+
				"  you did (or your follow-up). Run by the coding agent after addressing a\n"+
				"  comment; --out must match the review's directory (the REPO line). The id is\n"+
				"  validated against comments.csv and suggestions — list ids with\n"+
				"  `prereview comments --json`.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return fmt.Errorf("reply takes exactly one comment or suggestion id")
	}
	id := rest[0]

	text := *body
	if text == "" && *file != "" {
		raw, err := readIDSource(*file) // "-" = stdin, else a file (shared with done)
		if err != nil {
			return err
		}
		text = string(raw)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		fs.Usage()
		return fmt.Errorf("empty reply — pass --body \"…\" or --file <f>|-")
	}

	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	csvPath := filepath.Join(dir, review.CommentsFileName)
	if err := validateReplyTarget(csvPath, id); err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, review.AgentRepliesFileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	// json.Encoder writes one object + newline → JSONL, matching the append-only
	// format loadThreadEntries reads. Nanosecond timestamp: the agent CLI and the
	// server share the same clock, so this sorts after the reviewer prompt it answers.
	entry := review.ThreadEntry{
		TargetID: id,
		Author:   review.AuthorAgent,
		Body:     text,
		At:       time.Now().UnixNano(),
	}
	if err := json.NewEncoder(f).Encode(entry); err != nil {
		return fmt.Errorf("append reply: %w", err)
	}
	fmt.Printf("posted reply on %s\n", id)
	return nil
}

// validateReplyTarget rejects an id that is neither a comment (comments.csv) nor a
// suggestion (suggestions.jsonl) — mirrors done's validateIDs. If the id is unknown
// AND there is nothing to validate against, it is almost always a wrong --out, so
// that is a distinct, actionable error rather than a bare "unknown id".
func validateReplyTarget(csvPath, id string) error {
	known := false
	// cerr is intentionally SOFT: a reply targets a comment OR a suggestion, so an
	// unreadable/missing comments.csv is only fatal when there are also no
	// suggestions (handled by the wrong-`--out` branch below), not on its own.
	rows, cerr := csv.Read(csvPath)
	for _, r := range rows {
		if r.ID == id {
			known = true
		}
	}
	sugs := review.LoadSuggestions(csvPath)
	for _, s := range sugs {
		if s.ID == id {
			known = true
		}
	}
	if known {
		return nil
	}
	if (cerr != nil || len(rows) == 0) && len(sugs) == 0 {
		return fmt.Errorf("no comments or suggestions found at %s — is --out the review's directory?", filepath.Dir(csvPath))
	}
	return fmt.Errorf("unknown id %q — not a comment or suggestion (use `prereview comments --json` to list ids)", id)
}
