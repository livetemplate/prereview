package review

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
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

// A file swapped for a link after its checkpoint is not written through:
// restoring it would change whatever the link points at.
func TestRestoreVersion_RefusesALink(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	victim := filepath.Join(parent, "victim.txt")
	mustWrite(t, victim, "untouched")
	mustWrite(t, filepath.Join(repo, "a.txt"), "checkpointed")
	s := newTestStore(t)
	if _, _, err := s.Checkpoint([]FileRef{{Path: "a.txt", AbsPath: filepath.Join(repo, "a.txt")}}, VersionTriggerBaseline, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}

	c := &PrereviewController{RepoPath: repo, NoGit: true, Versions: s}
	ctx := livetemplate.NewContext(context.TODO(), "restoreVersion", map[string]interface{}{"seq": 0})
	if _, err := c.RestoreVersion(PrereviewState{SelectedFile: "a.txt"}, ctx); !errors.Is(err, gitdiff.ErrLinkTarget) {
		t.Fatalf("RestoreVersion err = %v, want %v", err, gitdiff.ErrLinkTarget)
	}
	if b, _ := os.ReadFile(victim); string(b) != "untouched" {
		t.Fatalf("the link's target was written: %q", b)
	}
}

// A link that leaves the repo is left out of the snapshot scope, so its
// target is never read into the version store.
func TestVersionScope_SkipsLinksOutOfRoot(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	mustWrite(t, filepath.Join(parent, "secret.txt"), "outside")
	mustWrite(t, filepath.Join(repo, "a.txt"), "a")
	mustWrite(t, filepath.Join(repo, "b.txt"), "b")
	if err := os.Symlink(filepath.Join(parent, "secret.txt"), filepath.Join(repo, "out.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b.txt", filepath.Join(repo, "in.txt")); err != nil {
		t.Fatal(err)
	}
	c := &PrereviewController{RepoPath: repo, NoGit: true, Versions: &VersionStore{}}
	var paths []string
	for _, r := range c.versionScope() {
		paths = append(paths, r.Path)
	}
	slices.Sort(paths)
	if want := []string{"a.txt", "b.txt", "in.txt"}; !slices.Equal(paths, want) {
		t.Fatalf("versionScope = %v, want %v", paths, want)
	}
}
