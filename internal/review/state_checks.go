package review

import (
	"fmt"
	"slices"
	"strings"

	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/checks"
)

// findingMarkClass is the <mark> class on a selected check's finding span; a
// finding inside a commented phrase carries both classes (gitdiff.MarkRanges).
const findingMarkClass = "check-finding"

// CheckSummary is the file header's "n/m passed": ONE count over the enabled
// checkers and the calibrated judges (M5). A running check counts toward Total
// but not Passed until it lands; a judge counts only once calibrated and with a
// current verdict (not run or stale counts in neither number), and then a
// failing judge marks the button failing. An advisory judge is shown, never
// counted. Nothing gates on any of it.
type CheckSummary struct {
	Passed  int
	Total   int
	Running int
	Failing bool // any enabled check failed or errored
}

// checkStatusOff is the Checks page's status for a check you turned off.
const checkStatusOff = "off"

// maxPageFindings caps the findings an expanded row lists; the rest are counted.
const maxPageFindings = 100

// FindingView is one finding as the Checks page and the inline card render it.
type FindingView struct {
	Path      string // the finding's file
	CheckID   string
	CheckName string
	Index     int // position in the check's findings for this file (QueueFinding's index)
	Line      int
	Label     string // "L12", "L12:4-9" or "L12-14"
	Message   string
	Severity  string
	Queued    bool // already queued as a comment (and not resolved): its comment card shows it
}

// ShowChecks gates the file-header button: the file has checks to report.
func (s PrereviewState) ShowChecks() bool {
	return s.Checks != nil && (len(s.Checks.Results) > 0 || s.Checks.ValeNotice != "")
}

func (s PrereviewState) checkDisabled(id string) bool {
	return slices.Contains(s.DisabledChecks, id)
}

// checkResult finds a result by check ID on the displayed file.
func (s PrereviewState) checkResult(id string) (checks.Result, bool) {
	if s.Checks == nil {
		return checks.Result{}, false
	}
	return resultFor(s.Checks.Results, id)
}

// resultFor finds a check's result among one file's results.
func resultFor(results []checks.Result, id string) (checks.Result, bool) {
	for _, r := range results {
		if r.ID == id {
			return r, true
		}
	}
	return checks.Result{}, false
}

// CheckSummary counts the enabled checks for the header button.
func (s PrereviewState) CheckSummary() CheckSummary {
	var cs CheckSummary
	if s.Checks == nil {
		return cs
	}
	for _, r := range s.Checks.Results {
		if s.checkDisabled(r.ID) || r.Status == checks.StatusNotRun || r.Status == checks.StatusStale || r.Advisory {
			continue
		}
		cs.Total++
		switch r.Status {
		case checks.StatusPass:
			cs.Passed++
		case checks.StatusRunning:
			cs.Running++
		default:
			cs.Failing = true
		}
	}
	return cs
}

// findingLabel formats a finding's span as a text comment's badge does, so the
// page row and the comment it queues read the same.
func findingLabel(f checks.Finding) string {
	return Comment{Kind: commentKindText, FromLine: f.Line, ToLine: f.EndLine, FromCol: f.FromCol, ToCol: f.ToCol}.LineSpan()
}

// finding looks up one finding of a check on the displayed file.
func (s PrereviewState) finding(id string, idx int) (checks.Result, checks.Finding, bool) {
	r, ok := s.checkResult(id)
	return findingAt(r, ok, idx)
}

// findingIn looks up one finding of a check in any checked file, from the same
// results the Checks page lists (pageFiles: the open file's displayed text, the
// others from disk), so an index always means the finding on screen.
func (s PrereviewState) findingIn(path, id string, idx int) (checks.Result, checks.Finding, bool) {
	for _, pc := range s.pageFiles() {
		if pc.Path == path {
			r, ok := resultFor(pc.Results, id)
			return findingAt(r, ok, idx)
		}
	}
	return checks.Result{}, checks.Finding{}, false
}

func findingAt(r checks.Result, ok bool, idx int) (checks.Result, checks.Finding, bool) {
	if !ok || idx < 0 || idx >= len(r.Findings) {
		return checks.Result{}, checks.Finding{}, false
	}
	return r, r.Findings[idx], true
}

// checkShowsSource reports that a rendered Markdown/HTML file should show its
// source lines: a selected, enabled check has findings to draw there. Derived,
// not stored, so it follows the selection and the file with nothing to clear.
func (s PrereviewState) checkShowsSource() bool {
	_, ok := s.selectedFindings()
	return ok
}

// selectedFindings is the selected, enabled check's findings, or nil. Only these
// draw inline: one check at a time keeps a noisy rule from unfolding the file.
func (s PrereviewState) selectedFindings() (checks.Result, bool) {
	if s.SelectedCheck == "" || s.checkDisabled(s.SelectedCheck) {
		return checks.Result{}, false
	}
	r, ok := s.checkResult(s.SelectedCheck)
	return r, ok && len(r.Findings) > 0
}

// FindingsByRow keys the selected check's finding cards by the row they render
// under ("<end line>-new", the diff view's $rowkey). Findings are on the new side.
func (s PrereviewState) FindingsByRow() map[string][]FindingView {
	r, ok := s.selectedFindings()
	if !ok || r.Type == checks.TypeJudge {
		return nil // a judge's critique is on its card above the file; its quote is only marked
	}
	out := map[string][]FindingView{}
	queued := s.queuedFindings()
	for i := range r.Findings {
		k := findingKey(s.SelectedFile, r.Findings[i], r.Name)
		if queued[k] || slices.Contains(s.HiddenFindings, k) {
			continue // queued: its comment card is right there; hidden: folded this session
		}
		fv := findingView(s.SelectedFile, r, i)
		row := rowKeysFor(fv.Line, "new")[0]
		out[row] = append(out[row], fv)
	}
	return out
}

// queuedFindings is every finding already queued as a comment that is still
// open: the comment Queue made carries the finding's file and exact span, and
// its body ends "(check: <name>)" (or "(judge: <name>)").
func (s PrereviewState) queuedFindings() map[string]bool {
	out := map[string]bool{}
	for _, c := range s.Comments {
		if c.Resolved || c.Kind != commentKindText || !strings.HasSuffix(c.Body, ")") {
			continue
		}
		i := strings.LastIndex(c.Body, " (")
		if i < 0 {
			continue
		}
		kind, name, ok := strings.Cut(strings.TrimSuffix(c.Body[i+2:], ")"), ": ")
		if !ok || (kind != "check" && kind != "judge") {
			continue
		}
		out[findingKey(c.File, checks.Finding{Line: c.FromLine, EndLine: c.ToLine, FromCol: c.FromCol, ToCol: c.ToCol}, name)] = true
	}
	return out
}

// findingKey identifies a finding by file, span and check name.
func findingKey(path string, f checks.Finding, check string) string {
	return fmt.Sprintf("%s\x00%d:%d:%d:%d\x00%s", path, f.Line, f.EndLine, f.FromCol, f.ToCol, check)
}

// findingLines reports the new-side lines a selected finding covers, so
// VisibleLines keeps them out of folds.
func (s PrereviewState) findingLines() map[int]bool {
	r, ok := s.selectedFindings()
	if !ok {
		return nil
	}
	out := map[int]bool{}
	for _, f := range r.Findings {
		for ln := f.Line; ln <= f.EndLine; ln++ {
			out[ln] = true
		}
	}
	return out
}

// findingMarksByLine indexes the selected check's spans by new-side line, in the
// coordinates textMarksForLine uses (multi-line spans run to the line's end).
// Built once per render by LineDisplay, not re-scanned per line.
func (s PrereviewState) findingMarksByLine() map[int][]gitdiff.ColRange {
	r, ok := s.selectedFindings()
	if !ok {
		return nil
	}
	out := map[int][]gitdiff.ColRange{}
	for _, f := range r.Findings {
		for ln := f.Line; ln <= f.EndLine; ln++ {
			from, to, _ := spanOnLine(ln, f.Line, f.FromCol, f.EndLine, f.ToCol)
			if to <= from { // a zero-width finding: mark the whole line so it's visible
				from, to = 0, lineEndSentinel
			}
			out[ln] = append(out[ln], gitdiff.ColRange{From: from, To: to, Class: findingMarkClass})
		}
	}
	return out
}
