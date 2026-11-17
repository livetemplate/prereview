package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

// Shortcut files (#204), in Dir(): a tap log that orders the composer's chips by
// use, and the set of shortcuts the reviewer hid from them.
const (
	ShortcutUsesFile = "shortcut-uses.jsonl"
	ShortcutsFile    = "shortcuts.json"
)

// ShortcutUse is one tap on a shortcut chip.
type ShortcutUse struct {
	Slug    string `json:"slug"`
	At      string `json:"at"`
	Machine string `json:"machine"`
}

// RecordShortcutUse appends one tap on slug.
func RecordShortcutUse(dir, slug string) error {
	return Append(filepath.Join(dir, ShortcutUsesFile), ShortcutUse{Slug: slug, At: Now(), Machine: Machine()})
}

// ShortcutUseCounts is how many times each shortcut was tapped; nil when none were.
func ShortcutUseCounts(dir string) map[string]int {
	var out map[string]int
	eachLine(filepath.Join(dir, ShortcutUsesFile), func(b []byte) {
		var u ShortcutUse
		if json.Unmarshal(b, &u) != nil || u.Slug == "" {
			return
		}
		if out == nil {
			out = map[string]int{}
		}
		out[u.Slug]++
	})
	return out
}

type shortcutSettings struct {
	Hidden []string `json:"hidden"`
}

// HiddenShortcuts is the set of shortcut slugs the reviewer hid from the composer.
func HiddenShortcuts(dir string) map[string]bool {
	var out map[string]bool
	for _, slug := range ReadJSON[shortcutSettings](filepath.Join(dir, ShortcutsFile)).Hidden {
		if out == nil {
			out = map[string]bool{}
		}
		out[slug] = true
	}
	return out
}

// SetShortcutHidden hides or shows slug.
func SetShortcutHidden(dir, slug string, hidden bool) error {
	return EditJSON(dir, filepath.Join(dir, ShortcutsFile), func(s *shortcutSettings) (bool, error) {
		was := slices.Contains(s.Hidden, slug)
		if was == hidden {
			return false, nil
		}
		s.Hidden = slices.DeleteFunc(s.Hidden, func(h string) bool { return h == slug })
		if hidden {
			s.Hidden = append(s.Hidden, slug)
			slices.Sort(s.Hidden)
		}
		return true, nil
	})
}

// ReadJSON reads a JSON record (the zero value when it's absent or torn).
func ReadJSON[T any](path string) T {
	var v T
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &v)
	}
	return v
}

// EditJSON is a read-modify-write of the JSON record at path under the lock of the
// library at dir. fn reports whether it changed the record; an unchanged one isn't
// rewritten.
func EditJSON[T any](dir, path string, fn func(*T) (bool, error)) error {
	unlock, err := lockLibrary(dir)
	if err != nil {
		return err
	}
	defer unlock()
	v := ReadJSON[T](path)
	changed, ferr := fn(&v)
	if !changed {
		return ferr
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := WriteFileAtomic(path, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return ferr
}
