// Package skills turns accepted learnings into prereview-owned agent skills
// and checks (checks M4), installs them where each agent loads skills, and
// imports the skills you already have — by reference: prereview never writes a
// file it didn't create, except for one marked region it owns inside a file of
// yours (a memory-file block, a settings hook entry: see Region).
package skills

import (
	"crypto/sha256"
	"encoding/hex"
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
	// ErrNoLibrary: there is no library folder to keep the record in (no config
	// dir and no $PREREVIEW_LIBRARY_DIR).
	ErrNoLibrary = errors.New("no library folder to record what prereview wrote (set PREREVIEW_LIBRARY_DIR)")
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
	if libDir == "" {
		return ErrNoLibrary
	}
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
	if !isDir(root) {
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
	return o.Update(path, data, 0o644)
}

// Update writes data at path when the file is absent or still exactly what
// prereview last wrote, and reports whether it changed anything; mode is a new
// file's permission. Otherwise ErrForeign (prereview never wrote it) or
// ErrEdited (edited since). The caller vouches for path; WriteIn and Write are
// the guarded forms.
func (o *Owner) Update(path string, data []byte, mode os.FileMode) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil {
		if err := o.status(path, cur); err != nil {
			return false, err
		}
		if string(cur) == string(data) {
			return false, nil
		}
	}
	return true, o.Put(path, data, mode)
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
	return o.Put(path, data, 0o644)
}

// Put writes path whatever is there and records it as prereview's: the explicit
// install (`--install-skill`), which is also how you discard an edit. Its folder
// is created.
func (o *Owner) Put(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := library.WriteFileAtomic(path, data, mode); err != nil {
		return err
	}
	o.hashes[path], o.dirty = hash(data), true
	return nil
}

// Region is a part of a file you own that prereview writes: a block in an
// agent's memory file, prereview's entry in Claude Code's settings. Its hash is
// recorded under "<path>#<name>", which no file path can be, so the sweep for
// files never touches it. Unlike a file, a region with no record is taken
// over: it is found by prereview's own marker, so it is prereview's.
func (o *Owner) Region(path, name string) Region { return Region{o, path + "#" + name} }

// Region is one owned region (Owner.Region).
type Region struct {
	o   *Owner
	key string
}

// Edited reports whether the region's current bytes differ from what prereview
// last wrote there (false with no record: a region is taken over).
func (r Region) Edited(cur []byte) bool {
	want, ok := r.o.hashes[r.key]
	return ok && want != hash(cur)
}

// Set records data as the region's bytes; "" forgets the region. The record is
// marked for saving only when this changes it.
func (r Region) Set(data string) {
	old, had := r.o.hashes[r.key]
	switch {
	case data == "" && had:
		delete(r.o.hashes, r.key)
	case data == "" || (had && old == hash([]byte(data))):
		return
	default:
		r.o.hashes[r.key] = hash([]byte(data))
	}
	r.o.dirty = true
}

// isDir reports whether dir exists and is a folder: an agent is set up here.
func isDir(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
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
	return library.ReadJSON[T](filepath.Join(lib, "skills", name))
}

// editJSON is a skills/*.json read-modify-write under the library lock; fn
// reports whether it changed the record (an unchanged one isn't rewritten).
func editJSON[T any](lib, name string, fn func(*T) (bool, error)) error {
	return library.EditJSON(lib, filepath.Join(lib, "skills", name), fn)
}
