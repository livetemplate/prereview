package gitdiff

import (
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
