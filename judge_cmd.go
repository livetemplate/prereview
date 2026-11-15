package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/review"
)

// runJudge implements `prereview judge --out <store> --file F --judge ID
// --verdict pass|fail --critique "…" [--quote "…"] [--model M]`: the coding
// agent's answer to a "Run judges" request (checks M2). One call per judge; each
// appends one verdict to <store>/judgments.jsonl, which the review UI picks up
// live. prereview never calls an LLM — the agent is the judge.
//
// It records the file's content hash and the rubric's hash with the verdict, so
// the review can tell when a verdict is out of date (the file or the rubric
// changed since). Everything is validated before anything is written.
func runJudge(args []string) error {
	fs := flag.NewFlagSet("judge", flag.ContinueOnError)
	out := fs.String("out", "", "this review's store: the STORE path printed at launch (the reviewed file, or a directory whose .prereview/ holds the review, also work); defaults to the current directory")
	file := fs.String("file", "", "the judged file, relative to the review's root (as the request names it)")
	judge := fs.String("judge", "", "the judge's ID (as the request names it)")
	verdict := fs.String("verdict", "", "pass or fail")
	critique := fs.String("critique", "", "why it passed or failed, in a sentence or two")
	quote := fs.String("quote", "", "optional: the exact passage the critique is about, so the reviewer can jump to it")
	model := fs.String("model", "", "optional: the model id that judged")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview judge --out <store> --file <f> --judge <id> --verdict pass|fail --critique \"…\" [--quote \"…\"] [--model <id>]\n\n"+
				"  Record your verdict for one judge on one file, answering a \"Run judges\"\n"+
				"  request from the review. The request lists each judge's rubric and the\n"+
				"  exact command. A verdict is binary; the critique says why.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q (flags only)", fs.Arg(0))
	}
	if *file == "" || *judge == "" || *verdict == "" || strings.TrimSpace(*critique) == "" {
		fs.Usage()
		return errors.New("--file, --judge, --verdict and --critique are required")
	}
	if *verdict != checks.StatusPass && *verdict != checks.StatusFail {
		return fmt.Errorf("--verdict must be pass or fail, got %q", *verdict)
	}

	store, err := storeDir(*out)
	if err != nil {
		return err
	}
	root := storeRoot(store)
	rel, abs, err := reviewFile(root, *file)
	if err != nil {
		return err
	}
	lines, err := checks.ReadLines(abs)
	if err != nil {
		return fmt.Errorf("read %s: %w", rel, err)
	}

	lib := checks.Load(checks.Dir{Path: userChecksDir(), Source: checks.SourceUser})
	var rule *checks.Rule
	for _, r := range lib.Rules {
		if r.ID == *judge {
			rule = r
		}
	}
	switch {
	case rule == nil:
		return fmt.Errorf("no judge %q in %s", *judge, userChecksDir())
	case !rule.IsJudge():
		return fmt.Errorf("%q is a checker, not a judge", *judge)
	case rule.Err != "":
		return fmt.Errorf("judge %q doesn't load: %s", *judge, rule.Err)
	case !rule.Applies(rel):
		return fmt.Errorf("judge %q doesn't apply to %s (its files: %v)", *judge, rel, rule.Files)
	}

	j := review.Judgment{File: rel, Judge: rule.ID, Verdict: *verdict, Critique: strings.TrimSpace(*critique),
		Quote: strings.TrimSpace(*quote), Model: strings.TrimSpace(*model),
		ContentHash: checks.ContentHash(lines), RubricHash: rule.RubricHash}
	if err := review.AppendJudgment(filepath.Join(store, review.CommentsFileName), j); err != nil {
		return err
	}
	fmt.Printf("recorded %s on %s: %s\n", rule.ID, rel, *verdict)
	return nil
}

// storeRoot is the directory a store belongs to: the path above its
// .prereview segment (the REPO line printed at launch).
func storeRoot(store string) string {
	p := filepath.ToSlash(store)
	if i := strings.LastIndex(p, "/"+storeDirName); i >= 0 {
		return filepath.FromSlash(p[:i])
	}
	return filepath.Dir(store)
}

// reviewFile resolves a judged file against the review's root and refuses one
// outside it. It returns the repo-relative (slash) path the review keys on.
func reviewFile(root, file string) (rel, abs string, err error) {
	abs = file
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, file)
	}
	abs = filepath.Clean(abs)
	r, err := filepath.Rel(root, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%s is outside the review (%s)", file, root)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.Mode().IsRegular() {
		return "", "", fmt.Errorf("%s: not a file in the review (%s)", file, root)
	}
	return filepath.ToSlash(r), abs, nil
}
