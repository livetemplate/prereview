package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/review"
)

// judgeFixture is a review root with doc.md, a store, and a judge in the
// user's checks folder.
func judgeFixture(t *testing.T) (root, store string) {
	t.Helper()
	root = t.TempDir()
	store = filepath.Join(root, ".prereview")
	os.MkdirAll(store, 0o755)
	os.WriteFile(filepath.Join(root, "doc.md"), []byte("Plain line.\nThis is very good.\n"), 0o644)
	checksDir := filepath.Join(t.TempDir(), "checks")
	os.MkdirAll(checksDir, 0o755)
	os.WriteFile(filepath.Join(checksDir, "reader-first.md"), []byte("---\nkind: judge\nname: Reader first\n---\nLead with the action?\n"), 0o644)
	os.WriteFile(filepath.Join(checksDir, "house.yml"), []byte("extends: existence\ntokens: [x]\n"), 0o644)
	t.Setenv("PREREVIEW_CHECKS_DIR", checksDir)
	return root, store
}

func TestRunJudge_Records(t *testing.T) {
	root, store := judgeFixture(t)
	err := runJudge([]string{"--out", store, "--file", "doc.md", "--judge", "reader-first", "--verdict", "fail",
		"--critique", "Buries the action.", "--quote", "very good", "--model", "claude-opus-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store, review.JudgmentsFileName))
	if err != nil {
		t.Fatal(err)
	}
	var j review.Judgment
	if err := json.Unmarshal(data, &j); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	lines, _ := checks.ReadLines(filepath.Join(root, "doc.md"))
	if j.File != "doc.md" || j.Judge != "reader-first" || j.Verdict != "fail" || j.Quote != "very good" ||
		j.Model != "claude-opus-5-5" || j.ContentHash != checks.ContentHash(lines) || j.RubricHash == "" || j.At == "" {
		t.Errorf("recorded %+v", j)
	}
}

func TestRunJudge_Rejects(t *testing.T) {
	root, store := judgeFixture(t)
	outside := filepath.Join(t.TempDir(), "other.md")
	os.WriteFile(outside, []byte("x\n"), 0o644)
	base := func(over ...string) []string {
		args := map[string]string{"--out": store, "--file": "doc.md", "--judge": "reader-first", "--verdict": "pass", "--critique": "Fine."}
		for i := 0; i+1 < len(over); i += 2 {
			args[over[i]] = over[i+1]
		}
		var out []string
		for _, k := range []string{"--out", "--file", "--judge", "--verdict", "--critique"} {
			if args[k] != "" {
				out = append(out, k, args[k])
			}
		}
		return out
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"unknown judge":     {base("--judge", "nope"), `no judge "nope"`},
		"a checker":         {base("--judge", "house"), "is a checker"},
		"bad verdict":       {base("--verdict", "maybe"), "pass or fail"},
		"no critique":       {base("--critique", ""), "required"},
		"outside":           {base("--file", outside), "outside the review"},
		"escape with ../":   {base("--file", "../x.md"), "outside the review"},
		"not in the review": {base("--file", "missing.md"), "not a file in the review"},
	} {
		err := runJudge(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(store, review.JudgmentsFileName)); err == nil {
		t.Error("a rejected verdict must write nothing")
	}
	_ = root
}

func TestStoreRoot(t *testing.T) {
	for store, want := range map[string]string{
		"/repo/.prereview":          "/repo",
		"/repo/.prereview/doc.md-1": "/repo",
		"/a/b/.prereview/":          "/a/b",
	} {
		if got := storeRoot(filepath.FromSlash(store)); got != filepath.FromSlash(want) {
			t.Errorf("storeRoot(%q) = %q, want %q", store, got, want)
		}
	}
}
