package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/livetemplate/prereview/internal/bench"
	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
	"github.com/livetemplate/prereview/internal/skills"
)

// runJudge implements `prereview judge --out <store> --file F --judge ID
// --content-hash H --rubric-hash R --verdict pass|fail --critique "…"
// [--quote "…"] [--model M]`: the coding agent's answer to a "Run judges"
// request (checks M2). One call per judge; each appends one verdict to
// <store>/judgments.jsonl, which the review UI picks up live. prereview never
// calls an LLM — the agent is the judge.
//
// The request carries the file's content hash and each rubric's hash, and the
// verdict records them, so the review can tell when it goes out of date (the file
// or the rubric changed since). The CLI never resolves or re-reads the file: a
// store's directory isn't necessarily the repo (--out), and the hash from the
// request is exactly what was judged. Everything is validated before anything is
// written.
func runJudge(args []string) error {
	fs := flag.NewFlagSet("judge", flag.ContinueOnError)
	out := fs.String("out", "", "this review's store: the STORE path printed at launch (the reviewed file, or a directory whose .prereview/ holds the review, also work); defaults to the current directory")
	file := fs.String("file", "", "the judged file, as the request names it (review-relative)")
	judge := fs.String("judge", "", "the judge's ID, as the request names it")
	contentHash := fs.String("content-hash", "", "the file's content hash, as the request names it")
	rubricHash := fs.String("rubric-hash", "", "the judge's rubric hash, as the request names it")
	verdict := fs.String("verdict", "", "pass or fail")
	critique := fs.String("critique", "", "why it passed or failed, in a sentence or two")
	quote := fs.String("quote", "", "optional: the exact passage the critique is about, so the reviewer can jump to it")
	model := fs.String("model", "", "optional: the model id that judged")
	benchID := fs.String("bench", "", "answering a benchmark run: the benchmark's id (with --run and --case, instead of --out and --file)")
	runID := fs.String("run", "", "the benchmark run's id, as the request names it")
	caseID := fs.String("case", "", "the benchmark case's id, as the request names it")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview judge --out <store> --file <f> --judge <id> --content-hash <h> --rubric-hash <h>\n"+
				"                       --verdict pass|fail --critique \"…\" [--quote \"…\"] [--model <id>]\n\n"+
				"  Record your verdict for one judge on one file, answering a \"Run judges\"\n"+
				"  request from the review. The request lists each judge's rubric and the\n"+
				"  exact command (with both hashes). A verdict is binary; the critique says why.\n"+
				"  Answering a benchmark run: --bench <id> --run <run> --case <case> instead of\n"+
				"  --out/--file, as the benchmark's request names them.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q (flags only)", fs.Arg(0))
	}
	if *benchID != "" {
		return judgeBenchCase(*benchID, *runID, *caseID, *judge, *contentHash, *rubricHash, *verdict, *critique, *model)
	}
	if *file == "" || *judge == "" || *verdict == "" || strings.TrimSpace(*critique) == "" || *contentHash == "" || *rubricHash == "" {
		fs.Usage()
		return errors.New("--file, --judge, --content-hash, --rubric-hash, --verdict and --critique are required (copy the command from the request)")
	}
	if !review.ValidVerdict(*verdict) {
		return fmt.Errorf("--verdict must be pass or fail, got %q", *verdict)
	}
	rel, err := reviewRelPath(*file)
	if err != nil {
		return err
	}

	lib := checks.Load(checks.UserDirs(userChecksDir(), skills.LearnedChecksDir(library.Dir()))...)
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

	store, err := storeDir(*out)
	if err != nil {
		return err
	}
	j := review.Judgment{File: rel, Judge: rule.ID, Verdict: *verdict, Critique: strings.TrimSpace(*critique),
		Quote: strings.TrimSpace(*quote), Model: strings.TrimSpace(*model),
		ContentHash: *contentHash, RubricHash: *rubricHash}
	if err := review.AppendJudgment(filepath.Join(store, review.CommentsFileName), j); err != nil {
		return err
	}
	fmt.Printf("recorded %s on %s: %s\n", rule.ID, rel, *verdict)
	if *rubricHash != rule.RubricHash {
		fmt.Fprintf(os.Stderr, "note: %s's rubric changed since the request; the review shows this verdict as out of date\n", rule.ID)
	}
	return nil
}

// reviewRelPath checks a judged file is named the way the review keys files:
// a clean, relative, slash path that doesn't climb out of the review.
func reviewRelPath(file string) (string, error) {
	p := filepath.ToSlash(file)
	clean := path.Clean(p)
	if path.IsAbs(clean) || filepath.IsAbs(file) || clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		return "", fmt.Errorf("%s is outside the review: --file takes the review-relative path the request names", file)
	}
	return clean, nil
}

// judgeBenchCase records a verdict for one case of a benchmark run (checks M5):
// validated against the run's pins — the case must be in the run and the
// content and rubric hashes must be the ones it started with — before anything
// is written.
func judgeBenchCase(benchID, runID, caseID, judge, contentHash, rubricHash, verdict, critique, model string) error {
	switch {
	case runID == "" || caseID == "" || judge == "" || verdict == "" || strings.TrimSpace(critique) == "" || contentHash == "" || rubricHash == "":
		return errors.New("--bench needs --run, --case, --judge, --content-hash, --rubric-hash, --verdict and --critique (copy the command from the request)")
	case !review.ValidVerdict(verdict):
		return fmt.Errorf("--verdict must be pass or fail, got %q", verdict)
	case !bench.ValidID(benchID) || !bench.ValidID(runID) || !bench.ValidID(caseID):
		return errors.New("--bench, --run and --case take the ids the request names")
	}
	lib := library.Dir()
	bm, err := bench.Load(lib, benchID)
	if err != nil {
		return fmt.Errorf("no benchmark %q: %w", benchID, err)
	}
	var run *bench.Run
	for _, r := range bench.LoadRuns(lib, benchID) {
		if r.ID == runID {
			run = &r
			break
		}
	}
	switch {
	case run == nil:
		return fmt.Errorf("benchmark %s has no run %s", benchID, runID)
	case !slices.ContainsFunc(bm.Checks, func(c bench.CheckSpec) bool { return c.ID == judge }):
		return fmt.Errorf("benchmark %s doesn't score %s", benchID, judge)
	case run.Pins.Cases[caseID] == "":
		return fmt.Errorf("run %s has no case %s", runID, caseID)
	case run.Pins.Cases[caseID] != contentHash:
		return fmt.Errorf("case %s's content hash is %s in this run, not %s: copy the command from the request", caseID, run.Pins.Cases[caseID], contentHash)
	case run.Pins.Checks[judge] != rubricHash:
		return fmt.Errorf("%s's rubric is %s in this run, not %s: copy the command from the request", judge, run.Pins.Checks[judge], rubricHash)
	}
	if err := bench.RecordResult(lib, benchID, bench.Record{Run: runID, Case: caseID, Check: judge, Judge: true,
		Status: verdict, Critique: strings.TrimSpace(critique), Model: strings.TrimSpace(model)}); err != nil {
		return err
	}
	fmt.Printf("recorded %s on %s/%s: %s\n", judge, benchID, caseID, verdict)
	return nil
}
