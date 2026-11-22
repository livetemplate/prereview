package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
)

// runQuiz implements `prereview quiz [--out <dir>] [--file <f>]`: the coding
// agent submits a generated comprehension quiz about a file's diff, which the
// live review UI renders so the reviewer can test their understanding before
// accepting the change (issue #191).
//
// It reads a JSON payload from --file (or stdin) — a single object, a JSON array,
// or JSONL — and APPENDS one normalized JSON line per quiz to
// <store>/.prereview/quiz.jsonl. A pure append, like `suggest`, so it never races
// the server's comments.csv writes and survives relaunches. Re-submitting the same
// quiz `id` revises it (the loader keeps the last write per id).
//
// Every quiz is validated with review.ValidateQuiz BEFORE anything is written, and
// a failure names the offending question rather than recording garbage — the same
// fail-loudly contract `prereview done` applies to unknown ids. That check is
// deliberately here, not in the prompt: quiz prompts are user-replaceable, so a
// custom prompt must not be able to weaken the schema. (The other half of the
// contract — that a cited line range actually exists in the diff — is enforced by
// the server, which is the side that holds the diff.)
func runQuiz(args []string) error {
	fs := flag.NewFlagSet("quiz", flag.ContinueOnError)
	out := fs.String("out", "", "this review's store: the STORE path printed at launch (the reviewed file, or a directory whose .prereview/ holds the review, also work); defaults to the current directory")
	file := fs.String("file", "", "read the JSON payload from this file, or \"-\" for stdin (the default when omitted)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview quiz [--out <dir>] [--file <quiz.json>]\n\n"+
				"  Submit a comprehension quiz about the change, which the live review UI\n"+
				"  renders for the reviewer to answer. Reads a JSON payload from --file or\n"+
				"  stdin: a single object, a JSON array, or newline-delimited objects (JSONL).\n\n"+
				"    {\n"+
				"      \"id\":   \"stable-id\",           // optional; re-use to revise a quiz\n"+
				"      \"file\": \"internal/x/y.go\",     // the file, for a quiz on one file; omit for the whole change\n"+
				"      \"questions\": [\n"+
				"        {\n"+
				"          \"file\":      \"internal/x/y.go\", // the file it asks about; required without a quiz \"file\"\n"+
				"          \"probe\":     \"consequence\", // change-type|localization|consequence|rationale|decision\n"+
				"          \"prompt\":    \"What breaks if …?\",\n"+
				"          \"options\":   [\"…\", \"…\", \"…\"],  // >= 2, all plausible\n"+
				"          \"answer\":    1,               // 0-based index into options\n"+
				"          \"why\":       \"shown after answering\",\n"+
				"          \"from_line\": 211,             // 0 only for a `decision` about an omission\n"+
				"          \"to_line\":   238,\n"+
				"          \"side\":      \"new\"           // \"new\" | \"old\"\n"+
				"        }\n"+
				"      ]\n"+
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

	quizzes, err := parseQuizInput(raw)
	if err != nil {
		return err
	}
	if len(quizzes) == 0 {
		fs.Usage()
		return fmt.Errorf("no quizzes in payload")
	}

	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, review.QuizFileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	questions := 0
	enc := json.NewEncoder(f)
	for i := range quizzes {
		if err := enc.Encode(quizzes[i]); err != nil {
			return fmt.Errorf("append quiz %s: %w", quizzes[i].ID, err)
		}
		questions += len(quizzes[i].Questions)
	}
	fmt.Printf("submitted %d quiz(zes), %d question(s)\n", len(quizzes), questions)
	checkAgentText(os.Stderr, library.QuizText, quizPieces(quizzes))
	return nil
}

// parseQuizInput decodes the agent's payload — a JSON array, a single object, or
// JSONL — then normalizes and validates each quiz. Validation happens for the
// WHOLE payload before the caller writes anything, so a bad second quiz can't
// leave a half-written file behind.
func parseQuizInput(raw []byte) ([]review.Quiz, error) {
	parsed, err := decodePayload[review.Quiz](raw)
	if err != nil {
		return nil, err
	}

	out := make([]review.Quiz, 0, len(parsed))
	for i := range parsed {
		q := review.NormalizeQuiz(parsed[i])
		if q.CreatedAt == "" {
			q.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if err := review.ValidateQuiz(q); err != nil {
			return nil, fmt.Errorf("quiz %d: %w", i+1, err)
		}
		out = append(out, q)
	}
	return out, nil
}
