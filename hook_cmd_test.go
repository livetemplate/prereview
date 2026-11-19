package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHookCheck: the hook checks only prose, and reports only the findings on
// the lines the edit wrote (all of them for a Write), exiting 2 when there are
// any so the agent reads them.
func TestHookCheck(t *testing.T) {
	sp := t.TempDir()
	t.Setenv("PREREVIEW_LIBRARY_DIR", filepath.Join(sp, "lib"))
	t.Setenv("PREREVIEW_UI_PREFS_PATH", filepath.Join(sp, "ui-prefs.json"))
	t.Setenv("PREREVIEW_CHECKS_DIR", filepath.Join(sp, "checks"))
	t.Setenv("PREREVIEW_HOME", filepath.Join(sp, "home"))
	repo := t.TempDir()
	doc := "# Notes\n\nThis clearly works.\n\nWe ship it on Friday.\n\nIt clearly helps the team.\n"
	if err := os.WriteFile(filepath.Join(repo, "notes.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("// This clearly works.\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(tool string, input map[string]any) (string, error) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"cwd": repo, "tool_name": tool, "tool_input": input})
		var stderr bytes.Buffer
		err := runHook(bytes.NewReader(b), &stderr)
		if err != nil && !errors.Is(err, errHookFindings) {
			t.Fatalf("%s: %v", tool, err)
		}
		if errors.Is(err, errHookFindings) != (stderr.Len() > 0) {
			t.Fatalf("%s: exit-2 error %v but stderr %q", tool, err, stderr.String())
		}
		return stderr.String(), err
	}
	lines := func(out string) []string {
		var ls []string
		for _, l := range strings.Split(out, "\n") {
			if _, rest, ok := strings.Cut(l, "notes.md:"); ok {
				n, _, _ := strings.Cut(rest, ":")
				ls = append(ls, n)
			}
		}
		return ls
	}

	if out, _ := run("Edit", map[string]any{"file_path": filepath.Join(repo, "main.go"), "new_string": "// This clearly works."}); out != "" {
		t.Fatalf("checked a file that isn't prose:\n%s", out)
	}
	out, err := run("Edit", map[string]any{"file_path": "notes.md", "old_string": "It helps the team.", "new_string": "It clearly helps the team."})
	if !errors.Is(err, errHookFindings) || strings.Join(lines(out), ",") != "7" || !strings.Contains(out, "clearly") {
		t.Fatalf("edit on line 7: want only line 7's finding, got %v\n%s", lines(out), out)
	}
	if out, _ := run("Edit", map[string]any{"file_path": "notes.md", "new_string": "We ship it on Friday."}); out != "" {
		t.Fatalf("a clean edit reported the file's older findings:\n%s", out)
	}
	if out, _ := run("Edit", map[string]any{"file_path": "notes.md", "new_string": ""}); out != "" {
		t.Fatalf("a deletion reported findings:\n%s", out)
	}
	out, _ = run("MultiEdit", map[string]any{"file_path": "notes.md", "edits": []map[string]string{
		{"new_string": "This clearly works."}, {"new_string": "We ship it on Friday."}}})
	if got := strings.Join(lines(out), ","); got != "3" {
		t.Fatalf("multi-edit: want line 3, got %s\n%s", got, out)
	}
	out, _ = run("Write", map[string]any{"file_path": "notes.md", "content": doc})
	if got := strings.Join(lines(out), ","); got != "3,7" {
		t.Fatalf("write: want lines 3 and 7, got %s\n%s", got, out)
	}
	if err := runHook(strings.NewReader("not json"), &bytes.Buffer{}); err == nil || errors.Is(err, errHookFindings) {
		t.Fatalf("bad hook input: err = %v, want a plain error (exit 1, never 2)", err)
	}
}

// TestEditedLines: every place the new text stands counts, a multi-line new
// text covers each of its lines, and a Write covers the whole file.
func TestEditedLines(t *testing.T) {
	var in hookInput
	in.ToolInput.NewString = "a b"
	content := "a b\nc\na b\nd\ne\na b\n"
	if _, got := editedLines(in, content); len(got) != 3 || !got[1] || !got[3] || !got[6] {
		t.Fatalf("repeated: %v, want lines 1, 3, 6", got)
	}
	in.ToolInput.NewString = "c\na b\n"
	if _, got := editedLines(in, content); len(got) != 2 || !got[2] || !got[3] {
		t.Fatalf("multi-line: %v, want lines 2, 3", got)
	}
	in.ToolName = "Write"
	if all, _ := editedLines(in, content); !all {
		t.Fatal("a Write isn't the whole file")
	}
}
