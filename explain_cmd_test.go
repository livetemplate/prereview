package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/review"
	"github.com/livetemplate/prereview/internal/skills"
)

// explainPayload is a minimal well-formed explanation for the CLI tests.
const explainPayload = `{
  "summary": "Adds a retry loop.",
  "steps": [
    {"title": "Retry loop", "body": "Why it retries.",
     "anchors": [{"file": "worker.go", "from_line": 40, "to_line": 72}]},
    {"title": "The logo", "anchors": [{"file": "logo.png", "kind": "file"}]}
  ]
}`

func runExplainWith(t *testing.T, out, payload string) error {
	t.Helper()
	p := filepath.Join(t.TempDir(), "explain.json")
	if err := os.WriteFile(p, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	return runExplain([]string{"--out", out, "--file", p})
}

func explainLines(t *testing.T, out string) []review.Explanation {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, ".prereview", review.ExplainFileName))
	if err != nil {
		return nil
	}
	var got []review.Explanation
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e review.Explanation
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("the command must write JSONL the loader can read, got %q: %v", line, err)
		}
		got = append(got, e)
	}
	return got
}

func TestRunExplain_AppendsWithDefaults(t *testing.T) {
	out := t.TempDir()
	if err := runExplainWith(t, out, explainPayload); err != nil {
		t.Fatal(err)
	}
	if err := runExplainWith(t, out, explainPayload); err != nil {
		t.Fatal(err)
	}
	got := explainLines(t, out)
	if len(got) != 2 {
		t.Fatalf("a second submit must append, got %d lines", len(got))
	}
	e := got[0]
	if e.ID == "" || e.ID == got[1].ID || e.CreatedAt == "" {
		t.Errorf("each submit without an id gets its own id and a created_at: %+v / %+v", e, got[1])
	}
	if e.Steps[0].ID != "s1" || e.Steps[1].ID != "s2" {
		t.Errorf("step ids default to s1, s2: %q, %q", e.Steps[0].ID, e.Steps[1].ID)
	}
	if a := e.Steps[0].Anchors[0]; a.Kind != "line" || a.Side != "new" {
		t.Errorf("anchor defaults: kind %q side %q, want line/new", a.Kind, a.Side)
	}
}

// A bad payload writes nothing, and the error names the step to fix.
func TestRunExplain_RejectsNamingTheStep(t *testing.T) {
	cases := map[string]string{
		"no steps":       `{"steps": []}`,
		"no anchors":     `{"steps": [{"title": "t", "anchors": []}]}`,
		"no title":       `{"steps": [{"anchors": [{"file": "a.go", "from_line": 1}]}]}`,
		"no file":        `{"steps": [{"title": "t", "anchors": [{"from_line": 1}]}]}`,
		"escaping path":  `{"steps": [{"title": "t", "anchors": [{"file": "../x.go", "from_line": 1}]}]}`,
		"absolute path":  `{"steps": [{"title": "t", "anchors": [{"file": "/etc/passwd", "from_line": 1}]}]}`,
		"bad side":       `{"steps": [{"title": "t", "anchors": [{"file": "a.go", "side": "left", "from_line": 1}]}]}`,
		"line kind no n": `{"steps": [{"title": "t", "anchors": [{"file": "a.go"}]}]}`,
		"reversed range": `{"steps": [{"title": "t", "anchors": [{"file": "a.go", "from_line": 9, "to_line": 3}]}]}`,
		"bad kind":       `{"steps": [{"title": "t", "anchors": [{"file": "a.go", "kind": "area"}]}]}`,
		"duplicate id":   `{"steps": [{"id": "x", "title": "t", "anchors": [{"file": "a.go", "kind": "file"}]}, {"id": "x", "title": "u", "anchors": [{"file": "a.go", "kind": "file"}]}]}`,
	}
	for name, payload := range cases {
		out := t.TempDir()
		err := runExplainWith(t, out, payload)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if name != "no steps" && !strings.Contains(err.Error(), "step ") {
			t.Errorf("%s: error %q doesn't name the step", name, err)
		}
		if got := explainLines(t, out); len(got) != 0 {
			t.Errorf("%s: wrote %d lines", name, len(got))
		}
	}
	// One bad explanation in a batch rejects the whole batch.
	out := t.TempDir()
	if err := runExplainWith(t, out, "["+explainPayload+`,{"steps": []}]`); err == nil || len(explainLines(t, out)) != 0 {
		t.Errorf("a batch with a bad explanation: err %v, wrote %d", err, len(explainLines(t, out)))
	}
}

// The walkthrough's words get the agent-text checks as explain.md; findings print
// but never fail the command.
func TestRunExplain_Check(t *testing.T) {
	lib := t.TempDir()
	t.Setenv("PREREVIEW_LIBRARY_DIR", lib)
	t.Setenv("PREREVIEW_UI_PREFS_PATH", filepath.Join(t.TempDir(), "prefs.json"))
	learned := skills.LearnedChecksDir(lib)
	if err := os.MkdirAll(learned, 0o755); err != nil {
		t.Fatal(err)
	}
	rule := "extends: existence\nname: No basically\nmessage: Cut 'basically'.\nfiles: [explain.md]\ntokens: [basically]\n"
	if err := os.WriteFile(filepath.Join(learned, "learned-l1.yml"), []byte(rule), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var err error
	got := captureStderr(t, func() {
		err = runExplainWith(t, out, strings.Replace(explainPayload, "Why it retries.", "It basically retries.", 1))
	})
	if err != nil || !strings.Contains(got, "step s1:2:4: warning: Cut 'basically'. (No basically)") {
		t.Fatalf("err %v, stderr %q", err, got)
	}
	if len(explainLines(t, out)) != 1 {
		t.Error("a finding must not stop the explanation being written")
	}
}
