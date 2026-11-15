package library

import (
	"encoding/json"
	"path/filepath"
	"sort"
)

// Judge calibration (checks M5): your Agree/Disagree on a judge's verdicts are
// its labels. They're kept per user, across every review, in calibration.jsonl —
// each label with the verdict it answered and the rubric version that produced
// it, so a judge's agreement is counted for its current rubric only (a changed
// rubric is a different judge; its old labels are history).

// CalibrationFile is the per-user label log.
const CalibrationFile = "calibration.jsonl"

// The calibration rule: a judge counts (not advisory) once at least MinLabels of
// your answers exist for its current rubric and at least MinAgree of the last
// Window agree with it.
const (
	CalibrateMinLabels = 10
	CalibrateWindow    = 10
	CalibrateMinAgree  = 8
)

// Label is one answer to one verdict.
type Label struct {
	Judge       string `json:"judge"`
	RubricHash  string `json:"rubric_hash"`
	ContentHash string `json:"content_hash"`
	Verdict     string `json:"verdict"` // pass | fail: what the judge said
	Critique    string `json:"critique,omitempty"`
	Label       string `json:"label"` // agree | disagree; "" takes the answer back
	Store       string `json:"store"` // the review it was given in
	File        string `json:"file"`
	At          string `json:"at"`   // the verdict's time: which verdict this answers
	When        string `json:"when"` // when you answered
	Machine     string `json:"machine"`
}

// RecordLabel appends an answer (or its retraction) to the log.
func RecordLabel(dir string, l Label) error {
	l.When, l.Machine = Now(), Machine()
	return appendLocked(dir, CalibrationFile, l)
}

// Calibration is a judge's standing against its current rubric.
type Calibration struct {
	Agree, Total int // answers for the current rubric
	WindowAgree  int // of the last CalibrateWindow
	Calibrated   bool
	Earlier      int     // answers given to earlier rubric versions (history)
	OneSided     bool    // every labeled verdict was the same: it doesn't discriminate
	Flaky        bool    // the same content got both verdicts from the same rubric
	Disagreed    []Label // the latest CalibrateWindow you disagreed with
}

// LoadCalibration folds the log for every judge whose current rubric hash is in
// rubrics (judge id → hash): the latest answer per verdict, retractions removed.
func LoadCalibration(dir string, rubrics map[string]string) map[string]Calibration {
	type key struct{ store, file, judge, at string }
	latest := map[key]Label{}
	var order []key
	eachLine(filepath.Join(dir, CalibrationFile), func(b []byte) {
		var l Label
		if json.Unmarshal(b, &l) != nil || l.Judge == "" {
			return
		}
		k := key{l.Store, l.File, l.Judge, l.At}
		if _, seen := latest[k]; !seen {
			order = append(order, k)
		}
		latest[k] = l
	})
	byJudge := map[string][]Label{}
	for _, k := range order {
		if l := latest[k]; l.Label == "agree" || l.Label == "disagree" {
			byJudge[l.Judge] = append(byJudge[l.Judge], l)
		}
	}
	out := map[string]Calibration{}
	for judge, rubric := range rubrics {
		out[judge] = Calibrate(byJudge[judge], rubric)
	}
	return out
}

// Calibrate applies the rule to one judge's answers.
func Calibrate(ls []Label, rubric string) Calibration {
	var c Calibration
	var cur []Label
	for _, l := range ls {
		if l.RubricHash != rubric || rubric == "" {
			c.Earlier++
			continue
		}
		cur = append(cur, l)
	}
	sort.SliceStable(cur, func(i, j int) bool { return cur[i].When < cur[j].When })
	verdicts := map[string]bool{}
	byContent := map[string]map[string]bool{}
	for i, l := range cur {
		c.Total++
		if l.Label == "agree" {
			c.Agree++
			if i >= len(cur)-CalibrateWindow {
				c.WindowAgree++
			}
		} else {
			c.Disagreed = append(c.Disagreed, l)
		}
		verdicts[l.Verdict] = true
		if byContent[l.ContentHash] == nil {
			byContent[l.ContentHash] = map[string]bool{}
		}
		byContent[l.ContentHash][l.Verdict] = true
	}
	for _, vs := range byContent {
		if len(vs) > 1 {
			c.Flaky = true
		}
	}
	if n := len(c.Disagreed); n > CalibrateWindow { // the most recent are what a rubric fix needs
		c.Disagreed = c.Disagreed[n-CalibrateWindow:]
	}
	c.OneSided = c.Total >= 5 && len(verdicts) == 1
	c.Calibrated = c.Total >= CalibrateMinLabels && c.WindowAgree >= CalibrateMinAgree && !c.Flaky
	return c
}
