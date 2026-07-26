package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/livetemplate/prereview/internal/review"
)

// storeDir resolves the store directory a subcommand should read or write for the
// review named by out. It accepts the three things a user or agent plausibly has in
// hand, so --out is never a guessing game:
//
//  1. a STORE path (any path with a .prereview segment) — the STORE line printed at
//     launch, used verbatim;
//  2. the reviewed FILE — resolved to that target's own store (#199), so --out takes
//     the same path the review was launched with;
//  3. a directory — <dir>/.prereview, the repo / plain-directory review's store, and
//     the historical meaning of --out.
//
// An empty out means the current directory (form 3). Form 2 assumes the review used
// the default store root (the review path); a review launched with its own --out put
// the store somewhere this cannot infer, and only the printed STORE path finds it.
//
// It never creates anything: write subcommands (done, suggest) call os.MkdirAll on
// the result themselves; read subcommands (comments, watch) tolerate its absence.
// Centralising it here keeps every subcommand agreeing on one location — mirroring
// the review.ProcessedPath/SuggestionPath helpers on the server side.
func storeDir(out string) (string, error) {
	root := out
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve dir: %w", err)
	}
	if hasStoreSegment(abs) {
		return abs, nil
	}
	if info, serr := os.Stat(abs); serr == nil && info.Mode().IsRegular() {
		return storeDirFor(filepath.Dir(abs), abs), nil
	}
	dir := filepath.Join(abs, storeDirName)
	if err := errIfOnlyTargetStores(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// hasStoreSegment reports whether p already names a store — i.e. some path segment
// is the store directory. That is what makes the printed STORE line usable as --out
// verbatim, in both its forms (<dir>/.prereview and a per-target subdirectory of it).
func hasStoreSegment(p string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(p), "/"), storeDirName)
}

// errIfOnlyTargetStores turns the one silent failure the #199 store split can
// produce into a loud one. A skill installed by an older prereview still runs
// `prereview watch --out "<REPO>"`, which resolves to the directory's own store —
// and for a directory whose only reviews are single-file ones, nothing writes there
// any more, so the agent would block forever on an event log that never appears.
//
// Only that exact shape errors. A store with rows of its own (comments.csv), an
// event log (agent mode) or a live server (server.pid) is a real directory review
// and is left alone, and a directory with no target stores — the overwhelmingly
// common case, including a brand-new --out — resolves as it always did. Erring
// toward the error is deliberate: a false positive is a message that names the
// store to use, a false negative is the silent hang this exists to prevent.
func errIfOnlyTargetStores(dir string) error {
	for _, marker := range []string{review.CommentsFileName, review.EventsFileName, serverPIDFileName} {
		if pathExists(filepath.Join(dir, marker)) {
			return nil
		}
	}
	targets := targetStores(dir)
	if len(targets) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "no review store in %s — the reviews here are per-file (one store each).\n", dir)
	b.WriteString("Pass the STORE line printed at launch (or the reviewed file) to --out. Available:\n")
	for _, t := range targets {
		if f := review.SessionScope(filepath.Join(t, review.CommentsFileName)); f != "" {
			fmt.Fprintf(&b, "  %s\t(%s)\n", t, f)
			continue
		}
		fmt.Fprintf(&b, "  %s\n", t)
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

// targetStores lists the per-target store directories under a store, sorted for a
// stable message. Any read error yields none: this only ever enriches an error path,
// so it must never manufacture one of its own.
func targetStores(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, targetsDirName))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(dir, targetsDirName, e.Name()))
		}
	}
	slices.Sort(out)
	return out
}

// pathExists reports whether path is present, for the store-marker probes above.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
