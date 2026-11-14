package review

import (
	"html/template"
	"slices"

	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/checks"
)

// findingMarkClass is the <mark> class on a selected check's finding span; a
// finding inside a commented phrase carries both classes (gitdiff.MarkRanges).
const findingMarkClass = "check-finding"

// CheckSummary is the file header's "n/m passed": enabled checks only. A running
// check counts toward Total but not Passed until it lands.
type CheckSummary struct {
	Passed  int
	Total   int
	Running int
	Failing bool // any enabled check failed or errored
}

// FindingView is one finding as the panel and the inline card render it.
type FindingView struct {
	CheckID   string
	CheckName string
	Index     int // position in the check's findings (QueueFinding's index)
	Line      int
	Label     string // "L12", "L12:4-9" or "L12-14"
	Message   string
	Severity  string
}

// CheckRow is one check in the panel.
type CheckRow struct {
	ID          string
	Name        string
	Description string
	Source      string
	Status      string
	Err         string
	Count       int
	Disabled    bool
	Selected    bool
	RulePath    string       // the rule file on disk ("" for a built-in or Vale's own rules)
	EditorURL   template.URL // "Open in editor" for RulePath; "" when no editor is set
	Findings    []FindingView
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
	for _, r := range s.Checks.Results {
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
		if s.checkDisabled(r.ID) {
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

// CheckRows lists every check on the displayed file for the panel, the selected
// one carrying its findings.
func (s PrereviewState) CheckRows() []CheckRow {
	if s.Checks == nil {
		return nil
	}
	out := make([]CheckRow, 0, len(s.Checks.Results))
	for _, r := range s.Checks.Results {
		row := CheckRow{ID: r.ID, Name: r.Name, Description: r.Description,
			Source: r.Source, Status: r.Status, Err: r.Err, Count: len(r.Findings),
			Disabled: s.checkDisabled(r.ID), RulePath: r.Path}
		if row.RulePath != "" {
			row.EditorURL = editorURL(s.Editor, s.EditorTemplate, s.EditorSSHHost, row.RulePath, 1)
		}
		if r.ID == s.SelectedCheck && !row.Disabled {
			row.Selected = true
			row.Findings = findingViews(r)
		}
		out = append(out, row)
	}
	return out
}

func findingViews(r checks.Result) []FindingView {
	out := make([]FindingView, len(r.Findings))
	for i, f := range r.Findings {
		out[i] = FindingView{CheckID: r.ID, CheckName: r.Name, Index: i, Line: f.EndLine,
			Label: findingLabel(f), Message: f.Message, Severity: f.Severity}
	}
	return out
}

// findingLabel formats a finding's span as a text comment's badge does, so the
// panel row and the comment it queues read the same.
func findingLabel(f checks.Finding) string {
	return Comment{Kind: commentKindText, FromLine: f.Line, ToLine: f.EndLine, FromCol: f.FromCol, ToCol: f.ToCol}.LineSpan()
}

// finding looks up one finding of a check on the displayed file.
func (s PrereviewState) finding(id string, idx int) (checks.Result, checks.Finding, bool) {
	r, ok := s.checkResult(id)
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
	if !ok {
		return nil
	}
	out := map[string][]FindingView{}
	for _, fv := range findingViews(r) {
		k := rowKeysFor(fv.Line, "new")[0]
		out[k] = append(out[k], fv)
	}
	return out
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
