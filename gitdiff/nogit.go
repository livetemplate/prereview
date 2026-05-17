package gitdiff

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ListFilesNoGit enumerates the reviewable files when the target is NOT
// a git repo — the sibling of ListFiles for loose files / non-git
// directories.
//
//   - single != "" → exactly one entry (single-file review). single is
//     the basename relative to repo (repo is its parent directory).
//   - single == "" → walk repo recursively. Skips the .git/ and
//     .prereview/ control dirs, dotfiles and dotdirs (editor/state
//     cruft a non-git tree has no .gitignore to hide), and files over
//     the render cap (reviewing megabytes in a browser is pointless and
//     would only render a "too large" placeholder anyway).
//
// Every entry is Status "A": with no base there is no diff, so the whole
// file is "new" — the same convention ListFiles uses for untracked files
// and the empty-tree (clean-tree whole-branch) review. Added carries the
// line count so the drawer still shows a "+N" badge; binary files report
// 0 (countLines can't read them) and render as a binary placeholder when
// opened. Entries are sorted by path so the drawer is stable.
func ListFilesNoGit(repo, single string) ([]FileEntry, error) {
	if single != "" {
		full := filepath.Join(repo, single)
		if _, err := os.Stat(full); err != nil {
			// The sole file vanished (deleted after launch) — an empty
			// list degrades to the "pick a file" empty state instead of
			// breaking Mount, mirroring ListFiles skipping absent files.
			return nil, nil
		}
		e := FileEntry{Path: single, Status: "A"}
		if n, ok := countLines(full); ok {
			e.Added = n
		}
		return []FileEntry{e}, nil
	}

	var entries []FileEntry
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable subtree (permissions): skip it rather than abort
			// the whole listing — a non-git tree is a grab-bag we don't
			// control.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == repo {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == ".prereview" || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.Size() > maxRenderableFileBytes {
			return nil
		}
		rel, rerr := filepath.Rel(repo, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		e := FileEntry{Path: rel, Status: "A"}
		if n, ok := countLines(path); ok {
			e.Added = n
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// LoadDiffNoGit returns the full-file view for one path when there is no
// git base to diff against — the sibling of LoadDiff. Every line is an
// "add" (the file is wholly "new" with no base), produced by the same
// loadUntrackedAsAdded reader git mode already uses for untracked files,
// then run through the shared highlightLines hook so syntax highlighting
// and rendered-Markdown blocks populate identically to the git path.
// Files over the render cap short-circuit with a Note, exactly like
// LoadDiff, so a huge file never gets read into the page.
func LoadDiffNoGit(repo, path string) (*FileDiff, error) {
	full := filepath.Join(repo, path)
	if st, err := os.Stat(full); err == nil && st.Size() > maxRenderableFileBytes {
		return &FileDiff{
			Path: path,
			Note: tooLargeNote(st.Size()),
		}, nil
	}
	fd, err := loadUntrackedAsAdded(repo, path)
	if err != nil {
		return nil, err
	}
	highlightLines(fd)
	return fd, nil
}
