package review

import (
	"path/filepath"

	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
)

// Judge calibration (checks M5). Your Agree/Disagree on a verdict is recorded in
// the review's store (which answer this tab shows) and in the per-user
// calibration log, with the verdict and its rubric version — so a judge's
// agreement spans every review. A judge counts in a file's Checks n/m only once
// it's calibrated; until then it's advisory: shown, never counted.

// calibration is every loaded judge's standing against its current rubric,
// re-folded only when the label log or the rules change. Shared read-only.
func (c *PrereviewController) calibration() map[string]library.Calibration {
	if c.LibraryDir == "" {
		return nil
	}
	r := c.runner()
	r.mu.Lock()
	rubrics := map[string]string{}
	for _, rule := range r.lib.Rules {
		if rule.IsJudge() && rule.Err == "" {
			rubrics[rule.ID] = rule.RubricHash
		}
	}
	fp := statusFingerprint(c.calibrationPath()) + "|" + r.lib.Hash + "|" + r.stamp
	r.mu.Unlock()
	c.calMu.Lock()
	defer c.calMu.Unlock()
	if c.calCache == nil || fp != c.calFP {
		c.calCache, c.calFP = library.LoadCalibration(c.LibraryDir, rubrics), fp
	}
	return c.calCache
}

func (c *PrereviewController) calibrationPath() string {
	return filepath.Join(c.LibraryDir, library.CalibrationFile)
}

// calibrationChanged reports whether the label log moved since the watcher last
// looked (an answer in any review), so the watcher re-counts every tab once.
func (c *PrereviewController) calibrationChanged() bool {
	if c.LibraryDir == "" {
		return false
	}
	fp := statusFingerprint(c.calibrationPath())
	c.calMu.Lock()
	defer c.calMu.Unlock()
	if fp == c.calWatchFP {
		return false
	}
	c.calWatchFP = fp
	return true
}

// judgeCalibrated reports whether a judge counts in n/m (it's calibrated).
func (s PrereviewState) judgeCalibrated(id string) bool { return s.JudgeCal[id].Calibrated }

// isAdvisoryJudge reports a judge result that is shown but not counted.
func (s PrereviewState) isAdvisoryJudge(r checks.Result) bool {
	return r.Type == checks.TypeJudge && !s.judgeCalibrated(r.ID)
}
