package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// TestVersionScope_ExcludesPrereviewDir is the load-bearing safety check: the
// snapshot scope must never include the version store's own .prereview/ tree, or
// each checkpoint would version its own blobs (recursive growth). ListFilesNoGit
// already skips dotdirs, but versionScope filters .prereview/ explicitly so the
// guarantee holds in git mode too (where .prereview/ is only excluded if the user
// gitignored it).
func TestVersionScope_ExcludesPrereviewDir(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "a.txt"), "a")
	mustWrite(t, filepath.Join(repo, "sub", "b.txt"), "b")
	// Simulate the store's own files living under the review root.
	mustWrite(t, filepath.Join(repo, ".prereview", "comments.csv"), "id\n")
	mustWrite(t, filepath.Join(repo, ".prereview", "versions", "blobs", "deadbeef"), "blob")

	c := &PrereviewController{RepoPath: repo, NoGit: true, Versions: &VersionStore{}}
	scope := c.versionScope()

	var paths []string
	for _, r := range scope {
		paths = append(paths, r.Path)
		if r.AbsPath != filepath.Join(repo, r.Path) {
			t.Errorf("AbsPath %q not rooted under repo for %q", r.AbsPath, r.Path)
		}
	}
	slices.Sort(paths)
	want := []string{"a.txt", "sub/b.txt"}
	if !slices.Equal(paths, want) {
		t.Fatalf("versionScope = %v, want %v (must exclude .prereview/)", paths, want)
	}
}

// TestVersionScope_SingleFile: a single-file review scopes to exactly that file.
func TestVersionScope_SingleFile(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "doc.md"), "# hi")
	mustWrite(t, filepath.Join(repo, "other.md"), "ignored")

	c := &PrereviewController{RepoPath: repo, NoGit: true, SingleFile: "doc.md", Versions: &VersionStore{}}
	scope := c.versionScope()

	if len(scope) != 1 || scope[0].Path != "doc.md" {
		t.Fatalf("single-file scope = %+v, want just doc.md", scope)
	}
}

// TestVersionScope_NilStoreOrExternal: no store or external mode ⇒ no scope, so
// checkpointVersions is a safe no-op.
func TestVersionScope_NilStoreOrExternal(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, "a.txt"), "a")

	if s := (&PrereviewController{RepoPath: repo, NoGit: true}).versionScope(); s != nil {
		t.Errorf("nil Versions should yield nil scope, got %v", s)
	}
	external := &PrereviewController{RepoPath: repo, NoGit: true, ExternalMode: true, Versions: &VersionStore{}}
	if s := external.versionScope(); s != nil {
		t.Errorf("external mode should yield nil scope, got %v", s)
	}
}

// TestVersionScope_GitOnlyChangedFiles: in a git repo the scope is the CHANGED
// set only — an unchanged tracked file (Status=="") is excluded, so a checkpoint
// doesn't read+hash the whole tree. A modified file and an untracked file are in.
func TestVersionScope_GitOnlyChangedFiles(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	mustWrite(t, filepath.Join(repo, "unchanged.txt"), "same")
	mustWrite(t, filepath.Join(repo, "modified.txt"), "before")
	git("add", "-A")
	git("commit", "-qm", "seed")
	mustWrite(t, filepath.Join(repo, "modified.txt"), "after") // working-tree edit
	mustWrite(t, filepath.Join(repo, "untracked.txt"), "new")  // untracked

	c := &PrereviewController{RepoPath: repo, Base: "HEAD", Versions: &VersionStore{}}
	var paths []string
	for _, r := range c.versionScope() {
		paths = append(paths, r.Path)
	}
	slices.Sort(paths)
	want := []string{"modified.txt", "untracked.txt"}
	if !slices.Equal(paths, want) {
		t.Fatalf("git scope = %v, want %v (unchanged.txt must be excluded)", paths, want)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
