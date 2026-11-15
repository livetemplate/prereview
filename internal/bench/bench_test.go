package bench

import (
	"math"
	"strings"
	"testing"
)

func bm2x2() Benchmark {
	return Benchmark{ID: "b", Cases: []Case{{ID: "a"}, {ID: "b"}},
		Checks: []CheckSpec{{ID: "filler"}, {ID: "reader", Weight: 3}}}
}

func run(id, at string, pins Pins, model string, cells map[string]string) Run {
	r := Run{ID: id, At: at, Pins: pins, Model: model, Results: map[string]Result{}}
	for k, st := range cells {
		c, chk, _ := strings.Cut(k, "/")
		r.Results[resultKey(c, chk)] = Result{Status: st}
	}
	return r
}

func TestScore_WeightsErroredAndPending(t *testing.T) {
	bm := bm2x2()
	full := run("r1", "1", Pins{}, "", map[string]string{"a/filler": Pass, "a/reader": Fail, "b/filler": Pass, "b/reader": Pass})
	if s := bm.Score(full); !s.Complete || s.Scored != 4 || math.Abs(s.Value-5.0/8) > 1e-9 {
		t.Fatalf("weighted score = %+v, want 5/8", s)
	}
	errored := run("r2", "2", Pins{}, "", map[string]string{"a/filler": Pass, "a/reader": Error, "b/filler": Pass, "b/reader": Pass})
	if s := bm.Score(errored); s.Errored != 1 || s.Scored != 3 || math.Abs(s.Value-1) > 1e-9 {
		t.Fatalf("an errored cell must be missing, not a fail: %+v", s)
	}
	partial := run("r3", "3", Pins{}, "", map[string]string{"a/filler": Pass})
	if s := bm.Score(partial); s.Complete || s.Pending != 3 {
		t.Fatalf("partial run = %+v", s)
	}
}

func TestTrend_ComparableCompleteRunsOnly(t *testing.T) {
	bm := bm2x2()
	p1 := Pins{Def: "d", Checks: map[string]string{"filler": "h1", "reader": "r1"}, Cases: map[string]string{"a": "x", "b": "y"}}
	p2 := Pins{Def: "d", Checks: map[string]string{"filler": "h1", "reader": "r2"}, Cases: map[string]string{"a": "x", "b": "y"}} // rubric edited
	all := func(st string) map[string]string {
		return map[string]string{"a/filler": st, "a/reader": st, "b/filler": st, "b/reader": st}
	}
	runs := []Run{ // newest first
		run("r5", "5", p1, "m", map[string]string{"a/filler": Pass}), // partial: ignored
		run("r4", "4", p1, "m", all(Pass)),
		run("r3", "3", p2, "m", all(Fail)),      // not comparable: another rubric
		run("r2", "2", p1, "other", all(Fail)),  // not comparable: another model
		run("r1", "1", p1, "m", all(Fail)),
		run("r0", "0", p1, "m", all(Pass)),
	}
	tr := bm.Trend(runs)
	if tr.Latest != "r4" || tr.N != 3 || math.Abs(tr.Mean-2.0/3) > 1e-9 || tr.Std == 0 {
		t.Fatalf("trend = %+v, want r4 anchoring {r4, r1, r0}", tr)
	}
	if Comparable(runs[1], runs[2]) || Comparable(runs[1], runs[3]) || !Comparable(runs[1], runs[4]) {
		t.Fatal("comparability by pins and model")
	}
}

func TestBenchmarkFiles(t *testing.T) {
	lib := t.TempDir()
	bm, err := Create(lib, "Release notes", []string{"filler", "reader"})
	if err != nil || bm.ID != "release-notes" {
		t.Fatalf("create = %+v %v", bm, err)
	}
	if _, err := AddCase(lib, bm.ID, "notes.md", "/repo/notes.md", []byte("\x00\x01binary")); err == nil {
		t.Fatal("a binary case was accepted")
	}
	c, err := AddCase(lib, bm.ID, "notes.md", "/repo/notes.md", []byte("# Notes\nBasically done.\n"))
	if err != nil || c.File != "cases/notes-md.md" {
		t.Fatalf("add case = %+v %v", c, err)
	}
	again, _ := AddCase(lib, bm.ID, "notes.md", "/repo/notes.md", []byte("v2\n"))
	if again.ID == c.ID {
		t.Fatal("a second snapshot of the same file overwrote the first")
	}
	got, err := Load(lib, bm.ID)
	if err != nil || len(got.Cases) != 2 || got.DefHash() == bm.DefHash() {
		t.Fatalf("load = %+v %v (adding a case must change the def pin)", got, err)
	}
	run, err := StartRun(lib, bm.ID, Pins{Def: got.DefHash()})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Record{{Run: "../x", Case: c.ID, Status: Pass}, {Run: run, Case: "../../etc", Status: Pass}, {Run: run, Case: c.ID, Status: "maybe"}} {
		if err := RecordResult(lib, bm.ID, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := RecordResult(lib, bm.ID, Record{Run: run, Case: c.ID, Check: "reader", Judge: true, Status: Fail, Model: "m"}); err != nil {
		t.Fatal(err)
	}
	runs := LoadRuns(lib, bm.ID)
	if len(runs) != 1 || runs[0].Model != "m" || runs[0].Results[resultKey(c.ID, "reader")].Status != Fail {
		t.Fatalf("runs = %+v", runs)
	}
	if _, err := Load(lib, "../etc"); err == nil {
		t.Fatal("loaded a benchmark outside the folder")
	}
}
