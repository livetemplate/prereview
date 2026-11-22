package gitdiff

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hunksOf derives the hunks the reviewer sees: each maximal run of del/add rows
// in LoadDiff's view, with their text, plus the highest line number on each side.
// The index must carry the same text: move detection reads it (#115 B2).
func hunksOf(fd *FileDiff) (hunks []Hunk, oldLen, newLen int) {
	var cur *Hunk
	for _, l := range fd.Lines {
		oldLen, newLen = max(oldLen, l.OldNum), max(newLen, l.NewNum)
		if l.Kind == "ctx" {
			cur = nil
			continue
		}
		if cur == nil {
			hunks = append(hunks, Hunk{})
			cur = &hunks[len(hunks)-1]
		}
		if l.Kind == "del" {
			if cur.OldCount == 0 {
				cur.OldStart = l.OldNum
			}
			cur.OldCount++
			cur.Old = append(cur.Old, l.Content)
		} else {
			if cur.NewCount == 0 {
				cur.NewStart = l.NewNum
			}
			cur.NewCount++
			cur.New = append(cur.New, l.Content)
		}
	}
	return hunks, oldLen, newLen
}

// sameHunks compares hunks ignoring the start of an empty side, which -U0 sets to
// the line before and LoadDiff's view has no row for.
func sameHunks(a, b []Hunk) bool {
	norm := func(hs []Hunk) []Hunk {
		out := make([]Hunk, len(hs))
		for i, h := range hs {
			if h.OldCount == 0 {
				h.OldStart = 0
			}
			if h.NewCount == 0 {
				h.NewStart = 0
			}
			out[i] = h
		}
		return out
	}
	return reflect.DeepEqual(norm(a), norm(b))
}

func lines(n int, f func(i int) string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(f(i))
		b.WriteByte('\n')
	}
	return b.String()
}

// TestChangedIndexMatchesLoadDiff is the index's contract: for every changed file,
// its hunks and side lengths equal what LoadDiff renders.
func TestChangedIndexMatchesLoadDiff(t *testing.T) {
	orig := lines(40, func(i int) string { return "line " + string(rune('a'+i%26)) + strings.Repeat("x", i%5) })
	repo := fixtureRepo(t, map[string]string{
		"mod.go":       orig,
		"old name.txt": lines(12, func(i int) string { return "keep " + string(rune('A'+i)) }),
		"crlf.txt":     "one\r\ntwo\r\nthree\r\nfour\r\n",
		"bin.dat":      "\x00\x01\x02binary",
		"tail.txt":     "a\nb\nc\n",
		"gone.txt":     "deleted soon\n",
	})

	// mod.go: replace a line, delete a block, insert a block, append without a
	// trailing newline.
	ls := strings.Split(strings.TrimSuffix(orig, "\n"), "\n")
	ls[2] = "changed three"
	ls = append(ls[:10], ls[15:]...)
	ls = append(ls[:20], append([]string{"new 1", "new 2", "new 3"}, ls[20:]...)...)
	write(t, repo, "mod.go", strings.Join(ls, "\n")+"\nno newline at end")
	// A staged rename with an edit, so -M pairs it.
	runOrFail(t, repo, "git", "mv", "old name.txt", "new name.txt")
	write(t, repo, "new name.txt", lines(12, func(i int) string {
		if i == 6 {
			return "edited"
		}
		return "keep " + string(rune('A'+i))
	}))
	write(t, repo, "crlf.txt", "one\r\nTWO\r\nthree\r\nfour\r\nfive\r\n")
	write(t, repo, "bin.dat", "\x00\x01\x03binary changed")
	write(t, repo, "tail.txt", "a\nb\n") // pure deletion at EOF
	runOrFail(t, repo, "git", "rm", "-q", "gone.txt")
	write(t, repo, "fresh.go", "package fresh\n\nfunc F() {}") // untracked, no trailing newline
	write(t, repo, "nl.txt", "\n")                             // untracked, only a newline: the loaders render no rows

	files, err := ListFiles(repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := LoadChangedIndex(repo, "HEAD", files)
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, f := range files {
		if f.Status == "" {
			continue
		}
		fc := idx.Files[f.Path]
		if fc == nil {
			t.Errorf("%s (status %s): missing from the index", f.Path, f.Status)
			continue
		}
		fd, err := LoadDiff(repo, "HEAD", f.Path)
		if err != nil {
			t.Fatal(err)
		}
		if fd.IsBinary != fc.Opaque {
			t.Errorf("%s: Opaque=%v, LoadDiff IsBinary=%v", f.Path, fc.Opaque, fd.IsBinary)
		}
		want, oldLen, newLen := hunksOf(fd)
		if !sameHunks(fc.Hunks, want) {
			t.Errorf("%s: hunks\n got %+v\nwant %+v", f.Path, fc.Hunks, want)
		}
		if !fc.Opaque && (fc.OldLen != oldLen || fc.NewLen != newLen) {
			t.Errorf("%s: lens old=%d new=%d, LoadDiff old=%d new=%d", f.Path, fc.OldLen, fc.NewLen, oldLen, newLen)
		}
		checked++
	}
	// mod.go, new name.txt, crlf.txt, bin.dat, tail.txt, fresh.go, nl.txt.
	if checked != 7 {
		t.Errorf("checked %d changed files, want 7 (files=%+v)", checked, files)
	}
	if _, ok := idx.Files["gone.txt"]; ok {
		t.Error("gone.txt: a deleted file is not in the review's scope, so not in the index")
	}
}

func TestChangedIndexFingerprint(t *testing.T) {
	repo := fixtureRepo(t, map[string]string{"a.go": "a\n"})
	write(t, repo, "a.go", "a\nb\n")
	write(t, repo, "new.go", "x\n")
	fp := func() string {
		files, err := ListFiles(repo, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		idx, err := LoadChangedIndex(repo, "HEAD", files)
		if err != nil {
			t.Fatal(err)
		}
		return idx.Fingerprint
	}
	first := fp()
	if again := fp(); again != first {
		t.Fatalf("fingerprint not stable: %s then %s", first, again)
	}
	write(t, repo, "a.go", "a\nb\nc\n")
	second := fp()
	if second == first {
		t.Error("editing a tracked file kept the fingerprint")
	}
	write(t, repo, "new.go", "x\ny\n")
	if fp() == second {
		t.Error("editing an untracked file kept the fingerprint")
	}
}

func TestHunkCovers(t *testing.T) {
	mixed := Hunk{OldStart: 10, OldCount: 2, NewStart: 10, NewCount: 3}
	del := Hunk{OldStart: 5, OldCount: 4, NewStart: 4, NewCount: 0}
	cases := []struct {
		h        Hunk
		side     string
		from, to int
		want     bool
	}{
		{mixed, "new", 12, 12, true},
		{mixed, "new", 13, 20, false},
		{mixed, "new", 1, 10, true},
		{mixed, "old", 11, 11, true},
		{mixed, "old", 12, 12, false},
		{del, "new", 4, 4, false}, // a deletion has no new-side lines to cover
		{del, "old", 8, 9, true},
	}
	for _, c := range cases {
		if got := c.h.Covers(c.side, c.from, c.to); got != c.want {
			t.Errorf("%+v.Covers(%s, %d, %d) = %v, want %v", c.h, c.side, c.from, c.to, got, c.want)
		}
	}
}

func TestChangedIndexNoGit(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.md", "# A\n\ntext")
	write(t, dir, "b.bin", "\x00x")
	idx := LoadChangedIndexNoGit(dir, []string{"a.md", "b.bin"})
	if got := idx.Files["a.md"]; got == nil || got.NewLen != 3 || !sameHunks(got.Hunks, []Hunk{{NewStart: 1, NewCount: 3, New: []string{"# A", "", "text"}}}) {
		t.Errorf("a.md = %+v, want one 3-line added hunk", got)
	}
	if got := idx.Files["b.bin"]; got == nil || !got.Opaque {
		t.Errorf("b.bin = %+v, want opaque", got)
	}
}

// Against a base other than HEAD, LoadDiff shows an untracked file as context: it
// has rows a step can cite, but no hunks to explain.
func TestChangedIndexUntrackedOlderBase(t *testing.T) {
	repo := fixtureRepo(t, map[string]string{"a.go": "a\n"})
	write(t, repo, "a.go", "a\nb\n")
	runOrFail(t, repo, "git", "commit", "-q", "-am", "second")
	write(t, repo, "new.go", "x\ny\nz\n")
	files, err := ListFiles(repo, "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := LoadChangedIndex(repo, "HEAD~1", files)
	if err != nil {
		t.Fatal(err)
	}
	fc := idx.Files["new.go"]
	if fc == nil || len(fc.Hunks) != 0 || !fc.HasLine("new", 3) || fc.HasLine("new", 4) {
		t.Fatalf("new.go = %+v, want 3 context rows and no hunks", fc)
	}
	fd, err := LoadDiff(repo, "HEAD~1", "new.go")
	if err != nil {
		t.Fatal(err)
	}
	if want, _, _ := hunksOf(fd); len(want) != 0 {
		t.Fatalf("LoadDiff shows hunks %+v; the index's assumption is stale", want)
	}
}

// UnchangedFile must agree with LoadDiff's all-context view, binary and
// empty-row files included.
func TestUnchangedFileMatchesLoadDiff(t *testing.T) {
	repo := fixtureRepo(t, map[string]string{
		"text.go": "a\nb\nc",
		"nl.txt":  "\n",
		"bin.dat": "\x00\x01",
	})
	for _, p := range []string{"text.go", "nl.txt", "bin.dat"} {
		fd, err := LoadDiff(repo, "HEAD", p)
		if err != nil {
			t.Fatal(err)
		}
		fc := UnchangedFile(repo, p)
		_, oldLen, newLen := hunksOf(fd)
		if fc.Opaque != fd.IsBinary || (!fc.Opaque && (fc.OldLen != oldLen || fc.NewLen != newLen)) {
			t.Errorf("%s: %+v, LoadDiff binary=%v rows old=%d new=%d", p, fc, fd.IsBinary, oldLen, newLen)
		}
	}
	if fc := UnchangedFile(repo, "../escape"); !fc.Opaque {
		t.Errorf("a path outside the repo must not be citable: %+v", fc)
	}
}

// The index reads only regular files inside the repo, like LoadDiff's guard: a
// symlink out of the repo or a FIFO is opaque and never read (a FIFO would block).
func TestChangedIndexReadsOnlyRegularFilesInside(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "secret.txt", "s\ne\nc\n")
	repo := fixtureRepo(t, map[string]string{"a.go": "a\n"})
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(repo, "link.txt")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if err := syscall.Mkfifo(filepath.Join(repo, "pipe"), 0o644); err != nil {
		t.Skip("mkfifo unsupported:", err)
	}
	files := []FileEntry{{Path: "link.txt", Status: "A"}, {Path: "pipe", Status: "A"}}
	done := make(chan *ChangedIndex, 1)
	go func() {
		idx, err := LoadChangedIndex(repo, "HEAD", files)
		if err != nil {
			t.Error(err)
		}
		done <- idx
	}()
	var idx *ChangedIndex
	select {
	case idx = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("LoadChangedIndex blocked: it opened the FIFO")
	}
	for _, p := range []string{"link.txt", "pipe"} {
		if fc := idx.Files[p]; fc == nil || !fc.Opaque {
			t.Errorf("%s: %+v, want opaque", p, fc)
		}
		if fc := UnchangedFile(repo, p); !fc.Opaque {
			t.Errorf("UnchangedFile(%s): %+v, want opaque", p, fc)
		}
	}
	if n := LoadChangedIndexNoGit(repo, []string{"link.txt", "pipe"}); !n.Files["link.txt"].Opaque || !n.Files["pipe"].Opaque {
		t.Errorf("no-git index read a symlink or FIFO: %+v %+v", n.Files["link.txt"], n.Files["pipe"])
	}
}
