package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// commandFinding is the JSON a command check prints: an array of these. Lines are
// 1-based, columns 0-based half-open runes (prereview's text-comment coordinates).
// end_line defaults to line; severity defaults to the rule's level.
type commandFinding struct {
	Line     int    `json:"line"`
	EndLine  int    `json:"end_line"`
	FromCol  int    `json:"from_col"`
	ToCol    int    `json:"to_col"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// RunCommand runs a command rule on one file: argv plus the file's absolute path
// as the last argument, with the content being reviewed on stdin. Any failure (a
// timeout, a non-zero exit, output that isn't the JSON array) is an errored
// result, never a panic or a hang. A relative argv[0] containing a slash resolves
// against the rule file's folder, so a script can sit beside its rule.
func RunCommand(ctx context.Context, r *Rule, absPath string, content []byte) Result {
	if r.Err != "" {
		return ErrorResult(r, r.Err)
	}
	if r.Source != SourceBuiltin && r.Source != SourceUser { // defence in depth: Rule's fields are exported
		return ErrorResult(r, errCommandSource)
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	argv := append([]string(nil), r.Command...)
	if strings.Contains(argv[0], "/") && !filepath.IsAbs(argv[0]) && r.Path != "" {
		argv[0] = filepath.Join(filepath.Dir(r.Path), argv[0])
	}
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], absPath)...)
	cmd.Stdin = bytes.NewReader(content)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = r.Timeout / 5 // a child holding the pipes open can't wedge us
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return ErrorResult(r, fmt.Sprintf("timed out after %s", r.Timeout))
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg == "" {
				msg = ee.Error()
			}
			return ErrorResult(r, "command failed: "+firstLine(msg))
		}
		return ErrorResult(r, "could not run command: "+err.Error())
	}
	var raw []commandFinding
	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 {
		out = []byte("[]")
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return ErrorResult(r, "output is not a JSON array of findings: "+err.Error())
	}
	res := newResult(r)
	for _, f := range raw {
		if f.Line <= 0 {
			continue
		}
		end := f.EndLine
		if end < f.Line {
			end = f.Line
		}
		sev := f.Severity
		if sev == "" {
			sev = r.Level
		}
		msg := f.Message
		if msg == "" {
			msg = r.Message
		}
		res.Findings = append(res.Findings, Finding{Line: f.Line, EndLine: end,
			FromCol: max(f.FromCol, 0), ToCol: max(f.ToCol, 0), Message: msg, Severity: sev})
	}
	sortFindings(res.Findings)
	return res.finish()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
