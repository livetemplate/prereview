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
	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
	"github.com/livetemplate/prereview/internal/skills"
)

// errChecksFailed makes `prereview check` exit 1 without an extra error line: the
// findings already printed are the message.
var errChecksFailed = errors.New("checks failed")

// runCheck implements `prereview check [--json] <file>...`: the same checks the
// Checks page shows (built-ins, your checks folder, the repo's Vale config when
// vale is installed), run once from the CLI so an agent can self-check before it
// marks work done. Checks you turned off on the Checks page are skipped here too. Exits 1
// when any check fails or errors.
func runCheck(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the results as JSON (one entry per file, every check with its findings)")
	hook := fs.Bool("hook", false, "run as Claude Code's PostToolUse hook: read the hook input on stdin, check the lines the edit wrote, exit 2 with findings")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview check [--json] <file>...\n\n"+
				"  Run the review's checks on each file: the built-in prose rules, the rules in\n"+
				"  your checks folder ("+userChecksDir()+"), and the repo's Vale\n"+
				"  config when you've trusted it on the Checks page and vale is installed. Prints each finding as path:line:col; exits 1\n"+
				"  when any check fails or errors. Checks turned off on the Checks page are skipped.\n")
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if *hook {
		return runHook(os.Stdin, os.Stderr)
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("missing file")
	}
	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	run := newCheckRun(repo)
	var reports []review.PathChecks // the same shape the Checks page reads
	failed := false
	for _, arg := range fs.Args() {
		results, err := run.file(arg)
		if err != nil {
			return err
		}
		for _, r := range results {
			if r.Status != checks.StatusPass {
				failed = true
			}
		}
		reports = append(reports, review.PathChecks{Path: arg, Results: results})
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

// checkRun is the checks `prereview check` runs in a repo: built-ins, your
// checks folder and learned checks, the repo's Vale config when trusted, minus
// the checks turned off on the Checks page.
type checkRun struct {
	repo      string
	lib       *checks.Library
	disabled  []string
	trustVale bool
}

// A turned-off rule is dropped before anything runs (a command check would
// otherwise start its process only to be discarded); Vale's results are
// filtered after, since Vale reports its own rule IDs.
func newCheckRun(repo string) checkRun {
	disabled, trustVale := review.CheckPrefs(uiPrefsPath(), repo)
	lib := checks.Load(checks.UserDirs(userChecksDir(), skills.LearnedChecksDir(library.Dir()))...)
	lib.Rules = slices.DeleteFunc(lib.Rules, func(r *checks.Rule) bool { return slices.Contains(disabled, r.ID) })
	return checkRun{repo: repo, lib: lib, disabled: disabled, trustVale: trustVale}
}

// file runs the checks on one file (path relative to the cwd, or absolute).
func (c checkRun) file(path string) ([]checks.Result, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	lines, err := checks.ReadLines(abs)
	if err != nil {
		return nil, err
	}
	return c.lines(abs, lines), nil
}

// lines runs the checks on a file's lines, already read.
func (c checkRun) lines(abs string, lines []string) []checks.Result {
	var kept []checks.Result
	for _, r := range c.lib.RunAll(context.Background(), c.repo, checkRel(c.repo, abs), lines, c.trustVale) {
		if !slices.Contains(c.disabled, r.ID) {
			kept = append(kept, r)
		}
	}
	return kept
}

// checkRel is abs as the checks' globs match it: relative to repo, or the bare
// name for a file outside it.
func checkRel(repo, abs string) string {
	rel, err := filepath.Rel(repo, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = filepath.Base(abs)
	}
	return filepath.ToSlash(rel)
}

// findingLine is one finding as every CLI check prints it: path:line:col.
func findingLine(where string, r checks.Result, f checks.Finding) string {
	return fmt.Sprintf("%s:%d:%d: %s: %s (%s)", where, f.Line, f.FromCol+1, f.Severity, f.Message, r.Name)
}

// errorLine is a check that couldn't run.
func errorLine(where string, r checks.Result) string {
	return fmt.Sprintf("%s: %s: error: %s", where, r.Name, r.Err)
}

func printCheckReports(w io.Writer, reports []review.PathChecks) {
	for _, rep := range reports {
		passed := 0
		for _, r := range rep.Results {
			switch r.Status {
			case checks.StatusPass:
				passed++
			case checks.StatusError:
				fmt.Fprintln(w, errorLine(rep.Path, r))
			default:
				for _, f := range r.Findings {
					fmt.Fprintln(w, findingLine(rep.Path, r, f))
				}
			}
		}
		fmt.Fprintf(w, "%s: %d/%d checks passed\n", rep.Path, passed, len(rep.Results))
	}
}
