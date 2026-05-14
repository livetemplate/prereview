package gitdiff

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// fixtureRepo creates a fresh git repo in t.TempDir() with an initial commit
// containing `seed` files (path → content) and returns the repo path. The
// repo's user.name/user.email are set to deterministic values so the test
// doesn't depend on the developer's git config.
func fixtureRepo(t *testing.T, seed map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	runOrFail(t, dir, "git", "init", "-q", "-b", "main")
	runOrFail(t, dir, "git", "config", "user.email", "test@example.com")
	runOrFail(t, dir, "git", "config", "user.name", "Test")
	runOrFail(t, dir, "git", "config", "commit.gpgsign", "false")

	for path, content := range seed {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	if len(seed) > 0 {
		runOrFail(t, dir, "git", "add", "-A")
		runOrFail(t, dir, "git", "commit", "-q", "-m", "seed")
	} else {
		// Empty commit so HEAD exists.
		runOrFail(t, dir, "git", "commit", "-q", "--allow-empty", "-m", "seed")
	}
	return dir
}

func runOrFail(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v\nstderr: %s", name, args, err, stderr.String())
	}
}

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestListFiles_ModifyAddDelete(t *testing.T) {
	repo := fixtureRepo(t, map[string]string{
		"keep.go":   "package keep\n",
		"remove.go": "package remove\n",
		"sub/m.go":  "package sub\nfunc A() {}\n",
	})

	// Modify, add, delete (working-tree changes vs HEAD).
	write(t, repo, "sub/m.go", "package sub\nfunc A() {}\nfunc B() {}\n")
	write(t, repo, "brand_new.go", "package brand\n")
	if err := os.Remove(filepath.Join(repo, "remove.go")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got, err := ListFiles(repo, "HEAD")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}

	sort.Slice(got, func(i, j int) bool { return got[i].Path < got[j].Path })

	want := []FileEntry{
		{Path: "brand_new.go", Status: "A", Added: 1, Deleted: 0},
		{Path: "remove.go", Status: "D", Added: 0, Deleted: 1},
		{Path: "sub/m.go", Status: "M", Added: 1, Deleted: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListFiles_Rename(t *testing.T) {
	repo := fixtureRepo(t, map[string]string{
		"old/path.go": "package old\nfunc Identical() {}\n",
	})

	// Rename via filesystem move + git add -A (lets git's rename detection see it).
	if err := os.MkdirAll(filepath.Join(repo, "new"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Rename(filepath.Join(repo, "old/path.go"), filepath.Join(repo, "new/path.go")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	runOrFail(t, repo, "git", "add", "-A")

	// Compare staged tree to HEAD (ListFiles default does worktree-vs-HEAD,
	// which after `git add -A` of a rename surfaces as a rename).
	got, err := ListFiles(repo, "HEAD")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Status != "R" || e.Path != "new/path.go" || e.Renamed != "old/path.go" {
		t.Errorf("got %+v, want R new/path.go ← old/path.go", e)
	}
}

func TestLoadDiff_ModifiedFile_FullFileContent(t *testing.T) {
	original := "line1\nline2\nline3\nline4\n"
	modified := "line1\nline2 EDIT\nline3\nLINE4\n"

	repo := fixtureRepo(t, map[string]string{"foo.txt": original})
	write(t, repo, "foo.txt", modified)

	fd, err := LoadDiff(repo, "HEAD", "foo.txt")
	if err != nil {
		t.Fatalf("LoadDiff: %v", err)
	}
	if fd.IsBinary {
		t.Fatal("unexpected IsBinary")
	}

	// With -U999999 every line must appear. We expect:
	//   ctx  line1     (old=1,new=1)
	//   del  line2     (old=2,new=0)
	//   add  line2 EDIT (old=0,new=2)
	//   ctx  line3     (old=3,new=3)
	//   del  line4     (old=4,new=0)
	//   add  LINE4     (old=0,new=4)
	want := []DiffLine{
		{OldNum: 1, NewNum: 1, Kind: "ctx", Content: "line1"},
		{OldNum: 2, NewNum: 0, Kind: "del", Content: "line2"},
		{OldNum: 0, NewNum: 2, Kind: "add", Content: "line2 EDIT"},
		{OldNum: 3, NewNum: 3, Kind: "ctx", Content: "line3"},
		{OldNum: 4, NewNum: 0, Kind: "del", Content: "line4"},
		{OldNum: 0, NewNum: 4, Kind: "add", Content: "LINE4"},
	}
	if len(fd.Lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%+v", len(fd.Lines), len(want), fd.Lines)
	}
	for i, w := range want {
		if fd.Lines[i] != w {
			t.Errorf("line %d: got %+v, want %+v", i, fd.Lines[i], w)
		}
	}
}

func TestLoadDiff_AddedFile_AllAddLines(t *testing.T) {
	repo := fixtureRepo(t, map[string]string{"existing.txt": "x\n"})
	write(t, repo, "added.txt", "alpha\nbeta\ngamma\n")

	fd, err := LoadDiff(repo, "HEAD", "added.txt")
	if err != nil {
		t.Fatalf("LoadDiff: %v", err)
	}
	if fd.Note != "file added" {
		t.Errorf("note = %q, want %q", fd.Note, "file added")
	}
	if len(fd.Lines) != 3 {
		t.Fatalf("got %d lines, want 3: %+v", len(fd.Lines), fd.Lines)
	}
	for i, ln := range fd.Lines {
		if ln.Kind != "add" {
			t.Errorf("line %d kind = %q, want add", i, ln.Kind)
		}
		if ln.NewNum != i+1 {
			t.Errorf("line %d NewNum = %d, want %d", i, ln.NewNum, i+1)
		}
		if ln.OldNum != 0 {
			t.Errorf("line %d OldNum = %d, want 0", i, ln.OldNum)
		}
	}
	if fd.Lines[0].Content != "alpha" || fd.Lines[2].Content != "gamma" {
		t.Errorf("content mismatch: %+v", fd.Lines)
	}
}

func TestLoadDiff_DeletedFile_AllDelLines(t *testing.T) {
	repo := fixtureRepo(t, map[string]string{"gone.txt": "one\ntwo\n"})
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	fd, err := LoadDiff(repo, "HEAD", "gone.txt")
	if err != nil {
		t.Fatalf("LoadDiff: %v", err)
	}
	if fd.Note != "file deleted" {
		t.Errorf("note = %q, want %q", fd.Note, "file deleted")
	}
	if len(fd.Lines) != 2 {
		t.Fatalf("got %d lines, want 2: %+v", len(fd.Lines), fd.Lines)
	}
	for i, ln := range fd.Lines {
		if ln.Kind != "del" {
			t.Errorf("line %d kind = %q, want del", i, ln.Kind)
		}
		if ln.OldNum != i+1 {
			t.Errorf("line %d OldNum = %d, want %d", i, ln.OldNum, i+1)
		}
	}
}

func TestLoadDiff_UnchangedFile_EmptyLines(t *testing.T) {
	repo := fixtureRepo(t, map[string]string{"steady.txt": "same\n"})

	fd, err := LoadDiff(repo, "HEAD", "steady.txt")
	if err != nil {
		t.Fatalf("LoadDiff: %v", err)
	}
	if len(fd.Lines) != 0 {
		t.Errorf("unchanged file produced %d lines, want 0: %+v", len(fd.Lines), fd.Lines)
	}
	if fd.Note != "no changes" {
		t.Errorf("note = %q, want %q", fd.Note, "no changes")
	}
}

func TestLoadDiff_BinaryFile(t *testing.T) {
	// Use raw bytes that include a NUL — git treats them as binary.
	bin := append([]byte{0x00, 0x01, 0x02}, bytes.Repeat([]byte{0xff}, 16)...)

	repo := fixtureRepo(t, map[string]string{"data.bin": string(bin)})
	// Modify the binary file.
	if err := os.WriteFile(filepath.Join(repo, "data.bin"), append(bin, 0x99), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	fd, err := LoadDiff(repo, "HEAD", "data.bin")
	if err != nil {
		t.Fatalf("LoadDiff: %v", err)
	}
	if !fd.IsBinary {
		t.Error("expected IsBinary=true")
	}
	if len(fd.Lines) != 0 {
		t.Errorf("binary file produced %d lines, want 0", len(fd.Lines))
	}
}

func TestParseHunkHeader(t *testing.T) {
	cases := []struct {
		in            string
		wantOld, wantNew int
	}{
		{"@@ -1,3 +1,4 @@", 1, 1},
		{"@@ -42,1 +43,1 @@ func foo()", 42, 43},
		{"@@ -0,0 +1,3 @@", 1, 1}, // new file: -0,0 → caller treats as 1
		{"@@ -1,3 +0,0 @@", 1, 1}, // deletion: +0,0
	}
	for _, c := range cases {
		o, n, err := parseHunkHeader(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if o != c.wantOld || n != c.wantNew {
			t.Errorf("%q: got (%d,%d), want (%d,%d)", c.in, o, n, c.wantOld, c.wantNew)
		}
	}
}
