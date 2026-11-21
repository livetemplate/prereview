package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/review"
)

// TestImportCmd: a valid payload is queued in the store's inbox; any bad record
// fails the command and queues nothing. A missing file is caught only while a
// review runs (it records its root); otherwise the review checks at launch.
func TestImportCmd(t *testing.T) {
	repo := t.TempDir()
	store := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "doc.md"), []byte("# Doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inbox := review.ImportsPath(filepath.Join(store, review.CommentsFileName))
	run := func(payload string) error {
		t.Helper()
		f := filepath.Join(t.TempDir(), "p.json")
		if err := os.WriteFile(f, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
		return runImport([]string{"--out", store, "--file", f})
	}
	good := `{"remote_id":"a1","source":"Google Docs","file":"doc.md","quote":"Doc","body":"ok?"}`
	missing := `{"remote_id":"a4","source":"Notion","file":"gone.md","body":"x"}`
	for name, payload := range map[string]string{
		"no body":        `{"remote_id":"a2","source":"Notion","file":"doc.md"}`,
		"outside":        `{"remote_id":"a3","source":"Notion","file":"../doc.md","body":"x"}`,
		"one bad of two": "[" + good + `,{"remote_id":"a5","source":"Notion","file":"../x.md","body":"x"}]`,
		"not JSON":       `{"remote_id":`,
	} {
		if err := run(payload); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := os.Stat(inbox); !os.IsNotExist(err) {
		t.Fatalf("a failed import wrote the inbox (%v)", err)
	}

	// A review running here (this process, recorded with its root).
	if err := writeServerRecord(filepath.Join(store, serverPIDFileName), store, serverRecord{PID: os.Getpid(), Repo: repo}); err != nil {
		t.Fatal(err)
	}
	if err := run(missing); err == nil {
		t.Error("a missing file was accepted while the review runs")
	}
	if err := run(good + "\n" + strings.Replace(good, "a1", "a6", 1)); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(store, serverPIDFileName))
	if err := run(missing); err != nil {
		t.Errorf("with no review running, the file is left to the launch: %v", err)
	}
	b, err := os.ReadFile(inbox)
	if err != nil || strings.Count(string(b), "\n") != 3 {
		t.Errorf("inbox after three good records: %q (%v)", b, err)
	}
}
