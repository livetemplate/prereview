package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/livetemplate/prereview/internal/library"
)

// repeated is a flag that may be given several times (each value kept).
type repeated []string

func (r *repeated) String() string { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// runLearn implements `prereview learn propose|list` (checks M3). The coding
// agent answers an extraction request by proposing learnings, each grounded in
// real review records; the reviewer accepts them in the Library. prereview never
// calls an LLM — the agent does the judging, prereview stores and checks.
func runLearn(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: prereview learn propose|list [flags] (see `prereview learn propose -h`)")
	}
	dir := library.Dir()
	if dir == "" {
		return errors.New("no user config directory for the library (set PREREVIEW_LIBRARY_DIR)")
	}
	switch args[0] {
	case "propose":
		return runLearnPropose(dir, args[1:], stdout)
	case "list":
		return runLearnList(dir, args[1:], stdout)
	default:
		return fmt.Errorf("unknown learn verb %q (want propose or list)", args[0])
	}
}

func runLearnPropose(dir string, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("learn propose", flag.ContinueOnError)
	text := fs.String("text", "", "the learning, as one instruction (\"Cut filler words: basically, really, just\")")
	scope := fs.String("scope", "", "where it applies, a short label (\"Technical docs\"); default "+library.DefaultScope)
	var globs, evidence repeated
	fs.Var(&globs, "glob", "optional, repeatable: files the scope covers (docs/**/*.md)")
	fs.Var(&evidence, "evidence", "repeatable, at least one: <store>:<id> of a comment, suggestion or reply the learning is drawn from (the request lists them)")
	replaces := fs.String("replaces", "", "optional: the id of an existing learning this one would supersede (a conflict: newer wins once accepted)")
	produces := fs.String("produces", "", "optional, comma-separated: what it should become — instruction, checker, judge")
	checker := fs.String("checker", "", "optional: a file (or - for stdin) with the check rule YAML this lesson becomes (existence, substitution, occurrence or vocab; never command)")
	judge := fs.String("judge", "", "optional: a file (or - for stdin) with the judge rubric Markdown this lesson becomes (kind: judge frontmatter + the rubric)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview learn propose --text \"…\" --evidence <store>:<id> [--evidence …]\n"+
				"                               [--scope \"…\"] [--glob <pattern>]… [--replaces <id>] [--produces instruction,checker,judge]\n"+
				"                               [--checker <rule.yml|->] [--judge <rubric.md|->]\n\n"+
				"  Propose one learning drawn from your reviews, answering an extraction request.\n"+
				"  Every --evidence must name a real record, or nothing is written. The reviewer\n"+
				"  accepts, edits or rejects it in the Library; nothing takes effect until then.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q (flags only)", fs.Arg(0))
	}
	p := library.Proposal{Text: *text, Scope: *scope, Globs: globs, Evidence: evidence, Replaces: *replaces}
	for _, k := range strings.Split(*produces, ",") {
		if k = strings.TrimSpace(k); k != "" {
			p.Produces = append(p.Produces, k)
		}
	}
	if *checker == "-" && *judge == "-" {
		return errors.New("only one of --checker and --judge can read stdin")
	}
	for _, f := range []struct {
		from string
		to   *string
	}{{*checker, &p.Checker}, {*judge, &p.Judge}} {
		if f.from == "" {
			continue
		}
		b, err := readArgFile(f.from)
		if err != nil {
			return err
		}
		*f.to = string(b)
	}
	l, err := library.Propose(dir, p)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "proposed %s (%d evidence)\n", l.ID, len(l.Evidence))
	return nil
}

func runLearnList(dir string, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("learn list", flag.ContinueOnError)
	status := fs.String("status", "", "only this status: proposed, accepted, rejected, superseded")
	asJSON := fs.Bool("json", false, "one JSON object per line (with evidence)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, l := range library.LoadLearnings(dir) {
		if *status != "" && l.Status != *status {
			continue
		}
		if *asJSON {
			b, err := json.Marshal(l)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s\n", b)
			continue
		}
		fmt.Fprintf(stdout, "%s  %-10s  %s  — %s\n", l.ID, l.Status, l.Text, l.Scope)
	}
	return nil
}

// learnMain is the `prereview learn` entry (from main).
func learnMain(args []string) {
	if err := runLearn(args, os.Stdout); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "prereview learn:", err)
		}
		os.Exit(1)
	}
}

// readArgFile reads a flag's file, or stdin for "-".
func readArgFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
