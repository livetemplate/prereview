package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreDir(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	// Empty out ⇒ .prereview under the current directory.
	got, err := storeDir("")
	if err != nil {
		t.Fatalf("storeDir(\"\"): %v", err)
	}
	if want := filepath.Join(cwd, ".prereview"); got != want {
		t.Errorf("storeDir(\"\") = %q, want %q", got, want)
	}

	// A relative out is resolved against the cwd, then joined with .prereview.
	got, err = storeDir("repo")
	if err != nil {
		t.Fatalf("storeDir(\"repo\"): %v", err)
	}
	if want := filepath.Join(cwd, "repo", ".prereview"); got != want {
		t.Errorf("storeDir(\"repo\") = %q, want %q", got, want)
	}

	// An absolute out passes through untouched (aside from the .prereview join).
	abs := filepath.Join(cwd, "elsewhere")
	got, err = storeDir(abs)
	if err != nil {
		t.Fatalf("storeDir(abs): %v", err)
	}
	if want := filepath.Join(abs, ".prereview"); got != want {
		t.Errorf("storeDir(abs) = %q, want %q", got, want)
	}

	// storeDir is pure: it must NOT create the directory.
	if _, statErr := os.Stat(filepath.Join(cwd, ".prereview")); !os.IsNotExist(statErr) {
		t.Errorf("storeDir created .prereview (or unexpected stat error): %v", statErr)
	}
}
