package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
)

// repeated is a flag that may be given several times (each value kept).
type repeated []string

func (r *repeated) String() string { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// runLearn implements `prereview learn propose|list|wait|finish` (checks M3).
// A background learner the coding agent starts waits for a learning job
// (`wait`), proposes learnings grounded in real review records (`propose --job`)
// and finishes the job (`finish`). prereview never calls an LLM — the agent does
// the judging, prereview stores and checks.
func runLearn(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: prereview learn propose|list|wait|finish [flags] (see `prereview learn <verb> -h`)")
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
	case "wait":
		return runLearnWait(dir, args[1:], stdout)
	case "finish":
		return runLearnFinish(dir, args[1:], stdout)
	default:
		return fmt.Errorf("unknown learn verb %q (want propose, list, wait or finish)", args[0])
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
	produces := fs.String("produces", "", "optional, comma-separated: what it should become — instruction, checker, judge, shortcut")
	checker := fs.String("checker", "", "optional: a file (or - for stdin) with the check rule YAML this lesson becomes (existence, substitution, occurrence or vocab; never command)")
	judge := fs.String("judge", "", "optional: a file (or - for stdin) with the judge rubric Markdown this lesson becomes (kind: judge frontmatter + the rubric)")
	shortcutLabel := fs.String("shortcut-label", "", "optional, with --shortcut-body: a comment-box chip this lesson adds, in the reviewer's own words (\"Why is this here?\")")
	shortcutBody := fs.String("shortcut-body", "", "optional, with --shortcut-label: the comment text the chip fills in")
	job := fs.String("job", "", "the `id` of the learning job this answers (printed by prereview learn wait); its evidence must come from the job's stores")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview learn propose --job <id> --text \"…\" --evidence <store>:<id> [--evidence …]\n"+
				"                               [--scope \"…\"] [--glob <pattern>]… [--replaces <id>] [--produces instruction,checker,judge]\n"+
				"                               [--checker <rule.yml|->] [--judge <rubric.md|->]\n"+
				"                               [--shortcut-label \"…\" --shortcut-body \"…\"]\n\n"+
				"  Propose one learning drawn from your reviews, answering a learning job.\n"+
				"  Every --evidence must name a real record in one of the job's stores, or\n"+
				"  nothing is written. With \"Accept automatically\" on (the reviewer's\n"+
				"  switch), it takes effect at once and they can undo it; otherwise it\n"+
				"  waits for them in Library → Learnings. Without --job it always waits.\n\n")
		fs.PrintDefaults()
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q (flags only)", fs.Arg(0))
	}
	p := library.Proposal{Text: *text, Scope: *scope, Globs: globs, Evidence: evidence, Replaces: *replaces,
		ShortcutLabel: *shortcutLabel, ShortcutBody: *shortcutBody, Job: *job}
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
		b, err := readIDSource(f.from)
		if err != nil {
			return err
		}
		*f.to = string(b)
	}
	l, err := library.Propose(dir, p)
	if err != nil {
		return err
	}
	verb := "proposed"
	if l.Status == library.StatusAccepted {
		verb = "accepted"
	}
	fmt.Fprintf(stdout, "%s %s (%d evidence)\n", verb, l.ID, len(l.Evidence))
	return nil
}

func runLearnList(dir string, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("learn list", flag.ContinueOnError)
	status := fs.String("status", "", "only this status: proposed, accepted, rejected, superseded")
	asJSON := fs.Bool("json", false, "one JSON object per line (with evidence)")
	if err := parseInterspersed(fs, args); err != nil {
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

// learnWaitPoll is how often `learn wait` looks for a job (and touches the
// learner heartbeat, which must stay well inside library.LearnerStale).
var learnWaitPoll = time.Second

// runLearnWait is the background learner's loop step: it blocks until a learning
// job can be claimed, claims it and prints it (`job <id>`, a blank line, then the
// prompt), or prints `idle` at --timeout, or `stop` once the review named by
// --out has ended. While it waits, the review shows a learner as running.
func runLearnWait(dir string, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("learn wait", flag.ContinueOnError)
	out := fs.String("out", "", "optional: the review's STORE; once that review ends, print \"stop\" (your cue to exit the loop)")
	timeout := fs.Duration("timeout", 9*time.Minute, "print \"idle\" after this long with no job (keep it under your shell's command timeout); 0 checks once")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview learn wait [--out <STORE>] [--timeout 9m]\n\n"+
				"  Wait for a learning job and claim it. Prints one of:\n"+
				"    job <id>   then a blank line and the job: what to read and how to answer\n"+
				"    idle       no job before --timeout: run it again\n"+
				"    stop       the review --out names has ended: stop looping\n\n")
		fs.PrintDefaults()
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	var events string
	if *out != "" {
		d, err := storeDir(*out)
		if err != nil {
			return err
		}
		events = filepath.Join(d, review.EventsFileName)
	}
	deadline := time.Now().Add(*timeout)
	warned := false
	jobsPath := filepath.Join(dir, library.LearnJobsFile)
	var lastFP string
	var lastTry time.Time
	for {
		if err := library.TouchLearner(dir); err != nil && !warned {
			fmt.Fprintln(os.Stderr, "prereview learn wait: can't mark the learner running:", err)
			warned = true
		}
		if events != "" && streamEnded(events) {
			fmt.Fprintln(stdout, "stop")
			return nil
		}
		// Claiming takes the library lock every server writes under, so only try
		// when the job log changed, or a claim may have expired since the last try.
		if fp := fileFingerprint(jobsPath); fp != lastFP || time.Since(lastTry) >= claimRetry {
			lastFP, lastTry = fp, time.Now()
			j, ok, err := library.ClaimJob(dir)
			if err != nil {
				return err
			}
			if ok {
				fmt.Fprintf(stdout, "job %s\n\n%s", j.ID, library.ExtractionPrompt(j, library.LoadLearnings(dir)))
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintln(stdout, "idle")
			return nil
		}
		time.Sleep(learnWaitPoll)
	}
}

// claimRetry is how often `learn wait` retries a claim with no change to the
// job log: often enough to pick up an expired claim (library.JobClaimTTL).
const claimRetry = 30 * time.Second

// fileFingerprint is a file's size and mtime ("" when it's missing).
func fileFingerprint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprint(fi.Size(), fi.ModTime().UnixNano())
}

// streamEnded reports whether a review's event log ends with `end`. The server
// clears the log at launch and nothing follows `end` (a short line), so the last
// line of a small tail decides; a missing log (not started yet, or no --agent)
// hasn't ended.
func streamEnded(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	const tail = 4 << 10
	if fi, err := f.Stat(); err == nil && fi.Size() > tail {
		if _, err := f.Seek(fi.Size()-tail, io.SeekStart); err != nil {
			return false
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	b = bytes.TrimRight(b, "\n")
	ev, ok := peekEvent(b[bytes.LastIndexByte(b, '\n')+1:])
	return ok && ev.Event == "end"
}

// runLearnFinish marks a learning job done.
func runLearnFinish(dir string, args []string, stdout io.Writer) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: prereview learn finish <job id>")
	}
	if err := library.FinishJob(dir, args[0]); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "finished %s\n", args[0])
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
