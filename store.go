package main

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/review"
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
	// Clear any stale agent-status file so a fresh session doesn't start showing
	// a "working"/"done" left over from the previous run (same intent as the
	// DONE/events reset above). It's the agent's to recreate.
	_ = os.Remove(filepath.Join(dir, review.LLMStatusFileName))
	return csvPath, donePath, csv.NewWriter(csvPath), nil
}

// uiPrefsPath returns the durable per-user view-prefs file (see
// internal/review/uiprefs.go). It is deliberately per-USER, not per-repo:
// theme/mode/focus/file-view/raw/show-resolved mean the same thing in every repo,
// so they live under the OS user-config dir, shared across every prereview
// session. PREREVIEW_UI_PREFS_PATH overrides the location — used by e2e tests to
// isolate each run from the real config (and available to users who want a custom
// path). Returns "" when the user-config dir can't be resolved, which disables
// durable prefs (session-only) rather than failing — a prefs file must never
// block launching a review.
func uiPrefsPath() string {
	if p := os.Getenv("PREREVIEW_UI_PREFS_PATH"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "prereview", "ui-prefs.json")
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

// stageTemplates writes the embedded split template set to a fresh temp dir and
// returns the on-disk paths in templateOrder (page.tmpl first) plus a cleanup
// func. livetemplate.New requires template files on disk; embedding + staging
// keeps the binary self-contained. The returned order is load-bearing:
// page.tmpl is the main template and must be WithParseFiles' first argument
// (the rest are {{define}}-only partials).
func stageTemplates(fsys embed.FS) (paths []string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", fmt.Sprintf("prereview-%d-*", os.Getpid()))
	if err != nil {
		return nil, nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	for _, name := range templateOrder {
		content, err := fsys.ReadFile("templates/" + name)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("read embedded template %s: %w", name, err)
		}
		dst := filepath.Join(dir, name)
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("stage template %s: %w", name, err)
		}
		paths = append(paths, dst)
	}
	return paths, cleanup, nil
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
