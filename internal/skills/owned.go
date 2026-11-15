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
	"sort"

	"github.com/livetemplate/prereview/internal/library"
)

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
	libDir string
	hashes map[string]string
	dirty  bool
}

// Own runs fn with the ownership record loaded under the library lock and saves
// it after: every write and its hash land together, so another server can never
// read a file before its new hash is recorded (and mistake it for a hand edit).
func Own(libDir string, fn func(*Owner) error) error {
	unlock, err := library.Lock(libDir)
	if err != nil {
		return err
	}
	defer unlock()
	o := &Owner{libDir: libDir, hashes: map[string]string{}}
	if b, err := os.ReadFile(o.path()); err == nil {
		_ = json.Unmarshal(b, &o.hashes)
	}
	ferr := fn(o)
	if o.dirty {
		if err := o.save(); err != nil && ferr == nil {
			ferr = err
		}
	}
	return ferr
}

func (o *Owner) path() string { return filepath.Join(o.libDir, "skills", OwnedFile) }

func (o *Owner) save() error {
	if err := os.MkdirAll(filepath.Dir(o.path()), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(o.hashes, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(o.path(), append(b, '\n'), 0o644)
}

// Status reports a path's ownership without changing it: nil when it's
// prereview's (or absent), else ErrForeign / ErrEdited.
func (o *Owner) Status(path string) error {
	cur, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	want, ok := o.hashes[path]
	switch {
	case !ok:
		return ErrForeign
	case want != hash(cur):
		return ErrEdited
	}
	return nil
}

// Write puts data at path when prereview may: the file is absent or still
// exactly what prereview last wrote. root, when set, must already exist (an
// agent's skills folder: prereview creates only its own folder inside it). It
// reports whether it changed anything.
func (o *Owner) Write(root, path string, data []byte) (bool, error) {
	if root != "" {
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			return false, ErrNoRoot
		}
	}
	if cur, err := os.ReadFile(path); err == nil {
		if err := o.Status(path); err != nil {
			return false, err
		}
		if string(cur) == string(data) {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := atomicWrite(path, data, 0o644); err != nil {
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
	if err := o.Status(path); err != nil {
		return false, err
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

// Paths lists every path prereview owns, sorted.
func (o *Owner) Paths() []string {
	out := make([]string, 0, len(o.hashes))
	for p := range o.hashes {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Owns reports whether prereview recorded writing path.
func (o *Owner) Owns(path string) bool { _, ok := o.hashes[path]; return ok }

// Reclaim overwrites a file prereview wrote that has been edited since — only
// when you chose to discard the edit and regenerate. A file prereview never
// wrote is still refused.
func (o *Owner) Reclaim(path string, data []byte) error {
	if !o.Owns(path) {
		return ErrForeign
	}
	if err := atomicWrite(path, data, 0o644); err != nil {
		return err
	}
	o.hashes[path], o.dirty = hash(data), true
	return nil
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// atomicWrite replaces path via a temp file + rename beside it.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
