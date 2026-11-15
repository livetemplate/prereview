package review

import (
	"encoding/json"
	"path/filepath"
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

// CheckDismissalsFileName is the review store's dismissal log.
const CheckDismissalsFileName = "check-dismissals.jsonl"

// Dismissal is one dismissal (or its undoing).
type Dismissal struct {
	File      string `json:"file"`
	Check     string `json:"check"`
	Match     string `json:"match"` // the flagged text
	Line      string `json:"line"`  // the whole line's text, trimmed
	Dismissed bool   `json:"dismissed"`
	At        string `json:"at"`
}

func (d Dismissal) key() string { return dismissKey(d.File, d.Check, d.Match, d.Line) }

func dismissKey(file, check, match, line string) string {
	return file + "\x00" + check + "\x00" + match + "\x00" + line
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
	if err := appendJSONL(c.dismissalsPath(), d); err != nil {
		return state, err
	}
	c.reapplyChecks(&state)
	state.Flash = "Ignored: " + d.Match
	go c.broadcast("ChecksUpdated") // other tabs
	return state, nil
}

// RestoreDismissed brings back every finding you ignored for one check in one file.
func (c *PrereviewController) RestoreDismissed(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path, id := ctx.GetString("path"), ctx.GetString("id")
	at := time.Now().UTC().Format(time.RFC3339)
	for _, d := range c.dismissals() {
		if d.File == path && d.Check == id {
			d.Dismissed, d.At = false, at
			if err := appendJSONL(c.dismissalsPath(), d); err != nil {
				return state, err
			}
		}
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
		state.HiddenFindings = append(state.HiddenFindings, findingKey(state.SelectedFile, f, r.Name))
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
		hide[findingKey(state.SelectedFile, f, r.Name)] = true
	}
	for _, k := range state.HiddenFindings {
		if !hide[k] {
			keep = append(keep, k)
		}
	}
	state.HiddenFindings = keep
	return state, nil
}
