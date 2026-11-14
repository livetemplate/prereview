package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/review"
)

// errChecksFailed makes `prereview check` exit 1 without an extra error line: the
// findings already printed are the message.
var errChecksFailed = errors.New("checks failed")

// checkFileReport is one file's results in `prereview check --json`.
type checkFileReport struct {
	Path    string          `json:"path"`
	Results []checks.Result `json:"results"`
}

// runCheck implements `prereview check [--json] <file>...`: the same checks the
// review panel shows (built-ins, your checks folder, the repo's Vale config when
// vale is installed), run once from the CLI so an agent can self-check before it
// marks work done. Checks you disabled in the panel are skipped here too. Exits 1
// when any check fails or errors.
func runCheck(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the results as JSON (one entry per file, every check with its findings)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview check [--json] <file>...\n\n"+
				"  Run the review's checks on each file: the built-in prose rules, the rules in\n"+
				"  your checks folder ("+userChecksDir()+"), and the repo's Vale\n"+
				"  config when vale is installed. Prints each finding as path:line:col; exits 1\n"+
				"  when any check fails or errors. Checks disabled in the panel are skipped.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("missing file")
	}
	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	lib := checks.Load(checks.Dir{Path: userChecksDir(), Source: checks.SourceUser})
	disabled := review.DisabledCheckIDs(uiPrefsPath())

	var reports []checkFileReport
	failed := false
	for _, arg := range fs.Args() {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return err
		}
		lines, err := readLines(abs)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repo, abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(abs) // outside cwd: match globs on the name alone
		}
		rel = filepath.ToSlash(rel)
		var kept []checks.Result
		for _, r := range lib.RunAll(context.Background(), repo, rel, lines) {
			if slices.Contains(disabled, r.ID) {
				continue
			}
			if r.Status != checks.StatusPass {
				failed = true
			}
			kept = append(kept, r)
		}
		reports = append(reports, checkFileReport{Path: arg, Results: kept})
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			return err
		}
	} else {
		printCheckReports(stdout, reports)
	}
	if failed {
		return errChecksFailed
	}
	return nil
}

func printCheckReports(w io.Writer, reports []checkFileReport) {
	for _, rep := range reports {
		passed := 0
		for _, r := range rep.Results {
			switch r.Status {
			case checks.StatusPass:
				passed++
			case checks.StatusError:
				fmt.Fprintf(w, "%s: %s: error: %s\n", rep.Path, r.Name, r.Err)
			default:
				for _, f := range r.Findings {
					fmt.Fprintf(w, "%s:%d:%d: %s: %s (%s)\n", rep.Path, f.Line, f.FromCol+1, f.Severity, f.Message, r.Name)
				}
			}
		}
		fmt.Fprintf(w, "%s: %d/%d checks passed\n", rep.Path, passed, len(rep.Results))
	}
}

// readLines reads a file as the diff view's lines: split on "\n" after dropping
// the final newline (gitdiff's split), so findings land on the same columns.
func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), nil
}
