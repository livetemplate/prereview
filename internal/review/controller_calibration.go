package review

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

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
	r.mu.Unlock()
	fp := statusFingerprint(c.calibrationPath()) + "|" + fmt.Sprint(rubrics)
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

// calibrated is the set of judges that count in n/m.
func (c *PrereviewController) calibrated() map[string]bool {
	out := map[string]bool{}
	for id, cal := range c.calibration() {
		if cal.Calibrated {
			out[id] = true
		}
	}
	return out
}

// calibrationChanged reports whether any judge's standing moved since the
// watcher last looked (an answer in any review, or a rubric edit), keyed on the
// standings rather than the log file — so another review's answer that changes
// nothing here re-counts no tab.
func (c *PrereviewController) calibrationChanged() bool {
	if c.LibraryDir == "" {
		return false
	}
	cal := c.calibration()
	ids := slices.Sorted(maps.Keys(cal))
	var key strings.Builder
	for _, id := range ids {
		s := cal[id]
		fmt.Fprintf(&key, "%s:%v:%d:%d:%d|", id, s.Calibrated, s.Agree, s.Total, len(s.Disagreed))
	}
	return c.watchMoved(&c.calWatchFP, key.String())
}

// watchMoved records fp as the watcher's last look at one area and reports
// whether it changed (every area's "fan it out once" check).
func (c *PrereviewController) watchMoved(last *string, fp string) bool {
	c.watchMu.Lock()
	defer c.watchMu.Unlock()
	if fp == *last {
		return false
	}
	*last = fp
	return true
}
