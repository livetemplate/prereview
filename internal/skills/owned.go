// Package skills turns accepted learnings into prereview-owned agent skills
// and checks (checks M4), installs them where each agent loads skills, and
// imports the skills you already have — by reference: prereview never writes a
// file it didn't create.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/prereview/internal/library"
)

// AgentHome is the root prereview writes agent integrations under — its own
// skill (--install-skill, the startup sync) and generated skills: $PREREVIEW_HOME,
// else $HOME. Tests set it so no run ever rewrites the real agent folders.
func AgentHome() (string, error) {
	if d := os.Getenv("PREREVIEW_HOME"); d != "" {
		return d, nil
	}
	return os.UserHomeDir()
}

// OwnedFile records, per absolute path, the hash of the bytes prereview last
// wrote there. A file whose content still has that hash is prereview's to
// rewrite or remove; anything else was made or changed by someone else.
const OwnedFile = "owned.json"

// Write outcomes.
var (
	// ErrNoRoot: the agent's skills root doesn't exist (the agent isn't set up
	// here) — prereview never creates an agent's folder.
	ErrNoRoot = errors.New("not set up on this machine")
	// ErrForeign: a file prereview didn't write is at that path.
	ErrForeign = errors.New("a file prereview didn't write is already there")
	// ErrEdited: prereview wrote it, but it has been edited since.
	ErrEdited = errors.New("edited since prereview wrote it")
)

// Owner is the ownership record, read and written under the library lock.
type Owner struct {
	hashes map[string]string
	dirty  bool
}

// Own runs fn with the ownership record loaded under the library lock and saves
// it after: every write and its hash land together, so another server can never
// read a file before its new hash is recorded (and mistake it for a hand edit).
func Own(libDir string, fn func(*Owner) error) error {
	return editJSON(libDir, OwnedFile, func(h *map[string]string) (bool, error) {
		if *h == nil {
			*h = map[string]string{}
		}
		o := &Owner{hashes: *h}
		err := fn(o)
		return o.dirty, err
	})
}

// status is a path's ownership given its current bytes: nil when it's
// prereview's, else ErrForeign / ErrEdited.
func (o *Owner) status(path string, cur []byte) error {
	want, ok := o.hashes[path]
	switch {
	case !ok:
		return ErrForeign
	case want != hash(cur):
		return ErrEdited
	}
	return nil
}

// Write puts data at path inside an agent's skills root when prereview may: the
// root already exists (prereview never creates an agent's folder, only its own
// skill folder inside it), and the file is absent or still exactly what
// prereview last wrote. It reports whether it changed anything.
func (o *Owner) Write(root, path string, data []byte) (bool, error) {
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return false, ErrNoRoot
	}
	return o.WriteIn(root, path, data)
}

// WriteIn is Write for prereview's own folders (the library's skill sources,
// the learned checks): base may be created, but path must stay inside it.
func (o *Owner) WriteIn(base, path string, data []byte) (bool, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !within(base, path) {
		return false, errors.New("refusing a path outside " + base + ": " + path)
	}
	if cur, err := os.ReadFile(path); err == nil {
		if err := o.status(path, cur); err != nil {
			return false, err
		}
		if string(cur) == string(data) {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := library.WriteFileAtomic(path, data, 0o644); err != nil {
		return false, err
	}
	o.hashes[path], o.dirty = hash(data), true
	return true, nil
}

// Remove deletes a file prereview wrote and nobody changed since (and its own
// folder when that leaves it empty). A file that isn't prereview's is left.
func (o *Owner) Remove(path string) (bool, error) {
	if _, ok := o.hashes[path]; !ok {
		return false, nil
	}
	if cur, err := os.ReadFile(path); err == nil {
		if err := o.status(path, cur); err != nil {
			return false, err
		}
	}
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	delete(o.hashes, path)
	o.dirty = true
	_ = os.Remove(filepath.Dir(path)) // only succeeds when empty
	return err == nil, nil
}

// Reclaim overwrites a file prereview wrote that has been edited since — only
// when you chose to discard the edit and regenerate. A file prereview never
// wrote is still refused.
func (o *Owner) Reclaim(path string, data []byte) error {
	if _, ok := o.hashes[path]; !ok {
		return ErrForeign
	}
	if err := library.WriteFileAtomic(path, data, 0o644); err != nil {
		return err
	}
	o.hashes[path], o.dirty = hash(data), true
	return nil
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// within reports whether path is inside dir.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// readJSON reads one of the library's skills/*.json records (zero when absent).
func readJSON[T any](lib, name string) T {
	var v T
	if b, err := os.ReadFile(filepath.Join(lib, "skills", name)); err == nil {
		_ = json.Unmarshal(b, &v)
	}
	return v
}

// editJSON is a skills/*.json read-modify-write under the library lock; fn
// reports whether it changed the record (an unchanged one isn't rewritten).
func editJSON[T any](lib, name string, fn func(*T) (bool, error)) error {
	unlock, err := library.Lock(lib)
	if err != nil {
		return err
	}
	defer unlock()
	v := readJSON[T](lib, name)
	changed, ferr := fn(&v)
	if !changed {
		return ferr
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(lib, "skills"), 0o755); err != nil {
		return err
	}
	if err := library.WriteFileAtomic(filepath.Join(lib, "skills", name), append(b, '\n'), 0o644); err != nil {
		return err
	}
	return ferr
}
