// Package gitdiff shells out to the git CLI to enumerate changed files and
// load per-file unified diffs for prereview. Shelling out (vs. a Go git
// library) is deliberate — it respects the user's local git config (rename
// detection thresholds, autocrlf, etc.) so what prereview shows matches
// what `git diff` shows in their terminal.
package gitdiff

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FileEntry is one row in the changed-files list.
type FileEntry struct {
	Path         string // post-rename / current path
	Status       string // "A","M","D","R","C","T","U"
	Renamed      string // pre-rename path for R/C; empty otherwise
	Added        int    // lines added vs base; -1 means "binary / unknown"
	Deleted      int    // lines deleted vs base; -1 means "binary / unknown"
	CommentCount int    // populated by the controller, not by gitdiff
}

// ListFiles enumerates every file currently in the working tree
// (tracked + untracked-non-ignored), annotated with diff information vs
// base where applicable.
//
// This is the foundation of "show all files; diff is an overlay":
// prereview now always lets the reviewer browse the whole repo, with
// diff metadata layered on top of the files that happen to differ from
// base. Files unchanged vs base appear in the list with Status="",
// Added=0, Deleted=0 — the template renders them without a diff badge.
//
// Deleted files (present in base, absent in the working tree) are
// intentionally omitted — they don't exist in the repo "right now".
// Reviewers who care about deletions can flip the base or read git
// history directly.
//
// base may be "HEAD", a branch name, or any revision spec git accepts.
// If base is empty, diff annotation is skipped and every entry is bare.
func ListFiles(repo, base string) ([]FileEntry, error) {
	// 1. All tracked working-tree files via ls-files. This is the source
	//    of truth for "what's in the repo right now"; --name-status would
	//    miss any file that happens to match base exactly.
	trackedOut, err := runGit(repo, "ls-files")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var entries []FileEntry
	for line := range strings.SplitSeq(strings.TrimRight(string(trackedOut), "\n"), "\n") {
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		// Skip files that are tracked in the index but absent from the
		// working tree (`git rm` not yet run, or just `rm`-ed). They
		// don't exist "in the repo right now", so they shouldn't show
		// up in the all-files view.
		if _, err := os.Stat(filepath.Join(repo, line)); err != nil {
			continue
		}
		seen[line] = struct{}{}
		entries = append(entries, FileEntry{Path: line})
	}

	// 2. Untracked-non-ignored files — treat as "A" since they're new
	//    relative to any base. (If the user picks a future base that
	//    contains the file, the name-status pass below would correct
	//    this, but in practice untracked-relative-to-anything is "A".)
	untracked, err := listUntracked(repo)
	if err != nil {
		return nil, err
	}
	for _, p := range untracked {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		entries = append(entries, FileEntry{Path: p, Status: "A"})
	}

	// 3. Overlay diff metadata if a base is supplied. Status from
	//    --name-status, line counts from --numstat. Files without a diff
	//    entry keep Status="" / Added=0 / Deleted=0, which the template
	//    renders as a plain row (no badge).
	if strings.TrimSpace(base) != "" {
		if nsOut, err := runGit(repo, "diff", "--name-status", "-M", base); err == nil {
			for _, s := range parseNameStatus(nsOut) {
				for i := range entries {
					if entries[i].Path == s.Path {
						entries[i].Status = s.Status
						entries[i].Renamed = s.Renamed
						break
					}
				}
			}
		}
		if numOut, err := runGit(repo, "diff", "--numstat", "-M", base); err == nil {
			stats := parseNumstat(numOut)
			for i := range entries {
				if s, ok := stats[entries[i].Path]; ok {
					entries[i].Added, entries[i].Deleted = s[0], s[1]
				}
			}
		}
	}

	// 4. For files we tagged "A" from the untracked pass that didn't
	//    pick up numstat data (they're not in `git diff --numstat`),
	//    count working-tree lines directly so the +N badge still appears.
	for i := range entries {
		if entries[i].Status == "A" && entries[i].Added == 0 && entries[i].Deleted == 0 {
			if n, ok := countLines(filepath.Join(repo, entries[i].Path)); ok {
				entries[i].Added = n
			}
		}
	}

	// 5. Stable alphabetical order so the drawer doesn't reshuffle on
	//    every base swap.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// ChangedCount returns how many entries have a non-empty Status — i.e.
// how many of these files differ from the active base. Useful for the
// drawer header ("N files · M changed").
func ChangedCount(entries []FileEntry) int {
	n := 0
	for _, e := range entries {
		if e.Status != "" {
			n++
		}
	}
	return n
}

// parseNumstat parses `git diff --numstat -M` output. Each row is:
//
//	<added>\t<deleted>\t<path>
//
// where added/deleted are either decimal integers or `-` for binary files.
// Returns map[path] = [added, deleted]; uses -1 for the `-` (binary) case.
func parseNumstat(raw []byte) map[string][2]int {
	out := map[string][2]int{}
	for line := range strings.SplitSeq(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		add := parseNumstatField(parts[0])
		del := parseNumstatField(parts[1])
		// For renames, --numstat emits "{old => new}" or "old\0new"; the
		// path may contain a "=>" segment. Strip to the post-rename path
		// (everything after the last "=> " token, then trim "}").
		path := parts[2]
		if idx := strings.Index(path, "=> "); idx >= 0 {
			path = strings.TrimSuffix(path[idx+3:], "}")
			path = strings.TrimSpace(path)
		}
		out[path] = [2]int{add, del}
	}
	return out
}

func parseNumstatField(s string) int {
	if s == "-" {
		return -1 // binary
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func countLines(path string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	n := 0
	for {
		k, err := f.Read(buf)
		for i := range k {
			if buf[i] == '\n' {
				n++
			}
		}
		if err != nil {
			break
		}
	}
	return n, true
}

// IsValidRef reports whether `ref` resolves to a commit in this repo.
// Used by the runtime base picker to validate user-typed refs before
// swapping state.Base. Treats both branch names and commit-ish refs
// (HEAD~3, abc1234) uniformly via `git rev-parse --verify`.
func IsValidRef(repo, ref string) bool {
	if strings.TrimSpace(ref) == "" {
		return false
	}
	_, err := runGit(repo, "rev-parse", "--verify", ref)
	return err == nil
}

// ListBranches returns the short names of every local branch in the
// repo, sorted alphabetically. Used to populate the base-picker
// dropdown — `git for-each-ref` runs in microseconds even on big
// repos, so calling it from every Mount is fine.
func ListBranches(repo string) []string {
	out, err := runGit(repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil
	}
	var branches []string
	for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			branches = append(branches, line)
		}
	}
	sort.Strings(branches)
	return branches
}

// CurrentBranch returns the short name of the checked-out branch
// (e.g. "main", "feature/foo"). Returns "HEAD" in a detached state
// (which is what `git rev-parse --abbrev-ref HEAD` emits there).
// Errors return an empty string — callers should display "current
// branch" or similar generic copy when the result is blank.
func CurrentBranch(repo string) string {
	out, err := runGit(repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isWorkingTreeBase reports whether base resolves to HEAD — in which case
// untracked files in the working tree should also count as changes.
func isWorkingTreeBase(repo, base string) bool {
	if base == "HEAD" {
		return true
	}
	resolved, err := runGit(repo, "rev-parse", "--verify", base)
	if err != nil {
		return false
	}
	head, err := runGit(repo, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(resolved)) == strings.TrimSpace(string(head))
}

// listUntracked returns files that exist in the working tree but are not yet
// tracked by git. Respects .gitignore via --exclude-standard.
func listUntracked(repo string) ([]string, error) {
	out, err := runGit(repo, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var paths []string
	for line := range strings.SplitSeq(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}

func parseNameStatus(raw []byte) []FileEntry {
	var entries []FileEntry
	for line := range strings.SplitSeq(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		// Format:
		//   M\tpath
		//   A\tpath
		//   D\tpath
		//   R<score>\toldpath\tnewpath
		//   C<score>\toldpath\tnewpath
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		status := fields[0]
		// Strip similarity score from R100, C075, etc.
		kind := status[:1]
		switch kind {
		case "R", "C":
			if len(fields) < 3 {
				continue
			}
			entries = append(entries, FileEntry{
				Path:    fields[2],
				Status:  kind,
				Renamed: fields[1],
			})
		default:
			entries = append(entries, FileEntry{Path: fields[1], Status: kind})
		}
	}
	return entries
}

// runGit executes `git <args...>` in repo and returns stdout. Stderr is
// folded into the error so callers see the underlying git complaint.
func runGit(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
