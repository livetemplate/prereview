package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
)

// runExplain implements `prereview explain [--out <dir>] [--file <f>]`: the agent
// submits a walkthrough of the whole change (#115), which the review UI's
// Understand page shows step by step.
//
// Same contract as `prereview quiz`: a JSON payload from --file or stdin (one
// object, an array, or JSONL), each explanation normalized and validated BEFORE
// anything is written, then appended as one line to <store>/.prereview/explain.jsonl.
// Re-submitting an id revises it. Whether each cited line exists is the server's
// half: it holds the diff.
func runExplain(args []string) error {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	out := fs.String("out", "", "this review's store: the STORE path printed at launch (the reviewed file, or a directory whose .prereview/ holds the review, also work); defaults to the current directory")
	file := fs.String("file", "", "read the JSON payload from this file, or \"-\" for stdin (the default when omitted)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview explain [--out <dir>] [--file <explain.json>]\n\n"+
				"  Submit a walkthrough of the whole change: steps in the order to read them,\n"+
				"  each citing the lines it explains. The review UI walks the reviewer through\n"+
				"  them and flags the changed lines no step explains. Reads a JSON payload from\n"+
				"  --file or stdin: a single object, a JSON array, or JSONL.\n\n"+
				"    {\n"+
				"      \"id\":      \"stable-id\",        // optional; re-use to revise\n"+
				"      \"summary\": \"what the change does, in a sentence or two\",\n"+
				"      \"steps\": [\n"+
				"        {\n"+
				"          \"title\": \"Retry loop in the worker\",\n"+
				"          \"body\":  \"why it changed, and what to check\",\n"+
				"          \"anchors\": [\n"+
				"            {\"file\": \"worker.go\", \"from_line\": 40, \"to_line\": 72},  // side \"new\" (default) | \"old\"\n"+
				"            {\"file\": \"logo.png\", \"kind\": \"file\"}                  // a whole file\n"+
				"          ]\n"+
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
	exps, err := parseExplainInput(raw)
	if err != nil {
		return err
	}
	if len(exps) == 0 {
		fs.Usage()
		return fmt.Errorf("no explanations in payload")
	}

	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	steps := 0
	vs := make([]any, len(exps))
	for i := range exps {
		vs[i] = exps[i]
		steps += len(exps[i].Steps)
	}
	if err := review.AppendJSONL(filepath.Join(dir, review.ExplainFileName), vs...); err != nil {
		return fmt.Errorf("append explanations: %w", err)
	}
	fmt.Printf("submitted %d explanation(s), %d step(s)\n", len(exps), steps)
	checkAgentText(os.Stderr, library.ExplainText, explainPieces(exps))
	return nil
}

// parseExplainInput decodes, normalizes and validates the whole payload before
// the caller writes anything, so a bad second explanation can't leave a
// half-written file.
func parseExplainInput(raw []byte) ([]review.Explanation, error) {
	parsed, err := decodePayload[review.Explanation](raw)
	if err != nil {
		return nil, err
	}
	out := make([]review.Explanation, 0, len(parsed))
	for i := range parsed {
		e := review.NormalizeExplanation(parsed[i])
		if e.CreatedAt == "" {
			e.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if err := review.ValidateExplanation(e); err != nil {
			return nil, fmt.Errorf("explanation %d: %w", i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}
