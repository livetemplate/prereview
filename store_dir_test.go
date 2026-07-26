package main

import (
	"os"
	"path/filepath"
	"strings"
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

// #199. --out has to accept whatever the caller has in hand: the STORE line, the
// reviewed file, or (historically) the review directory.
func TestStoreDir_Forms(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "plan.md")
	if err := os.WriteFile(target, []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := storeDirFor(root, target)

	// Form 1: the STORE line, used verbatim — no second .prereview joined onto it.
	if got, err := storeDir(want); err != nil || got != want {
		t.Errorf("storeDir(STORE) = %q err %v, want %q", got, err, want)
	}
	// Form 2: the reviewed file — the same path the review was launched with.
	if got, err := storeDir(target); err != nil || got != want {
		t.Errorf("storeDir(file) = %q err %v, want %q", got, err, want)
	}
	// Form 3: a directory still means <dir>/.prereview.
	dir := t.TempDir()
	if got, err := storeDir(dir); err != nil || got != filepath.Join(dir, ".prereview") {
		t.Errorf("storeDir(dir) = %q err %v, want %s/.prereview", got, err, dir)
	}
}

// A skill installed by an older prereview passes the REPO directory to --out. That
// used to be the store; for a directory whose reviews are all single-file it now
// holds nothing, and `watch` would block forever on an event log nobody writes.
// It must fail loudly, naming the stores it found.
func TestStoreDir_DirectoryAnchorWithOnlyTargetStores(t *testing.T) {
	root := t.TempDir()
	targetStore := storeDirFor(root, filepath.Join(root, "plan.md"))
	if err := os.MkdirAll(targetStore, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := storeDir(root)
	if err == nil {
		t.Fatal("storeDir(dir) succeeded with no store of its own — the agent would hang on a store nothing writes")
	}
	if !strings.Contains(err.Error(), targetStore) {
		t.Errorf("error does not name the store to use:\n%v", err)
	}

	// A directory review of the SAME directory is a real store and keeps working,
	// target stores beside it or not.
	if err := os.WriteFile(filepath.Join(root, ".prereview", "comments.csv"), []byte("id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := storeDir(root); err != nil || got != filepath.Join(root, ".prereview") {
		t.Errorf("storeDir(dir with its own store) = %q err %v, want it to resolve normally", got, err)
	}
}
