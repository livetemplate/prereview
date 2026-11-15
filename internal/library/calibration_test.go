package library

import (
	"fmt"
	"testing"
)

func labels(rubric string, agree ...bool) []Label {
	var out []Label
	for i, a := range agree {
		l := Label{Judge: "j", RubricHash: rubric, ContentHash: fmt.Sprint("c", i), Verdict: "fail", Label: "disagree",
			At: fmt.Sprint(i), When: fmt.Sprintf("2026-11-15T10:%02d:00Z", i)}
		if a {
			l.Label = "agree"
		}
		if i%2 == 0 {
			l.Verdict = "pass"
		}
		out = append(out, l)
	}
	return out
}

func rep(n int, v bool) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// The boundaries of the rule: 10 labels with 8 of the last 10 agreeing calibrates;
// 9 labels can't; 7 of 10 can't; one more disagreement drops it; a new rubric
// starts over (its old answers are history).
func TestCalibrate_Boundaries(t *testing.T) {
	eight := append(rep(8, true), false, false)
	for _, c := range []struct {
		name  string
		ls    []Label
		cal   bool
		agree int
	}{
		{"9 of 9", labels("r1", rep(9, true)...), false, 9},
		{"8 of 10", labels("r1", eight...), true, 8},
		{"7 of 10", labels("r1", append(rep(7, true), false, false, false)...), false, 7},
		{"8 of 10 then a disagreement", labels("r1", append(eight, false)...), false, 7},
	} {
		got := Calibrate(c.ls, "r1")
		if got.Calibrated != c.cal || got.WindowAgree != c.agree {
			t.Errorf("%s: calibrated=%v window=%d, want %v %d", c.name, got.Calibrated, got.WindowAgree, c.cal, c.agree)
		}
	}
	if got := Calibrate(labels("r1", eight...), "r2"); got.Calibrated || got.Total != 0 || got.Earlier != 10 {
		t.Errorf("a changed rubric kept its old labels: %+v", got)
	}
}

func TestCalibrate_Flags(t *testing.T) {
	same := labels("r1", rep(6, true)...)
	for i := range same {
		same[i].Verdict = "pass"
	}
	if c := Calibrate(same, "r1"); !c.OneSided {
		t.Error("a judge that passes everything isn't flagged as not discriminating")
	}
	flip := labels("r1", rep(10, true)...)
	flip[3].ContentHash = flip[2].ContentHash // same content, the other verdict
	if c := Calibrate(flip, "r1"); !c.Flaky || c.Calibrated {
		t.Errorf("a flaky judge: %+v", c)
	}
}

func TestLoadCalibration_RetractionAndLatestAnswer(t *testing.T) {
	dir := t.TempDir()
	put := func(at, label string) {
		if err := RecordLabel(dir, Label{Judge: "j", RubricHash: "r1", Verdict: "fail", Label: label, Store: "/s", File: "a.md", At: at}); err != nil {
			t.Fatal(err)
		}
	}
	put("v1", "agree")
	put("v1", "disagree") // changed your mind: the latest answer counts
	put("v2", "agree")
	put("v2", "") // took it back
	c := LoadCalibration(dir, map[string]string{"j": "r1", "unlabeled": "r9"})["j"]
	if c.Total != 1 || c.Agree != 0 || len(c.Disagreed) != 1 {
		t.Fatalf("calibration = %+v", c)
	}
}
