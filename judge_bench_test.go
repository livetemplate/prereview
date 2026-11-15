package main

import (
	"testing"

	"github.com/livetemplate/prereview/internal/bench"
)

// A benchmark verdict is checked against the run's pins before it's written.
func TestJudgeBenchCase_ValidatesAgainstPins(t *testing.T) {
	lib := t.TempDir()
	t.Setenv("PREREVIEW_LIBRARY_DIR", lib)
	bm, err := bench.Create(lib, "Notes", []string{"reader-first"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := bench.AddCase(lib, bm.ID, "notes.md", "notes.md", []byte("# Notes\n"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := bench.StartRun(lib, bm.ID, bench.Pins{Checks: map[string]string{"reader-first": "rub1"}, Cases: map[string]string{c.ID: "con1"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][9]string{
		"stale content":  {bm.ID, run, c.ID, "reader-first", "con0", "rub1", "pass", "ok", ""},
		"stale rubric":   {bm.ID, run, c.ID, "reader-first", "con1", "rub0", "pass", "ok", ""},
		"unknown case":   {bm.ID, run, "nope", "reader-first", "con1", "rub1", "pass", "ok", ""},
		"unscored judge": {bm.ID, run, c.ID, "other", "con1", "rub1", "pass", "ok", ""},
		"bad run id":     {bm.ID, "../x", c.ID, "reader-first", "con1", "rub1", "pass", "ok", ""},
		"bad verdict":    {bm.ID, run, c.ID, "reader-first", "con1", "rub1", "maybe", "ok", ""},
	} {
		if err := judgeBenchCase(args[0], args[1], args[2], args[3], args[4], args[5], args[6], args[7], args[8]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := judgeBenchCase(bm.ID, run, c.ID, "reader-first", "con1", "rub1", "fail", "Buries it.", "m"); err != nil {
		t.Fatal(err)
	}
	if res, ok := bench.LoadRuns(lib, bm.ID)[0].Result(c.ID, "reader-first"); !ok || res.Status != bench.Fail || res.Model != "m" {
		t.Fatalf("recorded %+v %v", res, ok)
	}
}
