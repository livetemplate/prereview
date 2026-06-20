package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/livetemplate/prereview/csv"
)

// openStore prepares the .prereview/ store (comments.csv + DONE marker) under
// storeRoot, the directory whose .prereview/ holds annotations — the value
// printed as REPO so the skill polls the right place. Shared by repo mode and
// external mode; clears any stale DONE marker so a fresh session isn't read as
// already-handed-off, and returns the paths plus a goroutine-safe CSV writer.
func openStore(storeRoot string) (csvPath, donePath string, w *csv.Writer, err error) {
	dir := filepath.Join(storeRoot, ".prereview")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// Fixed CSV filename — survives server restarts so users can resume editing
	// where they left off. (Earlier versions timestamped it per session, which
	// orphaned previous comments on restart.)
	csvPath = filepath.Join(dir, "comments.csv")
	donePath = filepath.Join(dir, "DONE")
	_ = os.Remove(donePath)
	// Clear any stale stream event log so a fresh session starts from seq 0
	// rather than appending onto a previous run's events (same intent as the
	// DONE reset above). Harmless when not streaming — the file won't exist.
	_ = os.Remove(filepath.Join(dir, "events.jsonl"))
	return csvPath, donePath, csv.NewWriter(csvPath), nil
}

// resolveStoreRoot picks the directory whose .prereview/ holds annotations:
// --out when set (available in every mode so it's never a silently-ignored
// flag), else the default review root.
func resolveStoreRoot(out, defaultRoot string) (string, error) {
	if out == "" {
		return defaultRoot, nil
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("resolve --out: %w", err)
	}
	return abs, nil
}

// writeTempTemplate stages the embedded template to a deterministic temp
// path tied to the PID and returns its path plus a cleanup func.
func writeTempTemplate(content string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", fmt.Sprintf("prereview-%d-*.tmpl", os.Getpid()))
	if err != nil {
		return "", nil, err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

// reviewTarget is the classified path argument after normalization.
// RepoPath is ALWAYS a directory: the comment store and DONE marker live
// at RepoPath/.prereview/, and every downstream filepath.Join(RepoPath,
// relPath) stays valid. SingleFile, when non-empty, is the only
// reviewable file (its basename, relative to RepoPath). NoGit is true
// whenever the target isn't backed by a git repo — the file list and
// per-file diff are then synthesized from the filesystem instead of git.
type reviewTarget struct {
	RepoPath   string
	SingleFile string
	NoGit      bool
}

// resolveTarget classifies an absolute review path:
//
//   - a file              → no-git, review just that file
//     (RepoPath = its parent dir, SingleFile = its basename)
//   - a directory with .git  → git mode (unchanged behaviour)
//   - a directory without .git → no-git, review the whole tree
//
// It deliberately does NOT walk up to find an ancestor .git: a mistyped
// path silently resolving to some parent repo is a worse failure than a
// clear "review exactly what you pointed at" contract. A stat error
// (missing path, permission) is fatal — same as the old assertGitRepo.
func resolveTarget(absPath string) (reviewTarget, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return reviewTarget{}, fmt.Errorf("repo %q: %w", absPath, err)
	}
	if !info.IsDir() {
		return reviewTarget{
			RepoPath:   filepath.Dir(absPath),
			SingleFile: filepath.Base(absPath),
			NoGit:      true,
		}, nil
	}
	// .git may be a directory (normal repo) or a file (worktree/submodule);
	// os.Stat succeeds for both, so err == nil ⇒ git mode. Only a genuine
	// "not there" (ErrNotExist) drops to no-git; any other stat error keeps
	// git mode so git itself surfaces the real problem (old assertGitRepo
	// intent: don't pre-empt git's clearer error message).
	if _, err := os.Stat(filepath.Join(absPath, ".git")); err == nil {
		return reviewTarget{RepoPath: absPath}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return reviewTarget{RepoPath: absPath}, nil
	}
	return reviewTarget{RepoPath: absPath, NoGit: true}, nil
}
