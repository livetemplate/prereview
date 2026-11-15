package review

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/livetemplate/livetemplate"

	"github.com/livetemplate/prereview/internal/bench"
	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
)

// Benchmarks (checks M5): a set of document snapshots scored by a set of
// checks, run again whenever you change a check, a rubric or the judging model.
// A run pins every check's version and every case's text; checkers run here at
// once, and the judges go to your agent as ONE request covering every case.

// libTabBenchmarks is the Library's Benchmarks tab.
const libTabBenchmarks = "benchmarks"

// BenchTab is the Benchmarks tab.
type BenchTab struct {
	Cards  []BenchCard
	Judges []JudgeCalRow // the calibration of every judge you have
	CanAdd bool          // a file is open: it can become a case / a new benchmark
}

// BenchCard is one benchmark.
type BenchCard struct {
	ID, Name                 string
	Cases, Checkers, Judges  int
	Weighted                 bool   // not every weight is 1
	Score, Spread            string // "0.83" and "± 0.06 over 3 runs"
	Headers                  []string
	Rows                     []BenchRow
	Runs                     []BenchRunRow
	Asking                   bool // its judges were asked for and not all answered
	DefPath                  string
	EditorURL                template.URL
}

// BenchRow is one case of the latest run.
type BenchRow struct {
	Label string
	Cells []BenchCell
}

// BenchCell is one check on one case: ✓, ✗, … (waiting) or ! (errored).
type BenchCell struct {
	Mark, Class, Title string
}

// BenchRunRow is one run in the runs table.
type BenchRunRow struct {
	When, Score, Model, Pins string
	Comparable, Partial      bool
}

// JudgeCalRow is one judge's calibration on the Benchmarks tab.
type JudgeCalRow struct {
	Name, Agrees, Status string
	Calibrated           bool
}

// maxBenchRuns bounds the runs table.
const maxBenchRuns = 10

// applyBenchTab builds the Benchmarks tab while it's open (nil otherwise).
func (c *PrereviewController) applyBenchTab(state *PrereviewState) {
	state.BenchTab = nil
	if !state.ShowLibrary || state.libraryTab() != libTabBenchmarks || c.LibraryDir == "" {
		return
	}
	tab := &BenchTab{CanAdd: state.CurrentDiff != nil && !state.CurrentDiff.IsBinary && state.SelectedFile != ""}
	rules := c.rulesByID()
	for _, bm := range bench.List(c.LibraryDir) {
		tab.Cards = append(tab.Cards, c.benchCard(state, bm, rules))
	}
	cal := c.calibration()
	for _, r := range rules {
		if !r.IsJudge() || r.Err != "" {
			continue
		}
		cc := cal[r.ID]
		row := JudgeCalRow{Name: r.Name, Agrees: fmt.Sprintf("%d / %d", cc.Agree, cc.Total), Calibrated: cc.Calibrated, Status: "advisory"}
		switch {
		case cc.Calibrated:
			row.Status = "calibrated"
		case cc.Total < library.CalibrateMinLabels:
			row.Status = fmt.Sprintf("advisory · needs %d answers", library.CalibrateMinLabels)
		}
		if cc.Flaky {
			row.Status += " · flaky"
		}
		if cc.OneSided {
			row.Status += " · doesn't discriminate"
		}
		tab.Judges = append(tab.Judges, row)
	}
	state.BenchTab = tab
}

func (c *PrereviewController) benchCard(state *PrereviewState, bm bench.Benchmark, rules map[string]*checks.Rule) BenchCard {
	card := BenchCard{ID: bm.ID, Name: bm.Name, Cases: len(bm.Cases), DefPath: bench.DefPath(c.LibraryDir, bm.ID)}
	card.EditorURL = editorURL(state.Editor, state.EditorTemplate, state.EditorSSHHost, card.DefPath, 1)
	for _, k := range bm.Checks {
		name := k.ID
		if r := rules[k.ID]; r != nil {
			name = r.Name
			if r.IsJudge() {
				card.Judges++
			} else {
				card.Checkers++
			}
		}
		if k.W() != 1 {
			card.Weighted = true
		}
		card.Headers = append(card.Headers, name)
	}
	runs := bench.LoadRuns(c.LibraryDir, bm.ID)
	tr := bm.Trend(runs)
	switch {
	case tr.N == 0:
		card.Score, card.Spread = "—", "no complete run yet"
	case tr.N == 1:
		card.Score, card.Spread = fmt.Sprintf("%.2f", tr.Mean), "1 run · weighted pass rate"
	default:
		card.Score, card.Spread = fmt.Sprintf("%.2f", tr.Mean), fmt.Sprintf("± %.2f over %d runs · weighted pass rate", tr.Std, tr.N)
	}
	var anchor *bench.Run
	for i := range runs {
		if runs[i].ID == tr.Latest {
			anchor = &runs[i]
		}
	}
	if len(runs) > 0 { // the matrix shows the newest run, finished or not
		latest := runs[0]
		card.Asking = !bm.Score(latest).Complete
		for _, cs := range bm.Cases {
			row := BenchRow{Label: cs.Label}
			for _, k := range bm.Checks {
				row.Cells = append(row.Cells, benchCell(latest, cs.ID, k.ID))
			}
			card.Rows = append(card.Rows, row)
		}
	} else {
		for _, cs := range bm.Cases {
			card.Rows = append(card.Rows, BenchRow{Label: cs.Label})
		}
	}
	for i, r := range runs {
		if i == maxBenchRuns {
			break
		}
		s := bm.Score(r)
		row := BenchRunRow{When: shortWhen(r.At), Model: r.Model, Partial: !s.Complete, Pins: pinsText(bm, r.Pins),
			Comparable: anchor == nil || bench.Comparable(*anchor, r)}
		row.Score = fmt.Sprintf("%.2f", s.Value)
		if row.Partial {
			row.Score = fmt.Sprintf("%.2f so far (%d waiting)", s.Value, s.Pending)
		}
		card.Runs = append(card.Runs, row)
	}
	return card
}

func benchCell(r bench.Run, caseID, check string) BenchCell {
	res, ok := r.Result(caseID, check)
	switch {
	case !ok:
		return BenchCell{"…", "bm-wait", "waiting for your agent"}
	case res.Status == bench.Pass:
		return BenchCell{"✓", "bm-ok", res.Critique}
	case res.Status == bench.Fail:
		return BenchCell{"✗", "bm-no", res.Critique}
	}
	return BenchCell{"!", "bm-err", "errored (not counted): " + res.Critique}
}

// pinsText is a run's check versions, short: "filler@3f2a · reader@91c0".
func pinsText(bm bench.Benchmark, p bench.Pins) string {
	var parts []string
	for _, k := range bm.Checks {
		h := p.Checks[k.ID]
		if len(h) > 4 {
			h = h[:4]
		}
		parts = append(parts, k.ID+"@"+h)
	}
	return strings.Join(parts, " · ")
}

func shortWhen(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2 Jan 2006 15:04")
}

// rulesByID is the loaded checks, by id.
func (c *PrereviewController) rulesByID() map[string]*checks.Rule {
	r := c.runner()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]*checks.Rule, len(r.lib.Rules))
	for _, rule := range r.lib.Rules {
		out[rule.ID] = rule
	}
	return out
}

// checkPin is a check's version: a judge's rubric hash, a checker's file
// content (a built-in's is the binary's version).
func (c *PrereviewController) checkPin(rule *checks.Rule) string {
	if rule.IsJudge() {
		return rule.RubricHash
	}
	if rule.Path != "" {
		if b, err := os.ReadFile(rule.Path); err == nil {
			return bench.HashBytes(b)
		}
	}
	return bench.HashBytes([]byte("builtin:" + rule.ID + ":" + c.Version))
}

// CreateBenchmark starts a benchmark from the open file: it becomes the first
// case, scored by every enabled check that applies to it.
func (c *PrereviewController) CreateBenchmark(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	name := strings.TrimSpace(ctx.GetString("name"))
	if c.LibraryDir == "" || state.SelectedFile == "" || name == "" {
		return state, errors.New("new benchmark: name it, with a file open")
	}
	var ids []string
	checkers, judges := c.checkSetup(state.TrustedVale).rulesFor(state.SelectedFile)
	for _, r := range append(checkers, judges...) {
		if r.Err == "" && !state.checkDisabled(r.ID) && r.Source != checks.SourceVale {
			ids = append(ids, r.ID)
		}
	}
	bm, err := bench.Create(c.LibraryDir, name, ids)
	if err != nil {
		return state, err
	}
	if err := c.addOpenFileCase(state, bm.ID); err != nil {
		return state, err
	}
	state.Flash = "Created the “" + bm.Name + "” benchmark"
	c.applyBenchTab(&state)
	return state, nil
}

// AddBenchmarkCase snapshots the open file into a benchmark.
func (c *PrereviewController) AddBenchmarkCase(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if err := c.addOpenFileCase(state, ctx.GetString("id")); err != nil {
		return state, err
	}
	state.Flash = "Added " + filepath.Base(state.SelectedFile) + " as a case"
	c.applyBenchTab(&state)
	return state, nil
}

func (c *PrereviewController) addOpenFileCase(state PrereviewState, id string) error {
	if state.SelectedFile == "" {
		return errors.New("open a file to add it as a case")
	}
	data, err := os.ReadFile(filepath.Join(c.RepoPath, filepath.FromSlash(state.SelectedFile)))
	if err != nil {
		return fmt.Errorf("read %s: %w", state.SelectedFile, err)
	}
	_, err = bench.AddCase(c.LibraryDir, id, filepath.Base(state.SelectedFile), state.SelectedFile, data)
	return err
}

// RunBenchmark starts a run: every check's version and case's text pinned, the
// checkers run now (command checks in the background), and the judges asked of
// your agent in one request (queued on the open file).
func (c *PrereviewController) RunBenchmark(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	bm, err := bench.Load(c.LibraryDir, ctx.GetString("id"))
	if err != nil {
		return state, err
	}
	if len(bm.Cases) == 0 || len(bm.Checks) == 0 {
		return state, errors.New("a benchmark needs a case and a check to run")
	}
	rules := c.rulesByID()
	pins := bench.Pins{Def: bm.DefHash(), Checks: map[string]string{}, Cases: map[string]string{}}
	texts := map[string][]string{}
	for _, cs := range bm.Cases {
		p, err := bench.CasePath(c.LibraryDir, bm.ID, cs)
		if err != nil {
			return state, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return state, fmt.Errorf("case %s: %w", cs.Label, err)
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		texts[cs.ID], pins.Cases[cs.ID] = lines, checks.ContentHash(lines)
	}
	for _, k := range bm.Checks {
		if r := rules[k.ID]; r != nil && r.Err == "" {
			pins.Checks[k.ID] = c.checkPin(r)
		} else {
			pins.Checks[k.ID] = "missing"
		}
	}
	run, err := bench.StartRun(c.LibraryDir, bm.ID, pins)
	if err != nil {
		return state, err
	}
	var judges []bench.JudgeAsk
	for _, k := range bm.Checks {
		if r := rules[k.ID]; r != nil && r.Err == "" && r.IsJudge() {
			judges = append(judges, bench.JudgeAsk{ID: r.ID, Name: r.Name, Rubric: r.Rubric, RubricHash: r.RubricHash})
		}
	}
	go c.runBenchCheckers(bm, run, rules, texts)
	if len(judges) > 0 {
		if !state.AgentMode || state.SelectedFile == "" {
			state.Flash = "Ran the checkers; the judges need your agent (start prereview with --agent) and a file open"
		} else {
			var cases []bench.CaseAsk
			for _, cs := range bm.Cases {
				p, _ := bench.CasePath(c.LibraryDir, bm.ID, cs)
				cases = append(cases, bench.CaseAsk{ID: cs.ID, Label: cs.Label, Path: p, ContentHash: pins.Cases[cs.ID]})
			}
			if state, err = c.queueRequest(state, bench.JudgePrompt(bm, run, judges, cases)); err != nil {
				return state, err
			}
			state.Flash = fmt.Sprintf("Running “%s”: %d judge%s asked of your agent", bm.Name, len(judges), plural(len(judges)))
		}
	}
	c.applyBenchTab(&state)
	return state, nil
}

// benchCommandTimeout bounds a command checker in a benchmark run.
const benchCommandTimeout = 30 * time.Second

// runBenchCheckers records every checker's result on every case, then tells the
// tabs. A missing or broken check is an error (missing from the score, shown).
func (c *PrereviewController) runBenchCheckers(bm bench.Benchmark, run string, rules map[string]*checks.Rule, texts map[string][]string) {
	for _, cs := range bm.Cases {
		for _, k := range bm.Checks {
			r := rules[k.ID]
			rec := bench.Record{Run: run, Case: cs.ID, Check: k.ID}
			switch {
			case r == nil:
				rec.Status, rec.Critique = bench.Error, "no such check"
			case r.Err != "":
				rec.Status, rec.Critique = bench.Error, r.Err
			case r.IsJudge():
				continue // the agent answers it
			default:
				var res checks.Result
				if r.Async() {
					ctx, cancel := context.WithTimeout(context.Background(), benchCommandTimeout)
					res = checks.RunCommand(ctx, r, cs.Label, []byte(strings.Join(texts[cs.ID], "\n")+"\n"))
					cancel()
				} else {
					res = checks.RunInline(r, checks.NewDoc(cs.Label, texts[cs.ID]))
				}
				rec.Status = bench.Pass
				switch res.Status {
				case checks.StatusFail:
					rec.Status, rec.Critique = bench.Fail, fmt.Sprintf("%d finding%s", len(res.Findings), plural(len(res.Findings)))
				case checks.StatusError:
					rec.Status, rec.Critique = bench.Error, res.Err
				}
			}
			if err := bench.RecordResult(c.LibraryDir, bm.ID, rec); err != nil {
				slog.Warn("benchmark result", "err", err)
			}
		}
	}
	c.benchChanged() // this server's tabs are told now, not again on the tick
	c.broadcast("BenchmarksUpdated")
}

// benchFingerprint is every benchmark's definition and run log: a result from
// the agent, or an edit to a benchmark.yml, refreshes the tab.
func (c *PrereviewController) benchFingerprint() string {
	files, _ := filepath.Glob(filepath.Join(bench.Dir(c.LibraryDir), "*", "*"))
	var b strings.Builder
	for _, f := range files {
		if base := filepath.Base(f); base == bench.RunsFile || base == bench.DefFile {
			b.WriteString(f + statusFingerprint(f) + "|")
		}
	}
	return b.String()
}

// benchChanged reports whether any benchmark moved since the watcher last looked.
func (c *PrereviewController) benchChanged() bool {
	if c.LibraryDir == "" {
		return false
	}
	fp := c.benchFingerprint()
	c.calMu.Lock()
	defer c.calMu.Unlock()
	if fp == c.benchWatchFP {
		return false
	}
	c.benchWatchFP = fp
	return true
}

// BenchmarksUpdated refreshes the Benchmarks tab after a result or an edit.
func (c *PrereviewController) BenchmarksUpdated(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.applyBenchTab(&state)
	return state, nil
}
