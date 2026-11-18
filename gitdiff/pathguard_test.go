package gitdiff

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadersStayInsideTheRoot: the diff loaders only read files inside the
// review root, whatever path a client sends.
func TestLoadersStayInsideTheRoot(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("not under review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(parent, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "in.txt"), []byte("under review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "outside.txt"), filepath.Join(repo, "link.txt")); err != nil {
		t.Fatal(err)
	}

	leaked := func(d *FileDiff) bool {
		if d == nil {
			return false
		}
		for _, l := range d.Lines {
			if strings.Contains(l.Content, "not under review") {
				return true
			}
		}
		return false
	}

	t.Run("no git", func(t *testing.T) {
		for _, p := range []string{"../outside.txt", filepath.Join(parent, "outside.txt"), "link.txt", "sub/../../outside.txt"} {
			d, err := LoadDiffNoGit(repo, p)
			if err == nil || leaked(d) {
				t.Errorf("LoadDiffNoGit(%q) = (leaked %v, err %v), want an error", p, leaked(d), err)
			}
		}
		if d, err := LoadDiffNoGit(repo, "in.txt"); err != nil || len(d.Lines) == 0 {
			t.Errorf("LoadDiffNoGit(in.txt) = %v, %v; want its lines", d, err)
		}
	})

	t.Run("git", func(t *testing.T) {
		for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-qm", "seed"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = repo
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		for _, p := range []string{"../outside.txt", filepath.Join(parent, "outside.txt")} {
			d, err := LoadDiff(repo, "HEAD", p)
			if err == nil || leaked(d) {
				t.Errorf("LoadDiff(%q) = (leaked %v, err %v), want an error", p, leaked(d), err)
			}
		}
	})
}

// TestIsValidRefRefusesOptions: a ref that starts with "-" is never valid,
// even one git would accept as an option.
func TestIsValidRefRefusesOptions(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "seed"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if !IsValidRef(repo, "HEAD") {
		t.Fatal("HEAD should be valid")
	}
	for _, ref := range []string{"--all", "-h", "--git-dir=x"} {
		if IsValidRef(repo, ref) {
			t.Errorf("IsValidRef(%q) = true, want false", ref)
		}
	}
}

// TestLoadDiffPathIsLiteral: a path is a file name, not a git pathspec, so
// pathspec magic can't widen it to files outside the review root.
func TestLoadDiffPathIsLiteral(t *testing.T) {
	parent := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = parent
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("not under review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(parent, "sub")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "in.txt"), []byte("under review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-qm", "seed"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = parent
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("not under review, edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _ := LoadDiff(repo, "HEAD", ":(top)outside.txt")
	if d != nil {
		for _, l := range d.Lines {
			if strings.Contains(l.Content, "not under review") {
				t.Fatalf("LoadDiff with a pathspec read a file outside the review root")
			}
		}
	}
}

// A write lands only on a plain file whose folder is inside the root: a link,
// even one pointing inside the root, would change the file it points at.
func TestWritableWithin(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	for _, p := range []string{filepath.Join(repo, "sub", "in.txt"), filepath.Join(parent, "outside.txt")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"out.txt": filepath.Join(parent, "outside.txt"),
		"in.txt":  filepath.Join(repo, "sub", "in.txt"),
		"up":      parent,
	} {
		if err := os.Symlink(target, filepath.Join(repo, link)); err != nil {
			t.Fatal(err)
		}
	}
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]error{
		"sub/in.txt":     nil,
		"sub/new.txt":    nil,
		"out.txt":        ErrLinkTarget,
		"in.txt":         ErrLinkTarget,
		"up/outside.txt": ErrOutsideRoot,
		"../outside.txt": ErrOutsideRoot,
		"..":             ErrOutsideRoot,
		".":              ErrOutsideRoot,
		"sub/..":         ErrOutsideRoot,
		"sub/../..":      ErrOutsideRoot,
		"":               ErrOutsideRoot,
	} {
		got, err := WritableWithin(repo, rel)
		if want == nil {
			if err != nil || got != filepath.Join(resolvedRepo, filepath.FromSlash(rel)) {
				t.Errorf("%s: got %q, %v; want the path under the repo", rel, got, err)
			}
			continue
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", rel, err, want)
		}
	}
}
