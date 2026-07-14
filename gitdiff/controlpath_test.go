package gitdiff

import (
	"os"
	"path/filepath"
	"testing"
)

// prereview's own store must never appear in the file list.
//
// The store is UNTRACKED, so `git ls-files --others` hands it right back unless the repo
// happens to .gitignore it. Nothing did — so in any repo without that ignore line, the drawer
// listed .prereview/comments.csv and .prereview/server.pid, and since a leading dot sorts
// first, auto-select opened prereview's OWN CSV as the file under review on launch.
// ListFilesNoGit skipped the control dirs from the start; the git path never did.
func TestListFiles_SkipsPrereviewStore(t *testing.T) {
	repo := fixtureRepo(t, map[string]string{"app.go": "package app\n"})

	// The store, exactly as a real run leaves it — untracked, not ignored.
	store := filepath.Join(repo, ".prereview")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"comments.csv", "server.pid", "events.jsonl"} {
		if err := os.WriteFile(filepath.Join(store, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A file whose NAME merely starts with ".prereview" is a normal repo file and must stay:
	// the rule is a path SEGMENT, not a prefix.
	if err := os.WriteFile(filepath.Join(repo, ".prereviewrc"), []byte("cfg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ListFiles(repo, "HEAD")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}

	var paths []string
	for _, e := range got {
		paths = append(paths, e.Path)
		if e.Path == ".prereview/comments.csv" || e.Path == ".prereview/server.pid" ||
			e.Path == ".prereview/events.jsonl" {
			t.Errorf("prereview's own store leaked into the file list: %q — in a repo that "+
				"hasn't gitignored .prereview/, this makes the tool open its own comments.csv "+
				"as the file under review (it sorts first)", e.Path)
		}
	}

	var sawApp, sawRC bool
	for _, p := range paths {
		switch p {
		case "app.go":
			sawApp = true
		case ".prereviewrc":
			sawRC = true
		}
	}
	if !sawApp {
		t.Errorf("real repo files must still be listed; got %v", paths)
	}
	if !sawRC {
		t.Errorf(".prereviewrc is a normal file, not the store — it must still be listed; got %v", paths)
	}
}

// The store can live under a subdirectory when --out points inside the repo, so the skip is a
// path-segment match rather than a top-level-prefix one.
func TestListFiles_SkipsNestedPrereviewStore(t *testing.T) {
	repo := fixtureRepo(t, map[string]string{"app.go": "package app\n"})
	nested := filepath.Join(repo, "sub", "dir", ".prereview")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "comments.csv"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ListFiles(repo, "HEAD")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	for _, e := range got {
		if e.Path == "sub/dir/.prereview/comments.csv" {
			t.Errorf("a store under --out inside the repo still leaked: %q", e.Path)
		}
	}
}
