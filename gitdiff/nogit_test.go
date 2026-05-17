package gitdiff

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func TestListFilesNoGit_SingleFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "plan.md", "line one\nline two\nline three\n")

	entries, err := ListFilesNoGit(dir, "plan.md")
	if err != nil {
		t.Fatalf("ListFilesNoGit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.Path != "plan.md" || e.Status != "A" {
		t.Fatalf("entry = %+v, want Path=plan.md Status=A", e)
	}
	if e.Added != 3 {
		t.Fatalf("Added = %d, want 3 (one per source line)", e.Added)
	}
}

func TestListFilesNoGit_SingleFileMissing(t *testing.T) {
	dir := t.TempDir()
	entries, err := ListFilesNoGit(dir, "gone.md")
	if err != nil {
		t.Fatalf("ListFilesNoGit: %v", err)
	}
	if entries != nil {
		t.Fatalf("want nil (graceful empty) for a missing sole file, got %+v", entries)
	}
}

func TestListFilesNoGit_DirWalkSkips(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", "alpha\n")
	writeFile(t, dir, "sub/b.txt", "bravo\nbravo2\n")
	writeFile(t, dir, ".hidden.md", "secret\n")                  // dotfile → skipped
	writeFile(t, dir, ".git/config", "[core]\n")                 // .git/ → skipped
	writeFile(t, dir, ".prereview/comments.csv", "id,\n")        // .prereview/ → skipped
	writeFile(t, dir, ".vscode/settings.json", "{}\n")           // dotdir → skipped
	writeFile(t, dir, "big.bin", strings.Repeat("x", (1<<20)+1)) // > render cap → skipped

	entries, err := ListFilesNoGit(dir, "")
	if err != nil {
		t.Fatalf("ListFilesNoGit: %v", err)
	}
	var got []string
	for _, e := range entries {
		if e.Status != "A" {
			t.Fatalf("entry %s Status=%q, want A", e.Path, e.Status)
		}
		got = append(got, e.Path)
	}
	want := []string{"a.md", "sub/b.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("entries = %v, want %v (sorted, skipping dotfiles/.git/.prereview/oversize)", got, want)
	}
}

func TestLoadDiffNoGit_AllAdded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "code.go", "package p\n\nfunc F() {}\n")

	fd, err := LoadDiffNoGit(dir, "code.go")
	if err != nil {
		t.Fatalf("LoadDiffNoGit: %v", err)
	}
	if len(fd.Lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(fd.Lines))
	}
	for i, l := range fd.Lines {
		if l.Kind != "add" || l.NewNum != i+1 || l.OldNum != 0 {
			t.Fatalf("line %d = %+v, want Kind=add NewNum=%d OldNum=0", i, l, i+1)
		}
	}
}

func TestLoadDiffNoGit_MarkdownBlocks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "doc.md", "# Title\n\nA paragraph here.\n")

	fd, err := LoadDiffNoGit(dir, "doc.md")
	if err != nil {
		t.Fatalf("LoadDiffNoGit: %v", err)
	}
	// highlightLines runs for the no-git path too, so the rendered-
	// Markdown blocks must populate exactly like the git path.
	if len(fd.MarkdownBlocks) == 0 {
		t.Fatalf("want rendered Markdown blocks for a .md file, got none")
	}
}

func TestLoadDiffNoGit_Binary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob"), []byte("ab\x00cd"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fd, err := LoadDiffNoGit(dir, "blob")
	if err != nil {
		t.Fatalf("LoadDiffNoGit: %v", err)
	}
	if !fd.IsBinary || len(fd.Lines) != 0 {
		t.Fatalf("fd = %+v, want IsBinary=true and no lines", fd)
	}
}

func TestLoadDiffNoGit_TooLarge(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "huge.txt"),
		bytes.Repeat([]byte("y"), (1<<20)+1), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fd, err := LoadDiffNoGit(dir, "huge.txt")
	if err != nil {
		t.Fatalf("LoadDiffNoGit: %v", err)
	}
	if len(fd.Lines) != 0 || !strings.Contains(fd.Note, "too large") {
		t.Fatalf("fd = %+v, want no lines + a 'too large' note", fd)
	}
}
