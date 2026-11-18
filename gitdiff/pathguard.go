package gitdiff

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideRoot: a path names something outside the review root.
var ErrOutsideRoot = errors.New("path is outside the review root")

// ResolveWithin resolves rel (slash- or OS-separated, relative to root) to an
// absolute path inside root. It rejects absolute paths and any path whose
// cleaned form leaves root, and, when the file exists, a symlink anywhere on
// the way that leaves root (ErrOutsideRoot). A path that stays inside root
// but doesn't exist (a file deleted in the working tree) returns its absolute
// path with an error that satisfies errors.Is(err, os.ErrNotExist).
func ResolveWithin(root, rel string) (string, error) {
	rel = filepath.FromSlash(rel)
	clean := filepath.Clean(rel)
	if filepath.IsAbs(rel) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: %w", rel, ErrOutsideRoot)
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(rootResolved, clean)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return full, err
	}
	if !strings.HasPrefix(resolved+string(filepath.Separator), rootResolved+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: %w", rel, ErrOutsideRoot)
	}
	return resolved, nil
}

// ErrLinkTarget reports a write refused because the path is a symlink: writing
// to it would change whatever file the link points at.
var ErrLinkTarget = errors.New("path is a symlink")

// WritableWithin returns the absolute path for writing or deleting rel under
// root. rel's folder must resolve inside root, and rel itself must not be a
// symlink. A missing file is fine (a restore can recreate it).
func WritableWithin(root, rel string) (string, error) {
	dir, err := ResolveWithin(root, filepath.Dir(filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	full := filepath.Join(dir, filepath.Base(rel))
	if fi, err := os.Lstat(full); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s: %w", rel, ErrLinkTarget)
	}
	return full, nil
}

// checkWithin is the loaders' guard: a path outside root is an error; one that
// is merely missing is left for the loader (git still diffs a deleted file).
func checkWithin(root, rel string) error {
	if _, err := ResolveWithin(root, rel); errors.Is(err, ErrOutsideRoot) {
		return err
	}
	return nil
}
