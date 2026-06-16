package main

import (
	"os"
	"path/filepath"
	"testing"
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
	csvPath, donePath, w, err := openStore(root)
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
	if filepath.Dir(donePath) != wantDir || filepath.Base(donePath) != "DONE" {
		t.Errorf("donePath = %q, want %s/DONE", donePath, wantDir)
	}

	// A stale DONE marker from a previous session must be cleared so a fresh
	// server isn't read as already-handed-off.
	if err := os.WriteFile(donePath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := openStore(root); err != nil {
		t.Fatalf("re-openStore: %v", err)
	}
	if _, err := os.Stat(donePath); !os.IsNotExist(err) {
		t.Errorf("stale DONE not cleared (stat err = %v)", err)
	}
}
