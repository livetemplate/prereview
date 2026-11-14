package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ValeTimeout bounds one vale run.
const ValeTimeout = 10 * time.Second

// ValeConfig returns the repo's .vale.ini (or _vale.ini) path, "" when it has none.
func ValeConfig(repo string) string {
	for _, n := range []string{".vale.ini", "_vale.ini"} {
		p := filepath.Join(repo, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// ValeBinary is the vale executable on PATH, "" when it isn't installed.
func ValeBinary() string {
	p, err := exec.LookPath("vale")
	if err != nil {
		return ""
	}
	return p
}

// valeAlert is one alert in `vale --output=JSON`. Span is [begin, end], 1-based
// and inclusive, in runes (recorded against vale v3: testdata/vale-output.json,
// where a 3-byte ✨ before the match counts as one column).
type valeAlert struct {
	Check    string `json:"Check"`
	Message  string `json:"Message"`
	Severity string `json:"Severity"`
	Match    string `json:"Match"`
	Line     int    `json:"Line"`
	Span     []int  `json:"Span"`
}

// ValeRuleID prefixes Vale's check names so they can't collide with library IDs.
const ValeRuleID = "vale"

// ValeSummary is the "vale" row: the whole repo config as one check. Its Status is
// unset; RunVale finishes it, and the server shows it as running meanwhile.
func ValeSummary(repo string) Result {
	return Result{ID: ValeRuleID, Name: "Vale (repo config)", Type: SourceVale,
		Level: LevelWarning, Source: SourceVale, Path: ValeConfig(repo)}
}

// RunVale runs `vale --output=JSON <rel>` in repo, using the repo's own config. It
// returns one result per Vale check that raised an alert, plus a "vale" summary
// row that passes when nothing was raised (Vale's JSON names only failing checks).
func RunVale(ctx context.Context, bin, repo, rel string) []Result {
	summary := ValeSummary(repo)
	ctx, cancel := context.WithTimeout(ctx, ValeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--output=JSON", "--no-exit", rel)
	cmd.Dir = repo
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		summary.Err = "vale timed out after " + ValeTimeout.String()
		return []Result{summary.finish()}
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		summary.Err = "could not run vale: " + err.Error()
		return []Result{summary.finish()}
	}
	results, perr := ParseValeJSON(stdout.Bytes())
	if perr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = perr.Error()
		}
		summary.Err = "vale: " + firstLine(msg)
		return []Result{summary.finish()}
	}
	failing := 0
	for _, r := range results {
		failing += len(r.Findings)
	}
	if failing == 0 {
		return []Result{summary.finish()}
	}
	return results
}

// ParseValeJSON turns vale's JSON (a map of file → alerts) into one Result per
// check, converting Span to prereview's 0-based half-open rune columns.
func ParseValeJSON(data []byte) ([]Result, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var byFile map[string][]valeAlert
	if err := json.Unmarshal(data, &byFile); err != nil {
		return nil, err
	}
	byCheck := map[string]*Result{}
	for _, alerts := range byFile {
		for _, a := range alerts {
			if a.Line <= 0 {
				continue
			}
			res := byCheck[a.Check]
			if res == nil {
				res = &Result{ID: ValeRuleID + ":" + a.Check, Name: a.Check, Type: SourceVale,
					Level: a.Severity, Source: SourceVale}
				byCheck[a.Check] = res
			}
			f := Finding{Line: a.Line, EndLine: a.Line, Message: a.Message, Severity: a.Severity, Match: a.Match}
			if len(a.Span) == 2 && a.Span[0] >= 1 && a.Span[1] >= a.Span[0] {
				f.FromCol, f.ToCol = a.Span[0]-1, a.Span[1]
			}
			res.Findings = append(res.Findings, f)
		}
	}
	out := make([]Result, 0, len(byCheck))
	for _, r := range byCheck {
		sortFindings(r.Findings)
		out = append(out, r.finish())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
