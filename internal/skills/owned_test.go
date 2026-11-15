package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestOwner: prereview creates a skill only inside an agent's existing skills
// root, refreshes and removes its own files, and never touches a file it didn't
// write or one edited since.
func TestOwner(t *testing.T) {
	lib, home := t.TempDir(), t.TempDir()
	root := filepath.Join(home, ".claude", "skills")
	path := filepath.Join(root, "learned-writing", "SKILL.md")
	foreign := filepath.Join(root, "mine", "SKILL.md")
	write := func(p, body string) (bool, error) {
		var changed bool
		err := Own(lib, func(o *Owner) error {
			var werr error
			changed, werr = o.Write(root, p, []byte(body))
			return werr
		})
		return changed, err
	}

	if _, err := write(path, "v1"); !errors.Is(err, ErrNoRoot) {
		t.Fatalf("wrote with no ~/.claude/skills: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("created the agent's skills root")
	}
	mustMkdir(t, filepath.Dir(foreign))
	mustWrite(t, foreign, "theirs")

	if ch, err := write(path, "v1"); err != nil || !ch {
		t.Fatalf("first write: %v %v", ch, err)
	}
	for _, bad := range []string{filepath.Join(root, "..", "evil", "SKILL.md"), filepath.Join(home, ".bashrc"), "relative/SKILL.md"} {
		if _, err := write(bad, "x"); err == nil {
			t.Errorf("wrote outside the skills root: %s", bad)
		}
	}
	if ch, err := write(path, "v1"); err != nil || ch {
		t.Fatalf("same bytes again should be a no-op: %v %v", ch, err)
	}
	if ch, err := write(path, "v2"); err != nil || !ch || read(t, path) != "v2" {
		t.Fatalf("refresh: %v %v %q", ch, err, read(t, path))
	}
	if _, err := write(foreign, "ours"); !errors.Is(err, ErrForeign) || read(t, foreign) != "theirs" {
		t.Fatalf("overwrote a foreign file: %v", err)
	}

	mustWrite(t, path, "v2 + my edit")
	if _, err := write(path, "v3"); !errors.Is(err, ErrEdited) || read(t, path) != "v2 + my edit" {
		t.Fatalf("overwrote a hand edit: %v", err)
	}
	if err := Own(lib, func(o *Owner) error { _, err := o.Remove(path); return err }); !errors.Is(err, ErrEdited) {
		t.Fatalf("removed a hand-edited file: %v", err)
	}
	if err := Own(lib, func(o *Owner) error { return o.Reclaim(foreign, []byte("x")) }); !errors.Is(err, ErrForeign) {
		t.Fatalf("reclaimed a file prereview never wrote: %v", err)
	}
	if err := Own(lib, func(o *Owner) error { return o.Reclaim(path, []byte("v3")) }); err != nil || read(t, path) != "v3" {
		t.Fatalf("reclaim after you chose to regenerate: %v", err)
	}
	if err := Own(lib, func(o *Owner) error { _, err := o.Remove(path); return err }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("left prereview's empty skill folder behind")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("removing ours touched theirs")
	}
}

func mustMkdir(t *testing.T, d string) {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}
