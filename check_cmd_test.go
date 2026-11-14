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

func TestRunCheck(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PREREVIEW_CHECKS_DIR", filepath.Join(dir, "checks"))
	t.Setenv("PREREVIEW_UI_PREFS_PATH", filepath.Join(dir, "prefs.json"))
	t.Chdir(dir)
	os.WriteFile("bad.md", []byte("# Doc\n\nThis is very clearly fine.\n"), 0o644)
	os.WriteFile("good.md", []byte("# Doc\n\nThis is fine.\n"), 0o644)

	var out bytes.Buffer
	err := runCheck([]string{"bad.md"}, &out)
	if !errors.Is(err, errChecksFailed) {
		t.Fatalf("want errChecksFailed, got %v", err)
	}
	if !strings.Contains(out.String(), "bad.md:3:9: suggestion: 'very'") || !strings.Contains(out.String(), "bad.md:3:14: warning: 'clearly'") {
		t.Errorf("output:\n%s", out.String())
	}

	out.Reset()
	if err := runCheck([]string{"--json", "good.md"}, &out); err != nil {
		t.Fatalf("good.md: %v\n%s", err, out.String())
	}
	var reps []checkFileReport
	if err := json.Unmarshal(out.Bytes(), &reps); err != nil || len(reps) != 1 || len(reps[0].Results) == 0 {
		t.Fatalf("json %v: %s", err, out.String())
	}

	// A check disabled in the panel is skipped here too.
	os.WriteFile(filepath.Join(dir, "prefs.json"), []byte(`{"disabled_checks":["filler-intensifiers","weasel-words"]}`), 0o644)
	out.Reset()
	if err := runCheck([]string{"bad.md"}, &out); err != nil {
		t.Errorf("disabled checks still failed: %v\n%s", err, out.String())
	}

	if err := runCheck(nil, &out); err == nil || errors.Is(err, errChecksFailed) {
		t.Errorf("no file: %v", err)
	}
}
