package review

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/livetemplate/livetemplate"

	"github.com/livetemplate/prereview/internal/checks"
)

// Ignoring a finding (dismissing it as fine — a false positive): the
// reviewer's call on one occurrence, kept in the review's store
// (check-dismissals.jsonl, server-owned, append-only). A dismissal is keyed by
// the file, the check, the flagged text and its line's text, so it survives
// the line moving and lapses when that line is edited. A dismissed finding
// stops counting: a check with nothing left passes.
//
// A judge's failing verdict is ignored the same way, keyed by the file, the
// judge and the hashes of the text and rubric it judged: it stays ignored
// across re-runs until either changes. It reads as a pass, which leaves
// calibration alone (labels record the verdict itself).

// CheckDismissalsFileName is the review store's dismissal log.
const CheckDismissalsFileName = "check-dismissals.jsonl"

// Dismissal is one dismissal (or its undoing).
type Dismissal struct {
	File  string `json:"file"`
	Check string `json:"check"`
	Match string `json:"match"` // the flagged text
	Line  string `json:"line"`  // the whole line's text, trimmed
	// A judge's verdict: the hashes of the text and rubric it judged (Match and
	// Line stay empty).
	Content   string `json:"content,omitempty"`
	Rubric    string `json:"rubric,omitempty"`
	Dismissed bool   `json:"dismissed"`
	At        string `json:"at"`
}

func (d Dismissal) key() string {
	return d.File + "\x00" + d.Check + "\x00" + d.Match + "\x00" + d.Line + "\x00" + d.Content + "\x00" + d.Rubric
}

// dismissalFor identifies a finding on its line.
func dismissalFor(file, check string, f checks.Finding, lines []string) Dismissal {
	d := Dismissal{File: file, Check: check, Match: f.Match}
	if d.Match == "" {
		d.Match = f.Message
	}
	if f.Line >= 1 && f.Line <= len(lines) {
		d.Line = strings.TrimSpace(lines[f.Line-1])
	}
	return d
}

// judgeDismissal identifies verdict j of judge check on file.
func judgeDismissal(file, check string, j Judgment) Dismissal {
	return Dismissal{File: file, Check: check, Content: j.ContentHash, Rubric: j.RubricHash}
}

// ignoredVerdict reports whether the reviewer ignored verdict j of judge check
// on file.
func ignoredVerdict(dismissed map[string]Dismissal, file, check string, j Judgment) bool {
	_, ok := dismissed[judgeDismissal(file, check, j).key()]
	return ok
}

func (c *PrereviewController) dismissalsPath() string {
	return filepath.Join(filepath.Dir(c.CSVPath), CheckDismissalsFileName)
}

// dismissals is the review's live dismissals (latest record per key), re-read
// only when the log changes. Shared read-only by every tab.
func (c *PrereviewController) dismissals() map[string]Dismissal {
	if c.CSVPath == "" {
		return nil
	}
	p := c.dismissalsPath()
	fp := statusFingerprint(p)
	c.judgeMu.Lock()
	defer c.judgeMu.Unlock()
	if c.dismissCache == nil || fp != c.dismissFP {
		latest := map[string]Dismissal{}
		readJSONL(p, func(line []byte) {
			var d Dismissal
			if json.Unmarshal(line, &d) == nil && d.File != "" && d.Check != "" {
				latest[d.key()] = d
			}
		})
		live := map[string]Dismissal{}
		for k, d := range latest {
			if d.Dismissed {
				live[k] = d
			}
		}
		c.dismissCache, c.dismissFP = live, fp
	}
	return c.dismissCache
}

// withoutDismissed drops a file's dismissed findings from its results (copies:
// the runner's cache is shared). A checker left with nothing passes; Dismissed
// counts what was dropped, for the Checks page's "Show again".
func withoutDismissed(res []checks.Result, path string, lines func() []string, dismissed map[string]Dismissal) []checks.Result {
	if len(dismissed) == 0 {
		return res
	}
	out := make([]checks.Result, len(res))
	copy(out, res)
	var ls []string
	for i, r := range out {
		if r.Type == checks.TypeJudge || len(r.Findings) == 0 {
			continue
		}
		if ls == nil {
			ls = lines()
		}
		var kept []checks.Finding
		for _, f := range r.Findings {
			if _, ok := dismissed[dismissalFor(path, r.ID, f, ls).key()]; ok {
				out[i].Dismissed++
			} else {
				kept = append(kept, f)
			}
		}
		if out[i].Dismissed == 0 {
			continue
		}
		out[i].Findings = kept
		if len(kept) == 0 && r.Status == checks.StatusFail {
			out[i].Status = checks.StatusPass
		}
	}
	return out
}

// DismissFinding ignores one finding: it's fine here. Recorded in the review's
// store; every tab drops it.
func (c *PrereviewController) DismissFinding(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path := ctx.GetString("path")
	if path == "" {
		path = state.SelectedFile
	}
	r, f, ok := state.findingIn(path, ctx.GetString("id"), ctx.GetInt("index"))
	if !ok {
		return state, nil // gone already (another tab, an edit)
	}
	lines, err := checks.ReadLines(filepath.Join(c.RepoPath, filepath.FromSlash(path)))
	if err != nil {
		return state, err
	}
	d := dismissalFor(path, r.ID, f, lines)
	d.Dismissed, d.At = true, time.Now().UTC().Format(time.RFC3339)
	if err := AppendJSONL(c.dismissalsPath(), d); err != nil {
		return state, err
	}
	c.reapplyChecks(&state)
	state.Flash = "Ignored: " + d.Match
	go c.broadcast("ChecksUpdated") // other tabs
	return state, nil
}

// DismissAllFindings ignores every failure check id's row counts, or with no
// id every listed check's (bulkTargets): a checker's findings, or a judge's
// failing verdicts.
func (c *PrereviewController) DismissAllFindings(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	at := time.Now().UTC().Format(time.RFC3339)
	verdicts := c.verdicts()
	files := map[string][]string{}
	var recs []any
	var judged int
	for _, t := range state.bulkTargets(ctx.GetString("id")) {
		id := t.Res.ID
		var d Dismissal
		if t.Res.Type == checks.TypeJudge {
			j, ok := verdicts[judgmentKey(t.Path, id)]
			if !ok { // the log changed since the page was built
				continue
			}
			d = judgeDismissal(t.Path, id, j)
			judged++
		} else {
			lines, ok := files[t.Path]
			if !ok {
				var err error
				if lines, err = checks.ReadLines(filepath.Join(c.RepoPath, filepath.FromSlash(t.Path))); err != nil {
					return state, err
				}
				files[t.Path] = lines
			}
			d = dismissalFor(t.Path, id, t.Res.Findings[t.Index], lines)
		}
		d.Dismissed, d.At = true, at
		recs = append(recs, d)
	}
	if len(recs) == 0 {
		return state, nil
	}
	if err := AppendJSONL(c.dismissalsPath(), recs...); err != nil {
		return state, err
	}
	c.reapplyChecks(&state)
	noun := "failure" // findings and verdicts
	switch judged {
	case 0:
		noun = "finding"
	case len(recs):
		noun = "verdict"
	}
	state.Flash = fmt.Sprintf("Ignored %d %s%s", len(recs), noun, plural(len(recs)))
	go c.broadcast("ChecksUpdated") // other tabs
	return state, nil
}

// RestoreAllDismissed is the pressed Ignored N: it brings back what check id
// ignored, or with no id what every listed check ignored, in the files the
// page covers (its file filter).
func (c *PrereviewController) RestoreAllDismissed(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	ids := []string{ctx.GetString("id")}
	if ids[0] == "" {
		p := state.CheckPage()
		if p == nil {
			return state, nil
		}
		ids = p.listedChecks()
	}
	return c.restore(state, ids, state.ChecksFile)
}

// ToggleIgnoreVerdict ignores a judge's failing verdict on path ("" = the open
// file), or brings an ignored one back.
func (c *PrereviewController) ToggleIgnoreVerdict(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path, id := ctx.GetString("path"), ctx.GetString("id")
	if path == "" {
		path = state.SelectedFile
	}
	j, ok := c.verdicts()[judgmentKey(path, id)]
	if !ok {
		return state, nil // gone already (another tab)
	}
	d := judgeDismissal(path, id, j)
	d.Dismissed, d.At = !ignoredVerdict(c.dismissals(), path, id, j), time.Now().UTC().Format(time.RFC3339)
	if err := AppendJSONL(c.dismissalsPath(), d); err != nil {
		return state, err
	}
	c.reapplyChecks(&state)
	go c.broadcast("ChecksUpdated") // other tabs
	return state, nil
}

// RestoreDismissed brings back every finding (or judge verdict) you ignored for
// one check in one file, or in every file when path is "".
func (c *PrereviewController) RestoreDismissed(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, nil
	}
	return c.restore(state, []string{id}, ctx.GetString("path"))
}

// restore brings back what checks ids ignored in path, or in every file when
// path is "", in one write.
func (c *PrereviewController) restore(state PrereviewState, ids []string, path string) (PrereviewState, error) {
	at := time.Now().UTC().Format(time.RFC3339)
	var undo []any
	for _, d := range c.dismissals() {
		if slices.Contains(ids, d.Check) && (path == "" || d.File == path) {
			d.Dismissed, d.At = false, at
			undo = append(undo, d)
		}
	}
	if len(undo) == 0 {
		return state, nil
	}
	if err := AppendJSONL(c.dismissalsPath(), undo...); err != nil {
		return state, err
	}
	c.reapplyChecks(&state)
	go c.broadcast("ChecksUpdated")
	return state, nil
}

// HideFinding folds one finding's card for this session: the mark and the
// bar's count stay; nothing is decided about the finding.
func (c *PrereviewController) HideFinding(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	r, f, ok := state.finding(ctx.GetString("id"), ctx.GetInt("index"))
	if ok {
		state.HiddenFindings = append(state.HiddenFindings, findingKey(state.SelectedFile, f, r.ID))
	}
	return state, nil
}

// ShowHiddenFindings unfolds the hidden cards of the picked check.
func (c *PrereviewController) ShowHiddenFindings(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	r, ok := state.selectedFindings()
	if !ok {
		return state, nil
	}
	var keep []string
	hide := map[string]bool{}
	for _, f := range r.Findings {
		hide[findingKey(state.SelectedFile, f, r.ID)] = true
	}
	for _, k := range state.HiddenFindings {
		if !hide[k] {
			keep = append(keep, k)
		}
	}
	state.HiddenFindings = keep
	return state, nil
}
