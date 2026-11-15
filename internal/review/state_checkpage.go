package review

import (
	"cmp"
	"fmt"
	"html/template"
	"slices"
	"strings"
	"time"

	"github.com/livetemplate/prereview/internal/checks"
)

// The Checks page (the Library frame's Checks tab): every check across every
// checked file, searchable, filtered by status / source / category / file, grouped
// by category. CheckPage builds the whole view model in one pass; it returns nil
// unless the page is open, because livetemplate evaluates every zero-arg method a
// template names on every render.

// CheckPage is the Checks page's view model.
type CheckPage struct {
	Query, Status, Source, Category, File string

	Chips []CheckChip // status chips, counted under every other filter
	// Kind chips (Checkers & judges / Checkers / Judges): shown only when the
	// library has a judge. Counted under every filter but the kind itself.
	Kind             string
	Checkers, Judges int
	Categories       []CheckCategory // the sidebar, counted under every other filter
	Groups           []CheckGroup    // what's listed
	// Folded is the passing groups shown as one line ("Clarity (2) and … all
	// pass"); ShowPassing is whether that line is offered as "hide" instead.
	Folded      []CheckGroup
	ShowPassing bool
	CanFold     bool // no filter narrows the page, so passing groups may fold
	Matches     int  // checks matching a search, for the "N checks match" line

	// ValeNotice / ValeTrust: the repo's Vale config isn't applied; shown in the
	// Vale category with the trust button.
	ValeNotice string
	ValeTrust  bool
	ShowVale   bool

	Files   int // files checked
	Skipped int // files past maxCheckFiles
	Checks  int // distinct checks
}

// CheckChip is one status chip.
type CheckChip struct {
	Value, Label string
	Count        int
	On           bool
}

// CheckCategory is one sidebar entry.
type CheckCategory struct {
	Name    string
	Note    string // "not trusted" on the Vale entry
	Count   int
	Failing int
	On      bool
}

// CheckGroup is one category's checks.
type CheckGroup struct {
	Name    string
	Failing int
	Errors  int
	Rows    []CheckRow
}

// CheckRow is one check on the page, aggregated across files.
type CheckRow struct {
	ID, Name, Description, Category, Source string
	Status                                  string // fail | error | running | pass | off
	Summary                                 string // "6 in 2 files", "passed", "timed out"…
	MatchNote                               string // "2 of 6 match" under a search
	Count                                   int    // findings (matching, under a search)
	Expanded                                bool
	RulePath                                string
	EditorURL                               template.URL
	Detail                                  string // "built-in · existence · files *.md"
	IsJudge                                 bool
	Agreement                               string // a judge's "agrees with you 5 of 7"
	// Expanded rows only:
	Verdicts []JudgeVerdictView // a judge's verdict per file
	ByFile   []CheckFileFindings
	Errors   []CheckFileError
	More     int           // findings past maxPageFindings
	History  *HistoryView // the rule file's versions (yours only), newest first
}

// CheckFileFindings is one file's findings under an expanded row.
type CheckFileFindings struct {
	Path     string
	Open     bool // the file on screen
	Count    int
	Findings []FindingView
}

// JudgeVerdictView is one file's verdict under an expanded judge row.
type JudgeVerdictView struct {
	Path, CheckID   string
	Open            bool   // the file on screen
	Status          string // pass | fail | stale | notrun | error
	Critique, Model string
	At              string // the verdict's time (labels key on it)
	Label           string // your answer: agree | disagree | ""
	CanShow         bool   // its quote was found: Show in file / Queue work
}

// CheckFileError is one file a check errored on.
type CheckFileError struct{ Path, Err string }

// SourceLabel is how the page names where a check lives.
func (r CheckRow) SourceLabel() string {
	switch r.Source {
	case checks.SourceBuiltin:
		return "built-in"
	case checks.SourceUser:
		return "yours"
	case checks.SourceLearned:
		return "learned"
	case checks.SourceVale, checks.SourceRepo:
		return "this repo"
	}
	return r.Source
}

// checkAgg is one check across files while CheckPage builds.
type checkAgg struct {
	first    checks.Result
	status   string
	count    int // findings kept
	total    int // findings before a search narrowed them
	files    []CheckFileFindings
	errs     []CheckFileError
	disabled bool
	nameHit  bool // the search matches the check itself, so every finding counts
	isJudge  bool
	judged   map[string]int // a judge's file count per status
	verdicts []JudgeVerdictView
}

// statusRank orders a check's worst status across files.
// A judge with no verdict (not run) or an outdated one ranks below a pass, so a
// judge that passed one file and wasn't run on another reads "passed".
var statusRank = map[string]int{checks.StatusNotRun: -2, checks.StatusStale: -1, checks.StatusPass: 0,
	checks.StatusRunning: 1, checks.StatusFail: 2, checks.StatusError: 3}

// CheckPage builds the Checks page, or nil when it's closed.
func (s PrereviewState) CheckPage() *CheckPage {
	if !s.ShowLibrary || !s.LibraryOnChecks() {
		return nil
	}
	p := &CheckPage{Query: s.ChecksQuery, Status: s.ChecksStatus, Source: s.ChecksSource, Kind: s.ChecksKind,
		Category: s.ChecksCategory, File: s.ChecksFile, ShowPassing: s.ShowPassingGroups}
	files := s.pageFiles()
	if s.ReviewChecks != nil {
		p.Skipped = max(0, s.ReviewChecks.Total-maxCheckFiles)
		p.ValeNotice, p.ValeTrust = s.ReviewChecks.ValeNotice, s.ReviewChecks.ValeTrust
	}
	p.Files = len(files)

	// Aggregate per check (library order) over the files the file chip allows,
	// keeping only what a search matches.
	q := strings.ToLower(strings.TrimSpace(s.ChecksQuery))
	var order []string
	aggs := map[string]*checkAgg{}
	for _, pc := range files {
		if p.File != "" && pc.Path != p.File {
			continue
		}
		for _, r := range pc.Results {
			a := aggs[r.ID]
			if a == nil {
				a = &checkAgg{first: r, status: r.Status, disabled: s.checkDisabled(r.ID),
					nameHit: q == "" || checkNameMatches(r, q), isJudge: r.Type == checks.TypeJudge}
				aggs[r.ID] = a
				order = append(order, r.ID)
			}
			if statusRank[r.Status] > statusRank[a.status] {
				a.status = r.Status
			}
			if r.Err != "" {
				a.errs = append(a.errs, CheckFileError{Path: pc.Path, Err: r.Err})
			}
			if a.isJudge {
				s.aggJudge(a, pc.Path, r, q)
				continue
			}
			a.total += len(r.Findings)
			// Only the expanded row lists findings; the rest only count them.
			listed := r.ID == s.ExpandedCheck
			ff := CheckFileFindings{Path: pc.Path, Open: pc.Path == s.SelectedFile}
			for i, f := range r.Findings {
				if a.nameHit || strings.Contains(strings.ToLower(f.Message), q) || strings.Contains(strings.ToLower(f.Match), q) {
					ff.Count++
					if listed {
						ff.Findings = append(ff.Findings, findingView(pc.Path, r, i))
					}
				}
			}
			if ff.Count > 0 {
				a.count += ff.Count
				a.files = append(a.files, ff)
			}
		}
	}
	p.Checks = len(order)

	// Base set: file chip + source + search. The chips count it under the
	// category filter, the sidebar under the status filter, the list under both.
	var base []*checkAgg
	for _, id := range order {
		a := aggs[id]
		if p.Source != "" && a.first.Source != p.Source {
			continue
		}
		if !a.nameHit && a.count == 0 {
			continue
		}
		if a.isJudge {
			p.Judges++
		} else {
			p.Checkers++
		}
		if p.Kind != "" && (p.Kind == checks.TypeJudge) != a.isJudge {
			continue
		}
		base = append(base, a)
	}
	statusOf := func(a *checkAgg) string {
		if a.disabled {
			return checkStatusOff
		}
		return a.status
	}
	chipCounts := map[string]int{}
	catCount, catFail := map[string]int{}, map[string]int{}
	for _, a := range base {
		st := statusOf(a)
		if p.Category == "" || a.first.Category == p.Category {
			chipCounts[""]++
			chipCounts[st]++
		}
		if p.Status == "" || st == p.Status {
			catCount[a.first.Category]++
			if st == checks.StatusFail || st == checks.StatusError {
				catFail[a.first.Category]++
			}
		}
	}
	for _, c := range []struct{ v, l string }{{"", "All"}, {checks.StatusFail, "Failing"},
		{checks.StatusError, "Errors"}, {checks.StatusPass, "Passing"}, {checkStatusOff, "Off"}} {
		p.Chips = append(p.Chips, CheckChip{Value: c.v, Label: c.l, Count: chipCounts[c.v], On: p.Status == c.v})
	}

	cats := make([]string, 0, len(catCount))
	for c := range catCount {
		cats = append(cats, c)
	}
	p.ShowVale = p.ValeNotice != "" && (p.Source == "" || p.Source == checks.SourceVale) && q == "" &&
		(p.Category == "" || p.Category == valeCategory)
	if p.ValeNotice != "" && !slices.Contains(cats, valeCategory) {
		cats = append(cats, valeCategory)
	}
	slices.SortFunc(cats, func(a, b string) int {
		return cmp.Or(cmp.Compare(categoryRank(a), categoryRank(b)), strings.Compare(a, b))
	})
	total, failing := 0, 0
	for _, c := range cats {
		total += catCount[c]
		failing += catFail[c]
	}
	p.Categories = append(p.Categories, CheckCategory{Name: "", Count: total, Failing: failing, On: p.Category == ""})
	for _, c := range cats {
		cc := CheckCategory{Name: c, Count: catCount[c], Failing: catFail[c], On: p.Category == c}
		if c == valeCategory && p.ValeNotice != "" {
			cc.Note = "not trusted"
			if !p.ValeTrust {
				cc.Note = "not installed"
			}
		}
		p.Categories = append(p.Categories, cc)
	}

	var tallies map[string][2]int
	if p.Judges > 0 {
		tallies = s.agreementTallies()
	}
	// Groups, in sidebar order.
	byCat := map[string]*CheckGroup{}
	for _, a := range base {
		st := statusOf(a)
		if (p.Status != "" && st != p.Status) || (p.Category != "" && a.first.Category != p.Category) {
			continue
		}
		g := byCat[a.first.Category]
		if g == nil {
			g = &CheckGroup{Name: a.first.Category}
			byCat[a.first.Category] = g
		}
		switch st {
		case checks.StatusFail:
			g.Failing++
		case checks.StatusError:
			g.Errors++
		}
		g.Rows = append(g.Rows, s.checkRow(a, st, tallies))
		p.Matches++
	}
	// Passing groups fold only on the unfiltered page: a search, chip, category
	// or source you picked asks to see what it matches.
	p.CanFold = q == "" && p.Status == "" && p.Category == "" && p.Source == ""
	fold := p.CanFold && !p.ShowPassing
	for _, c := range cats {
		g := byCat[c]
		if g == nil {
			continue
		}
		if fold && allPassing(g, s.ExpandedCheck) {
			p.Folded = append(p.Folded, *g)
			continue
		}
		p.Groups = append(p.Groups, *g)
	}
	return p
}

// valeCategory is the Vale config's own group (its summary row and notice);
// Vale's checks group by style (checks.ValeCategory: "Vale · Microsoft").
var valeCategory = checks.DefaultCategory(checks.SourceVale)

// categoryRank orders categories: rule-named ones first, then your rules, the
// repo's, and Vale's.
func categoryRank(c string) int {
	switch {
	case c == checks.DefaultCategory(checks.SourceUser):
		return 1
	case c == checks.DefaultCategory(checks.SourceRepo):
		return 2
	case checks.IsValeCategory(c):
		return 3
	}
	return 0
}

// allPassing reports a group that can fold into the "all pass" line: every check
// passed (an off, failing, errored or running one keeps it listed, so turning a
// check off never hides it) and none is the expanded row.
func allPassing(g *CheckGroup, expanded string) bool {
	for _, r := range g.Rows {
		if r.Status != checks.StatusPass || r.ID == expanded {
			return false
		}
	}
	return true
}

func checkNameMatches(r checks.Result, q string) bool {
	return strings.Contains(strings.ToLower(r.Name), q) || strings.Contains(strings.ToLower(r.Description), q) ||
		strings.Contains(strings.ToLower(r.Category), q)
}

func findingView(path string, r checks.Result, i int) FindingView {
	f := r.Findings[i]
	return FindingView{Path: path, CheckID: r.ID, CheckName: r.Name, Index: i, Line: f.EndLine,
		Label: findingLabel(f), Message: f.Message, Severity: f.Severity}
}

// checkRow renders one aggregated check; an expanded row carries its findings
// (capped) and errors.
func (s PrereviewState) checkRow(a *checkAgg, status string, tallies map[string][2]int) CheckRow {
	r := a.first
	row := CheckRow{ID: r.ID, Name: r.Name, Description: r.Description, Category: r.Category,
		Source: r.Source, Status: status, Count: a.count, Expanded: s.ExpandedCheck == r.ID,
		RulePath: r.Path}
	if a.isJudge {
		s.judgeRow(&row, a, tallies[row.ID])
		return row
	}
	switch status {
	case checkStatusOff:
		row.Summary = "off"
	case checks.StatusRunning:
		row.Summary = "running"
	case checks.StatusError:
		row.Summary = "error"
		if len(a.errs) > 0 && strings.Contains(a.errs[0].Err, "timed out") {
			row.Summary = "timed out"
		}
	case checks.StatusFail:
		row.Summary = fmt.Sprintf("%d in %d file%s", a.count, len(a.files), plural(len(a.files)))
	default:
		row.Summary = "passed"
	}
	if !a.nameHit && a.count < a.total {
		row.MatchNote = fmt.Sprintf("%d of %d match", a.count, a.total)
	}
	if row.RulePath != "" {
		row.EditorURL = editorURL(s.Editor, s.EditorTemplate, s.EditorSSHHost, row.RulePath, 1)
	}
	row.Detail = row.SourceLabel()
	if r.Type != "" && r.Type != checks.SourceVale {
		row.Detail += " · " + r.Type
	}
	if !row.Expanded {
		return row
	}
	row.Errors = a.errs
	if len(s.CheckHistory) > 0 {
		row.History = &HistoryView{ID: row.ID, Action: "restoreRuleVersion", Versions: s.CheckHistory}
	}
	shown := 0
	for _, ff := range a.files {
		if shown >= maxPageFindings {
			row.More += ff.Count
			continue
		}
		if keep := maxPageFindings - shown; len(ff.Findings) > keep {
			row.More += len(ff.Findings) - keep
			ff.Findings = ff.Findings[:keep]
		}
		shown += len(ff.Findings)
		row.ByFile = append(row.ByFile, ff)
	}
	return row
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// pageFiles is the files the page aggregates: the review-wide results, with the
// open file's entry taken from its displayed text (so the page and the inline
// marks agree) and added when it isn't a target (an unchanged file you opened).
func (s PrereviewState) pageFiles() []PathChecks {
	var files []PathChecks
	if s.ReviewChecks != nil {
		files = s.ReviewChecks.Files
	}
	if s.Checks == nil || s.SelectedFile == "" {
		return files
	}
	open := PathChecks{Path: s.SelectedFile, Results: s.Checks.Results}
	out := make([]PathChecks, 0, len(files)+1)
	seen := false
	for _, pc := range files {
		if pc.Path == open.Path {
			pc, seen = open, true
		}
		out = append(out, pc)
	}
	if !seen {
		out = append([]PathChecks{open}, out...)
	}
	return out
}

// CheckStrip is the file view's one-line bar while the open file has failing
// checks or judges to run: with none selected it lists the failing ones to pick
// (judges tagged) and offers Run judges; with a checker selected it's that
// check's finding stepper; with a judge selected it names it and Judge carries
// the critique card.
type CheckStrip struct {
	Selected bool
	ID, Name string
	Pos      int // 1-based finding under the cursor
	Count    int
	Failing  []CheckPick
	Judge    *JudgeVerdictView // the selected check is a judge: its card

	// Judges on this file (idle bar).
	JudgesRun     int  // enabled judges that apply: "Run judges (n)"
	JudgesStale   int  // of those, verdicts from an older file/rubric: "Re-run judges (n stale)"
	JudgeWaiting  int  // asked for and not answered yet
	JudgeTimedOut bool // asked over judgeWaitTimeout ago and still unanswered
	NeedsAgent    bool // judges exist but no agent is attached to answer
}

// CheckPick is one failing check offered on the bar.
type CheckPick struct {
	ID, Name string
	Count    int
	Judge    bool
}

// openJudgeRequest is when the open file's newest still-open "Run judges"
// request was queued (zero if none). Derived from the request comment — shared
// by every tab and surviving a reload — so the waiting state is the queue's own:
// it ends when verdicts land or the agent marks the request done.
func (s PrereviewState) openJudgeRequest() time.Time {
	var asked time.Time
	for _, c := range s.Comments {
		if c.File == s.SelectedFile && c.Kind == commentKindFile && !c.Resolved && !c.Processed &&
			checks.IsJudgeRequest(c.Body, s.SelectedFile) && c.Created.After(asked) {
			asked = c.Created
		}
	}
	return asked
}

// judgeWaitTimeout is how long "Asked your agent" waits for a judge's verdict
// before it reads "no answer yet — run again".
const judgeWaitTimeout = 10 * time.Minute

// CheckStrip builds the bar, or nil when there's nothing to show.
func (s PrereviewState) CheckStrip() *CheckStrip {
	if s.Checks == nil {
		return nil
	}
	if r, ok := s.checkResult(s.SelectedCheck); ok && r.Type == checks.TypeJudge && !s.checkDisabled(r.ID) &&
		(r.Status == checks.StatusPass || r.Status == checks.StatusFail || r.Status == checks.StatusStale) {
		v := s.verdictView(s.SelectedFile, r)
		return &CheckStrip{Selected: true, ID: r.ID, Name: r.Name, Count: len(r.Findings), Pos: 1, Judge: &v}
	}
	if r, ok := s.selectedFindings(); ok {
		n := len(r.Findings)
		return &CheckStrip{Selected: true, ID: r.ID, Name: r.Name, Count: n, Pos: min(max(s.FindingCursor, 0), n-1) + 1}
	}
	st := &CheckStrip{NeedsAgent: !s.AgentMode}
	asked := s.openJudgeRequest()
	unanswered := 0
	for _, r := range s.Checks.Results {
		if s.checkDisabled(r.ID) {
			continue
		}
		if r.Type != checks.TypeJudge {
			if r.Status == checks.StatusFail && len(r.Findings) > 0 {
				st.Failing = append(st.Failing, CheckPick{ID: r.ID, Name: r.Name, Count: len(r.Findings)})
			}
			continue
		}
		if r.Err != "" {
			continue
		}
		st.JudgesRun++
		switch r.Status {
		case checks.StatusFail:
			st.Failing = append(st.Failing, CheckPick{ID: r.ID, Name: r.Name, Judge: true})
		case checks.StatusStale:
			st.JudgesStale++
		}
		if !asked.IsZero() {
			if at, err := time.Parse(time.RFC3339Nano, r.JudgedAt); err != nil || at.Before(asked) {
				unanswered++
			}
		}
	}
	if unanswered > 0 {
		if time.Since(asked) < judgeWaitTimeout {
			st.JudgeWaiting = unanswered
		} else {
			st.JudgeTimedOut = true
		}
	}
	if len(st.Failing) == 0 && st.JudgesRun == 0 {
		return nil
	}
	return st
}

// aggJudge folds one file's judge result into its row: counts per status, the
// critique as searchable text, and (for the expanded row) the verdict view.
func (s PrereviewState) aggJudge(a *checkAgg, path string, r checks.Result, q string) {
	if a.judged == nil {
		a.judged = map[string]int{}
	}
	a.judged[r.Status]++
	if a.nameHit || (r.Critique != "" && strings.Contains(strings.ToLower(r.Critique), q)) {
		a.count++
	}
	if r.ID != s.ExpandedCheck {
		return
	}
	a.verdicts = append(a.verdicts, s.verdictView(path, r))
}

// verdictView is one judge result as the page row and the file's card show it.
func (s PrereviewState) verdictView(path string, r checks.Result) JudgeVerdictView {
	return JudgeVerdictView{Path: path, CheckID: r.ID, Open: path == s.SelectedFile,
		Status: r.Status, Critique: r.Critique, Model: r.Model, At: r.JudgedAt,
		Label: s.JudgeLabels[labelKey(path, r.ID, r.JudgedAt)], CanShow: len(r.Findings) > 0}
}

// judgeRow fills a judge's summary, detail and agreement.
func (s PrereviewState) judgeRow(row *CheckRow, a *checkAgg, tally [2]int) {
	row.IsJudge = true
	n := func(st string) int { return a.judged[st] }
	files := n(checks.StatusPass) + n(checks.StatusFail) + n(checks.StatusStale) + n(checks.StatusNotRun)
	switch row.Status {
	case checkStatusOff:
		row.Summary = "off"
	case checks.StatusError:
		row.Summary = "error"
	case checks.StatusFail:
		row.Summary = fmt.Sprintf("fails %d of %d file%s", n(checks.StatusFail), files, plural(files))
	case checks.StatusPass:
		row.Summary = fmt.Sprintf("passed in %d file%s", n(checks.StatusPass), plural(n(checks.StatusPass)))
		if k := n(checks.StatusNotRun) + n(checks.StatusStale); k > 0 {
			row.Summary += fmt.Sprintf(" · %d not run", k)
		}
	case checks.StatusStale:
		row.Summary = "out of date"
	default:
		row.Summary = "not run"
	}
	row.Detail = "judge · advisory · " + row.SourceLabel()
	row.Agreement = agreementText(tally)
	if row.RulePath != "" {
		row.EditorURL = editorURL(s.Editor, s.EditorTemplate, s.EditorSSHHost, row.RulePath, 1)
	}
	row.Errors = a.errs
	row.Verdicts = a.verdicts
}

// agreementTallies counts your answers per judge — [agree, total] — once per
// page build (labels are keyed file\x00judge\x00at).
func (s PrereviewState) agreementTallies() map[string][2]int {
	out := map[string][2]int{}
	for k, label := range s.JudgeLabels {
		parts := strings.SplitN(k, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		t := out[parts[1]]
		if label == LabelAgree {
			t[0]++
		}
		t[1]++
		out[parts[1]] = t
	}
	return out
}

// agreementText is how often you agreed with a judge's verdicts so far — its
// calibration (M5 scores it; M2 shows it).
func agreementText(t [2]int) string {
	if t[1] == 0 {
		return "no answers from you yet"
	}
	return fmt.Sprintf("agrees with you %d of %d", t[0], t[1])
}
