package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/review"
)

// openStore prepares the store (comments.csv) in dir — the directory resolved by
// storeDirFor and printed as STORE, so the agent polls the right place. Shared by
// repo mode and external mode; resets the per-session scratch files (event log,
// agent status, paused marker) so a fresh session starts clean, and returns the CSV
// path plus a goroutine-safe CSV writer.
func openStore(dir string) (csvPath string, w *csv.Writer, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// Fixed CSV filename — survives server restarts so users can resume editing
	// where they left off. (Earlier versions timestamped it per session, which
	// orphaned previous comments on restart.)
	csvPath = filepath.Join(dir, review.CommentsFileName)
	// Clear any stale stream event log so a fresh session starts from seq 0
	// rather than appending onto a previous run's events. Harmless when not in
	// agent mode — the file won't exist.
	_ = os.Remove(filepath.Join(dir, review.EventsFileName))
	// Clear any stale agent-status file so a fresh session doesn't start showing
	// a "working"/"done" left over from the previous run. It's the agent's to
	// recreate.
	_ = os.Remove(filepath.Join(dir, review.LLMStatusFileName))
	// Clear any stale paused marker so a fresh session starts unpaused — a
	// rollback-induced pause from a previous run shouldn't carry over (#90).
	// The versions/ dir itself is deliberately NOT reset: it's the uncommitted
	// version history and must survive restarts.
	_ = os.Remove(filepath.Join(dir, review.PausedMarkerName))
	// Clear any stale session scope (#171). run() rewrites it immediately when this
	// session is a single-file review. Removing it FIRST is what makes the scope
	// per-session: a leftover from an earlier single-file run in this directory would
	// otherwise silently narrow a DIRECTORY review down to that one file.
	_ = os.Remove(filepath.Join(dir, review.SessionFileName))
	return csvPath, csv.NewWriter(csvPath), nil
}

// seedFromLegacyStore carries a single-file review's existing comments across the
// #199 store split. Before #199 every file reviewed from one directory shared that
// directory's store, so a reviewer upgrading mid-review would otherwise open their
// document and find their own comments gone. It copies the rows for THIS target out
// of the shared store the first time the target's own store is used.
//
// It is a COPY, deliberately: the shared store may still be the live store of a
// directory review of the same directory, and rewriting it to remove rows would
// either race that server's persist (which rewrites the whole file from its
// in-memory buffer, putting them straight back) or delete a running review's data.
// The consequence is that a comment can exist in both stores and their resolved /
// done state then diverges — acceptable for a one-time carry-over, and strictly
// better than losing the rows.
//
// A comment's STATE comes with it: the id-keyed sidecars (done markers and their
// re-enqueue tombstones, thread replies) are filtered by the same ids. Without
// that, carrying a comment the agent had already finished would put it back in the
// snapshot as fresh work — worse than not carrying it at all. Suggestions, quizzes
// and their decisions are keyed to their own ids, not to a comment, and stay with
// the store that recorded them.
//
// Idempotent: csv.Write always writes a header, so a target store that has been
// persisted even once has a comments.csv and is never re-seeded; one that never was
// gets the same filtered rows again. comments.csv is written LAST for that reason —
// it is the gate, so a failure part-way through is retried on the next launch.
func seedFromLegacyStore(csvPath, storeRoot, singleFile string) error {
	if singleFile == "" {
		return nil // a directory / external review IS the shared store
	}
	if _, err := os.Stat(csvPath); err == nil {
		return nil // this target already has its own rows
	}
	legacyDir := filepath.Join(storeRoot, storeDirName)
	legacyCSV := filepath.Join(legacyDir, review.CommentsFileName)
	if legacyCSV == csvPath {
		// Unreachable while a single-file store is nested (storeDirFor), and
		// load-bearing if that ever changes: filtering a store into ITSELF would
		// delete every other file's rows — the #171 landmine, from a new direction.
		return nil
	}
	rows, err := csv.Read(legacyCSV)
	if err != nil {
		// Continuity is a courtesy; an unreadable legacy store must not stop the
		// review the user actually asked for. Loud, not fatal.
		slog.Warn("could not read the pre-#199 shared store; starting this file's store empty",
			"path", legacyCSV, "err", err)
		return nil
	}
	mine := make([]csv.Row, 0, len(rows))
	ids := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.File == singleFile {
			mine = append(mine, r)
			ids[r.ID] = true
		}
	}
	if len(mine) == 0 {
		return nil
	}
	storeDir := filepath.Dir(csvPath)
	for _, name := range []string{
		review.ProcessedFileName, review.ReenqueuedFileName,
		review.AgentRepliesFileName, review.ReviewerRepliesFileName,
	} {
		if err := seedMarksFile(filepath.Join(legacyDir, name), filepath.Join(storeDir, name), ids); err != nil {
			// A missing sidecar only costs a "worked on" badge or a thread; the
			// review is still usable, so say so and carry on rather than refusing
			// to launch.
			slog.Warn("could not carry over a comment sidecar from the shared store",
				"file", name, "from", legacyDir, "err", err)
		}
	}
	if err := csv.NewWriter(csvPath).Write(mine); err != nil {
		return fmt.Errorf("carry over comments from %s: %w", legacyCSV, err)
	}
	slog.Info("carried this file's comments over from the shared store",
		"from", legacyCSV, "to", csvPath, "comments", len(mine))
	return nil
}

// seedMarksFile copies the lines of an append-only, id-keyed sidecar (the done
// markers and their re-enqueue tombstones, the two thread-reply logs) that belong
// to ids. The raw line is preserved rather than re-marshalled, so fields this code
// doesn't model — timestamps, authors, bodies — survive verbatim.
//
// One shape covers all four: each line carries its target as "id" or "target_id".
// A line that parses as neither is skipped, matching every loader's tolerance for a
// torn append. Writing (not appending) keeps a retry idempotent, and the marker
// COUNTS that decide doneness (processed minus reenqueued) stay exact because both
// files are filtered by the same id set.
func seedMarksFile(src, dst string, ids map[string]bool) error {
	data, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var keep [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		var m struct {
			ID       string `json:"id"`
			TargetID string `json:"target_id"`
		}
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		id := m.ID
		if id == "" {
			id = m.TargetID
		}
		if ids[id] {
			keep = append(keep, line)
		}
	}
	if len(keep) == 0 {
		return nil
	}
	// Self-contained about its directory, like csv.Writer: these are written
	// BEFORE comments.csv (the gate), so they cannot lean on its MkdirAll.
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, append(bytes.Join(keep, []byte("\n")), '\n'), 0o644)
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

// userPromptsDir is the "ask for suggestions" prompt overlay directory (#147):
// ~/.config/prereview/prompts. PREREVIEW_PROMPTS_DIR overrides it (tests, or a custom
// library). "" when the user-config dir can't be resolved — the built-in prompts still
// ship, so the picker never depends on this resolving.
func userPromptsDir() string {
	if p := os.Getenv("PREREVIEW_PROMPTS_DIR"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "prereview", "prompts")
}

// userQuizzesDir is the comprehension-quiz prompt overlay directory (#191):
// ~/.config/prereview/quizzes. PREREVIEW_QUIZZES_DIR overrides it. Kept separate
// from the suggestions library above because the two answer with different verbs
// — a quiz prompt asks for `prereview quiz`, a suggestions prompt for
// `prereview suggest` — so mixing them in one directory would put the wrong
// instructions in the wrong picker. Same "" fallback: built-ins still ship.
func userQuizzesDir() string {
	if p := os.Getenv("PREREVIEW_QUIZZES_DIR"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "prereview", "quizzes")
}

// storeDirName is the on-disk store directory, and targetsDirName the container
// inside it that holds the per-target stores. Both are path segments gitdiff
// already skips (a .prereview segment is machinery, never review material), so
// nesting the per-target stores keeps that — and the repo's .gitignore entry —
// working unchanged.
const (
	storeDirName   = ".prereview"
	targetsDirName = "files"
)

// storeDirFor resolves the store directory for a review: <storeRoot>/.prereview
// for a git repo, a plain directory or --external, and a per-target subdirectory
// for a SINGLE-FILE review (absTarget non-empty).
//
// The namespacing is issue #199. A single-file review's storeRoot is the file's
// PARENT directory (resolveTarget normalizes it there), so without this every file
// in one directory — every Claude Code plan in ~/.claude/plans/, say — resolved to
// the same store AND the same server.pid lock: launching a review of a second file
// evicted the first. Keying the directory by the target makes the lock per-target
// too, since it lives inside the store.
//
// It is keyed on the absolute TARGET path rather than the basename so an explicit
// --out shared by reviews of same-named files in different directories still gets
// one store each.
func storeDirFor(storeRoot, absTarget string) string {
	dir := filepath.Join(storeRoot, storeDirName)
	if absTarget == "" {
		return dir
	}
	return filepath.Join(dir, targetsDirName, targetSlug(absTarget))
}

// targetSlug is the per-target store directory name: a readable, filesystem-safe
// rendering of the target's basename plus a short digest of its absolute path. The
// digest carries the uniqueness (the basename may be sanitized or truncated into a
// collision); the basename is there so a human listing .prereview/files/ can tell
// which store belongs to which document.
func targetSlug(absTarget string) string {
	sum := sha256.Sum256([]byte(absTarget))
	return slugBase(filepath.Base(absTarget)) + "-" + hex.EncodeToString(sum[:4])
}

// slugBase renders name as a directory-name-safe label: anything outside
// [A-Za-z0-9._-] becomes "_", leading dots are dropped (a target named ".env" must
// not produce a hidden store directory), and the result is capped so a long
// filename can't push the store path past a filesystem's name limit. Never returns
// "" — an entirely-stripped name falls back to "file", and targetSlug's digest
// keeps it unique anyway.
func slugBase(name string) string {
	const maxLen = 48
	var b strings.Builder
	for _, r := range name {
		if b.Len() >= maxLen {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.TrimLeft(b.String(), ".")
	if s == "" {
		return "file"
	}
	return s
}

// resolveStoreRoot picks the directory whose store holds annotations:
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
// RepoPath is ALWAYS a directory: the comment store lives
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
