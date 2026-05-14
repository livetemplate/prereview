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

// ListFiles enumerates files that differ between base and the working tree.
//
// base may be "HEAD" (working tree vs last commit), a branch name
// ("main", "origin/main"), or any revision spec git accepts.
//
// Uses `git diff --name-status -M` so renames surface as a single 'R' entry
// rather than a delete+add pair. Untracked files are surfaced as 'A' entries
// — without this, `git diff` would silently omit them and reviewers would
// miss freshly-generated code that hasn't been `git add`-ed yet, which is
// the dominant prereview use case.
func ListFiles(repo, base string) ([]FileEntry, error) {
	out, err := runGit(repo, "diff", "--name-status", "-M", base)
	if err != nil {
		return nil, err
	}
	entries := parseNameStatus(out)

	// Numstat for added/deleted line counts on tracked changes.
	if statOut, err := runGit(repo, "diff", "--numstat", "-M", base); err == nil {
		stats := parseNumstat(statOut)
		for i := range entries {
			if s, ok := stats[entries[i].Path]; ok {
				entries[i].Added, entries[i].Deleted = s[0], s[1]
			}
		}
	}

	if isWorkingTreeBase(repo, base) {
		untracked, err := listUntracked(repo)
		if err != nil {
			return nil, err
		}
		for _, path := range untracked {
			e := FileEntry{Path: path, Status: "A"}
			// Untracked files don't show up in `git diff --numstat`; count
			// lines from the working-tree file directly.
			if n, ok := countLines(filepath.Join(repo, path)); ok {
				e.Added = n
			}
			entries = append(entries, e)
		}
	}
	return entries, nil
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
