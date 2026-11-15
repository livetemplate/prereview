package bench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/livetemplate/prereview/internal/library"
)

// Result statuses inside a run. A result not recorded yet is pending.
const (
	Pass  = "pass"
	Fail  = "fail"
	Error = "error" // counts as missing, not as a fail
)

// Pins are what a run used: the benchmark definition, each check's version and
// each case's text. Runs with different pins (or judged by a different model)
// aren't comparable.
type Pins struct {
	Def    string            `json:"def"`
	Checks map[string]string `json:"checks"` // check id → its rule/rubric hash
	Cases  map[string]string `json:"cases"`  // case id → its content hash
}

// Record is one line of runs.jsonl: a run's start (with its pins) or one result.
type Record struct {
	Run      string `json:"run"`
	At       string `json:"at"`
	Kind     string `json:"kind"` // start | result
	Pins     *Pins  `json:"pins,omitempty"`
	Case     string `json:"case,omitempty"`
	Check    string `json:"check,omitempty"`
	Judge    bool   `json:"judge,omitempty"`
	Status   string `json:"status,omitempty"`
	Critique string `json:"critique,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Result is one check on one case in a run.
type Result struct {
	Status, Critique, Model string
	Judge                   bool
}

// Run is one run, folded.
type Run struct {
	ID, At  string
	Pins    Pins
	Results map[string]Result // resultKey(case, check)
	Model   string            // the judging model the agent reported
}

func resultKey(caseID, check string) string { return caseID + "\x00" + check }

// Result returns a run's result for one cell.
func (r Run) Result(caseID, check string) (Result, bool) {
	res, ok := r.Results[resultKey(caseID, check)]
	return res, ok
}

// StartRun records a new run with its pins and returns its id.
func StartRun(lib, id string, pins Pins) (string, error) {
	run := fmt.Sprintf("r%x", time.Now().UnixNano())
	return run, appendRecord(lib, id, Record{Run: run, Kind: "start", Pins: &pins})
}

// RecordResult appends one result to a run (the last one recorded for a cell wins).
func RecordResult(lib, id string, rec Record) error {
	if !ValidID(rec.Run) || !ValidID(rec.Case) {
		return fmt.Errorf("benchmark result: bad run or case id")
	}
	switch rec.Status {
	case Pass, Fail, Error:
	default:
		return fmt.Errorf("benchmark result: status %q (want pass, fail or error)", rec.Status)
	}
	rec.Kind = "result"
	return appendRecord(lib, id, rec)
}

func appendRecord(lib, id string, rec Record) error {
	p, err := path(lib, id, RunsFile)
	if err != nil {
		return err
	}
	rec.At = library.Now()
	unlock, err := library.Lock(lib)
	if err != nil {
		return err
	}
	defer unlock()
	return library.Append(p, rec)
}

// RunsPath is a benchmark's run log ("" for a bad id).
func RunsPath(lib, id string) string {
	p, _ := path(lib, id, RunsFile)
	return p
}

// LoadRuns folds a benchmark's run log, newest first.
func LoadRuns(lib, id string) []Run {
	p, err := path(lib, id, RunsFile)
	if err != nil {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	byID := map[string]*Run{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var rec Record
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Run == "" {
			continue
		}
		r := byID[rec.Run]
		switch {
		case rec.Kind == "start" && rec.Pins != nil && r == nil:
			byID[rec.Run] = &Run{ID: rec.Run, At: rec.At, Pins: *rec.Pins, Results: map[string]Result{}}
		case rec.Kind == "result" && r != nil:
			r.Results[resultKey(rec.Case, rec.Check)] = Result{Status: rec.Status, Critique: rec.Critique, Model: rec.Model, Judge: rec.Judge}
			if rec.Judge && rec.Model != "" {
				r.Model = rec.Model
			}
		}
	}
	out := make([]Run, 0, len(byID))
	for _, r := range byID {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// Score is a run's weighted pass rate over the cells that have a pass or fail;
// pending (not answered yet) and errored cells are missing, not fails.
type Score struct {
	Value            float64
	Scored           int
	Pending, Errored int
	Complete         bool // nothing pending
}

// Score scores a run against the benchmark's checks and cases.
func (bm Benchmark) Score(r Run) Score {
	var s Score
	var got, total float64
	for _, c := range bm.Cases {
		for _, k := range bm.Checks {
			res, ok := r.Result(c.ID, k.ID)
			switch {
			case !ok:
				s.Pending++
			case res.Status == Error:
				s.Errored++
			default:
				s.Scored++
				total += k.W()
				if res.Status == Pass {
					got += k.W()
				}
			}
		}
	}
	if total > 0 {
		s.Value = got / total
	}
	s.Complete = s.Pending == 0
	return s
}

// Comparable reports whether two runs used the same benchmark, check versions,
// case texts and judging model.
func Comparable(a, b Run) bool {
	return a.Pins.Def == b.Pins.Def && maps.Equal(a.Pins.Checks, b.Pins.Checks) && maps.Equal(a.Pins.Cases, b.Pins.Cases) && a.Model == b.Model
}

// TrendRuns is how many recent comparable runs the mean ± stddev covers.
const TrendRuns = 3

// Trend is the benchmark's score: the mean ± stddev of the latest complete runs
// comparable with the newest complete one (up to TrendRuns). N < 2 has no ±.
type Trend struct {
	Mean, Std float64
	N         int
	Latest    string // the newest complete run the trend is anchored to
}

// Trend scores runs (newest first).
func (bm Benchmark) Trend(runs []Run) Trend {
	var t Trend
	var anchor *Run
	var xs []float64
	for i := range runs {
		r := runs[i]
		s := bm.Score(r)
		if !s.Complete || s.Scored == 0 {
			continue
		}
		if anchor == nil {
			anchor, t.Latest = &runs[i], r.ID
		}
		if !Comparable(*anchor, r) {
			continue
		}
		xs = append(xs, s.Value)
		if len(xs) == TrendRuns {
			break
		}
	}
	t.N = len(xs)
	t.Mean, t.Std = meanStd(xs)
	return t
}

// DefPath is a benchmark's definition file ("" for a bad id).
func DefPath(lib, id string) string {
	p, _ := path(lib, id, DefFile)
	return p
}

// FolderOf is a benchmark's folder.
func FolderOf(lib, id string) string { return filepath.Join(Dir(lib), id) }
