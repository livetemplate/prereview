package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/livetemplate/prereview/internal/review"
)

func TestResolveStoreRoot(t *testing.T) {
	// --out empty → default review root, verbatim.
	if got, err := resolveStoreRoot("", "/some/repo"); err != nil || got != "/some/repo" {
		t.Errorf("default: got %q err %v, want /some/repo", got, err)
	}
	// --out set → overrides, made absolute (available in every mode, not just
	// --external, so the flag is never silently ignored).
	got, err := resolveStoreRoot("rel/dir", "/some/repo")
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if !filepath.IsAbs(got) || filepath.Base(got) != "dir" {
		t.Errorf("override: got %q, want an absolute path ending in rel/dir", got)
	}
}

func TestOpenStoreLayout(t *testing.T) {
	root := t.TempDir()
	csvPath, w, err := openStore(root)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if w == nil {
		t.Fatal("openStore returned nil writer")
	}
	wantDir := filepath.Join(root, ".prereview")
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf(".prereview dir not created: %v", err)
	}
	if filepath.Dir(csvPath) != wantDir || filepath.Base(csvPath) != "comments.csv" {
		t.Errorf("csvPath = %q, want %s/comments.csv", csvPath, wantDir)
	}
}

// TestOpenStoreResetsStatusFile ensures a stale agent-status file from a
// previous session is cleared on launch, so the UI doesn't show a leftover
// "working"/"done" before the agent writes anything this session.
func TestOpenStoreResetsStatusFile(t *testing.T) {
	root := t.TempDir()
	csvPath, _, err := openStore(root)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	statusPath := review.LLMStatusPath(csvPath)
	if err := os.WriteFile(statusPath, []byte(`{"state":"working"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openStore(root); err != nil {
		t.Fatalf("re-openStore: %v", err)
	}
	if _, err := os.Stat(statusPath); !os.IsNotExist(err) {
		t.Errorf("stale llm-status.json not cleared (stat err = %v)", err)
	}
}
