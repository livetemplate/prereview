package gitdiff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ChangedIndex is what changed in every reviewed file, without loading any of
// them as a FileDiff: per file, its changed hunks and the length of each side.
// It is built from ONE `git diff -U0` over the repo, unhighlighted, so it is cheap
// enough to cover every file of a large change. The #115 walkthrough uses it two
// ways: to ground a cited line in a file that isn't open, and to find the hunks no
// step explains.
//
// It must agree with LoadDiff, which is what the reviewer sees: a Hunk is one
// maximal run of del/add rows in LoadDiff's view, and OldLen/NewLen are the
// highest old/new line numbers it renders. -U0 and -U999999 run the same diff and
// differ only in how much context they print; files git doesn't diff are read
// with readRows, the same function the loaders use. changed_test.go checks the
// index against LoadDiff on every file of a fixture repo.
type ChangedIndex struct {
	Files map[string]*FileChanges
	// Fingerprint changes whenever any reviewed file's content (or the base)
	// does, so a walkthrough can tell it was written against an older tree.
	Fingerprint string
}

// FileChanges is one file's entry in a ChangedIndex.
type FileChanges struct {
	Hunks []Hunk
	// Opaque files get no line rows from LoadDiff: binary, or over the render cap.
	// Only a whole-file anchor can cite or explain one.
	Opaque bool
	// OldLen and NewLen are the file's line counts on each side; 0 when that side
	// doesn't exist (an added file has OldLen 0).
	OldLen, NewLen int
}

// Hunk is one block of changed lines, in `@@ -OldStart,OldCount +NewStart,NewCount @@`
// terms. A pure deletion has NewCount 0, a pure addition OldCount 0; the start of
// an empty side is meaningless and never compared.
type Hunk struct {
	OldStart, OldCount, NewStart, NewCount int
}

// Covers reports whether the line range [from, to] on side ("old" or "new")
// overlaps the hunk's lines on that side.
func (h Hunk) Covers(side string, from, to int) bool {
	start, count := h.NewStart, h.NewCount
	if side == "old" {
		start, count = h.OldStart, h.OldCount
	}
	return count > 0 && from <= start+count-1 && to >= start
}

// HasLine reports whether line n exists on side of the file, i.e. whether
// LoadDiff renders a row numbered n there. Opaque files have no rows.
func (fc *FileChanges) HasLine(side string, n int) bool {
	if fc == nil || fc.Opaque || n < 1 {
		return false
	}
	if side == "old" {
		return n <= fc.OldLen
	}
	return n <= fc.NewLen
}

// LoadChangedIndex indexes the changed files among files (the review's scope;
// entries with an empty Status are unchanged and skipped) against base.
//
// No -M, like LoadDiff: one path as the pathspec hides a rename's source, so the
// reviewer sees a renamed file as wholly added, and the index must agree.
func LoadChangedIndex(repo, base string, files []FileEntry) (*ChangedIndex, error) {
	raw, err := runGit(repo, "-c", "core.quotepath=off",
		"diff", "--no-color", "-U0", "--no-renames", "--no-ext-diff", base)
	if err != nil {
		return nil, err
	}
	parsed := parseU0(raw)
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00", base)
	sum.Write(raw)

	idx := &ChangedIndex{Files: map[string]*FileChanges{}}
	worktreeBase := isWorkingTreeBase(repo, base)
	for _, f := range files {
		if f.Status == "" {
			continue
		}
		full, ok := indexable(repo, f.Path)
		fc, inDiff := parsed[f.Path]
		switch {
		case !ok || tooLarge(full):
			fc = &FileChanges{Opaque: true}
		case !inDiff && f.Status == "A" && worktreeBase:
			// Untracked: git diff is silent about it, and LoadDiff shows it as wholly
			// added (loadUntrackedAsAdded). Its content isn't in raw, so it goes into
			// the fingerprint by size and mtime.
			fc = addedFile(full)
			statSig(sum, f.Path, full)
		case !inDiff:
			// Not in the diff after all (an untracked file against a base other
			// than HEAD): LoadDiff renders it as context, so it has rows but no hunks.
			fc = UnchangedFile(repo, f.Path)
		case !fc.Opaque:
			fc.NewLen = gitLineCount(full)
			fc.OldLen = fc.NewLen
			for _, h := range fc.Hunks {
				fc.OldLen += h.OldCount - h.NewCount
			}
		}
		idx.Files[f.Path] = fc
	}
	idx.Fingerprint = hex.EncodeToString(sum.Sum(nil))[:16]
	return idx, nil
}

// LoadChangedIndexNoGit indexes a no-git review, where LoadDiffNoGit shows every
// line of every file as added.
func LoadChangedIndexNoGit(repo string, paths []string) *ChangedIndex {
	idx := &ChangedIndex{Files: map[string]*FileChanges{}}
	sum := sha256.New()
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	for _, p := range sorted {
		full, ok := indexable(repo, p)
		if !ok || tooLarge(full) {
			idx.Files[p] = &FileChanges{Opaque: true}
		} else {
			idx.Files[p] = addedFile(full)
		}
		statSig(sum, p, filepath.Join(repo, p))
	}
	idx.Fingerprint = hex.EncodeToString(sum.Sum(nil))[:16]
	return idx
}

// UnchangedFile is the entry for a file LoadDiff shows as all context: rows on
// both sides, no hunks. Opaque when it is binary, over the render cap, unreadable,
// or not a regular file inside repo.
func UnchangedFile(repo, path string) *FileChanges {
	full, ok := indexable(repo, path)
	if !ok || tooLarge(full) {
		return &FileChanges{Opaque: true}
	}
	rows, binary, err := readRows(full)
	if err != nil || binary {
		return &FileChanges{Opaque: true}
	}
	return &FileChanges{OldLen: len(rows), NewLen: len(rows)}
}

// indexable resolves path for the index to read, with LoadDiff's guard (nothing
// outside repo, through a symlink included) and one more: only a regular file. A
// FIFO or device would block or never end, and a symlink is diffed by git as its
// target's path, not as the file it points at, so its rows can't be counted from
// disk. ok=false: treat the file as opaque (citable only as a whole file).
func indexable(repo, path string) (full string, ok bool) {
	full, err := ResolveWithin(repo, path)
	if err != nil {
		return "", false
	}
	fi, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(path)))
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return full, true
}

// addedFile is a wholly-added file, as loadUntrackedAsAdded renders it.
func addedFile(full string) *FileChanges {
	rows, binary, err := readRows(full)
	if err != nil {
		return &FileChanges{}
	}
	if binary {
		return &FileChanges{Opaque: true}
	}
	fc := &FileChanges{NewLen: len(rows)}
	if len(rows) > 0 {
		fc.Hunks = []Hunk{{NewStart: 1, NewCount: len(rows)}}
	}
	return fc
}

// statSig folds a file's size and mtime into a fingerprint: for content that
// isn't in the diff output (untracked files, no-git reviews).
func statSig(h hash.Hash, path, full string) {
	if st, err := os.Stat(full); err == nil {
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", path, st.Size(), st.ModTime().UnixNano())
	}
}

func tooLarge(full string) bool {
	st, err := os.Stat(full)
	return err == nil && st.Size() > maxRenderableFileBytes
}

// gitLineCount is the number of lines git diffs the file as: a final line
// without a newline still counts. (readRows differs on a file of only "\n":
// git diffs one empty line, the loaders render none.)
func gitLineCount(full string) int {
	data, err := os.ReadFile(full)
	if err != nil || len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// parseU0 splits `git diff -U0 --no-renames` output into per-file hunks, keyed by
// the file's current path (its old path for a deletion).
func parseU0(raw []byte) map[string]*FileChanges {
	out := map[string]*FileChanges{}
	var cur *FileChanges
	var header, oldPath, newPath string
	flush := func() {
		if cur == nil {
			return
		}
		p := newPath
		if p == "" {
			p = oldPath
		}
		if p == "" {
			p = headerPath(header)
		}
		if p != "" {
			out[p] = cur
		}
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &FileChanges{}
			header, oldPath, newPath = line, "", ""
		case cur == nil:
		case strings.HasPrefix(line, "--- "):
			oldPath = diffPath(line[4:], "a/")
		case strings.HasPrefix(line, "+++ "):
			newPath = diffPath(line[4:], "b/")
		case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
			cur.Opaque = true
		case strings.HasPrefix(line, "@@ "):
			if h, ok := parseHunkRanges(line); ok {
				cur.Hunks = append(cur.Hunks, h)
			}
		}
	}
	flush()
	return out
}

// diffPath reads a ---/+++ path, "" for /dev/null.
func diffPath(s, prefix string) string {
	s = unquotePath(strings.TrimSuffix(s, "\t"))
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(s, prefix)
}

// headerPath recovers the path from `diff --git a/P b/P`, used only for a binary
// file, whose block has no ---/+++ lines. With no renames both halves are P.
func headerPath(header string) string {
	rest := strings.TrimPrefix(header, "diff --git ")
	if strings.HasPrefix(rest, `"`) {
		if i := strings.Index(rest, `" "`); i > 0 {
			return strings.TrimPrefix(unquotePath(rest[i+2:]), "b/")
		}
		return ""
	}
	if (len(rest)-1)%2 != 0 {
		return ""
	}
	half := (len(rest) - 1) / 2
	a, b := rest[:half], rest[half+1:]
	if strings.HasPrefix(a, "a/") && strings.HasPrefix(b, "b/") && a[2:] == b[2:] {
		return b[2:]
	}
	return ""
}

// unquotePath undoes git's C-style quoting of unusual paths ("a/tab\there").
func unquotePath(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// parseHunkRanges reads `@@ -a[,b] +c[,d] @@`; an omitted count is 1.
func parseHunkRanges(line string) (Hunk, bool) {
	f := strings.Fields(line)
	if len(f) < 4 || f[0] != "@@" || f[3] != "@@" || !strings.HasPrefix(f[1], "-") || !strings.HasPrefix(f[2], "+") {
		return Hunk{}, false
	}
	ostart, oc, ok1 := hunkRange(f[1][1:])
	ns, nc, ok2 := hunkRange(f[2][1:])
	return Hunk{OldStart: ostart, OldCount: oc, NewStart: ns, NewCount: nc}, ok1 && ok2
}

func hunkRange(s string) (start, count int, ok bool) {
	a, b, has := strings.Cut(s, ",")
	start, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, false
	}
	if !has {
		return start, 1, true
	}
	count, err = strconv.Atoi(b)
	return start, count, err == nil
}

// ResolveRef returns the commit ref names, or "" when it doesn't resolve (or
// starts with "-", which git would read as an option; see IsValidRef). A cache
// keyed on it notices a new commit under an unchanged name (base HEAD after a
// commit).
func ResolveRef(repo, ref string) string {
	if strings.TrimSpace(ref) == "" || strings.HasPrefix(ref, "-") {
		return ""
	}
	out, err := runGit(repo, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
