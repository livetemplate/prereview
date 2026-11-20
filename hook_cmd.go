package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/prereview/internal/checks"
)

// errHookFindings makes `prereview check --hook` exit 2: Claude Code shows a
// PostToolUse hook's stderr to the agent only on exit 2 (the edit already ran).
var errHookFindings = errors.New("findings on the edited lines")

// hookInput is the part of Claude Code's PostToolUse input the check reads.
type hookInput struct {
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath  string  `json:"file_path"`
		Content   *string `json:"content"` // Write
		NewString string  `json:"new_string"`
		Edits     []struct {
			NewString string `json:"new_string"`
		} `json:"edits"` // MultiEdit
	} `json:"tool_input"`
}

// runHook implements `prereview check --hook`, the command of the Claude Code hook
// prereview adds while you have learned skills (internal/skills/claudehook.go).
// It reads the hook input on stdin and, for a prose file, runs the same checks
// as `prereview check`, reporting only the findings on the lines the edit wrote,
// so the agent fixes its own words, not the file's older ones.
//
// It's a flag on `check`, not a verb of its own, because the hook calls
// whichever prereview is on PATH, which can be older than the one that wrote
// the hook: an older binary rejects the unknown flag at once, where it would
// read an unknown verb as a folder to review and start a server.
func runHook(stdin io.Reader, stderr io.Writer) error {
	var in hookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return fmt.Errorf("reading the hook input: %w", err)
	}
	path := in.ToolInput.FilePath
	if path == "" {
		return nil
	}
	repo := in.Cwd
	if repo == "" {
		var err error
		if repo, err = os.Getwd(); err != nil {
			return err
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	// Most edits are code: decide before loading the checks library.
	if !checks.IsProse(checkRel(repo, path)) {
		return nil
	}
	fileLines, err := checks.ReadLines(path)
	if err != nil {
		return nil // gone since the edit, or not text: nothing to check
	}
	all, lines := editedLines(in, strings.Join(fileLines, "\n")+"\n")
	if !all && len(lines) == 0 {
		return nil // a deletion: no new words
	}
	var out []string
	for _, r := range newCheckRun(repo).lines(path, fileLines) {
		if r.Status != checks.StatusFail {
			continue
		}
		for _, f := range r.Findings {
			if all || touches(lines, f) {
				out = append(out, findingLine(path, r, f))
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	fmt.Fprintf(stderr, "prereview check found these on the lines you just wrote. Fix them before you finish:\n%s\n", strings.Join(out, "\n"))
	return errHookFindings
}

// editedLines is the 1-based lines an edit wrote in the file's new content:
// all of it for a Write, else every place an Edit's (or each MultiEdit's)
// new text now stands.
func editedLines(in hookInput, content string) (all bool, lines map[int]bool) {
	if in.ToolInput.Content != nil || in.ToolName == "Write" {
		return true, nil
	}
	news := []string{in.ToolInput.NewString}
	for _, e := range in.ToolInput.Edits {
		news = append(news, e.NewString)
	}
	lines = map[int]bool{}
	for _, ns := range news {
		if strings.TrimSpace(ns) == "" {
			continue
		}
		span := strings.Count(strings.TrimSuffix(ns, "\n"), "\n")
		line, counted := 1, 0 // the line at byte counted
		for off := 0; ; {
			i := strings.Index(content[off:], ns)
			if i < 0 {
				break
			}
			line += strings.Count(content[counted:off+i], "\n")
			counted = off + i
			for l := line; l <= line+span; l++ {
				lines[l] = true
			}
			off += i + len(ns)
		}
	}
	return false, lines
}

// touches reports whether a finding's span has a line in lines.
func touches(lines map[int]bool, f checks.Finding) bool {
	for l := f.Line; l <= max(f.EndLine, f.Line); l++ {
		if lines[l] {
			return true
		}
	}
	return false
}
