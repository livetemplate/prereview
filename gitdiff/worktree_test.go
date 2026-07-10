package gitdiff

import (
	"os"
	"path/filepath"
	"testing"
)

// newGitRepo creates a temp git repo with a single committed file, so the
// working tree starts CLEAN. It configures identity + a fixed default branch so
// the commit succeeds regardless of the host's git config.
func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		if _, err := runGit(dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := runGit(dir, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGit(dir, "commit", "-m", "init"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return dir
}

func TestWorktreeClean_CleanThenDirty(t *testing.T) {
	dir := newGitRepo(t)

	clean, err := WorktreeClean(dir)
	if err != nil {
		t.Fatalf("WorktreeClean (clean): %v", err)
	}
	if !clean {
		t.Fatal("freshly-committed working tree should be clean")
	}

	// Add an untracked file → dirty.
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	clean, err = WorktreeClean(dir)
	if err != nil {
		t.Fatalf("WorktreeClean (dirty): %v", err)
	}
	if clean {
		t.Fatal("working tree with an untracked file should be dirty")
	}
}

func TestWorktreeClean_NotARepo(t *testing.T) {
	if _, err := WorktreeClean(t.TempDir()); err == nil {
		t.Fatal("expected an error for a non-git directory")
	}
}

func TestEmptyTreeHash(t *testing.T) {
	dir := newGitRepo(t)

	hash, err := EmptyTreeHash(dir)
	if err != nil {
		t.Fatalf("EmptyTreeHash: %v", err)
	}
	if hash == "" {
		t.Fatal("empty-tree hash must not be empty")
	}
	// A SHA-1 repo yields the well-known 40-hex empty tree; SHA-256 yields 64.
	// Assert a plausible hex length rather than a literal (must work for both).
	if len(hash) != 40 && len(hash) != 64 {
		t.Fatalf("empty-tree hash %q has length %d, want 40 (SHA-1) or 64 (SHA-256)", hash, len(hash))
	}
	// Diffing HEAD against it must show a.txt as fully added (every line
	// commentable), which is the whole point of using it as a base.
	entries, err := ListFiles(dir, hash)
	if err != nil {
		t.Fatalf("ListFiles against empty tree: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Path == "a.txt" {
			found = true
			if e.Status != "A" {
				t.Errorf("a.txt status = %q, want A against the empty tree", e.Status)
			}
		}
	}
	if !found {
		t.Errorf("a.txt not listed diffing HEAD against the empty tree: %+v", entries)
	}
}
