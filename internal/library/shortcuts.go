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

func loadShortcutSettings(dir string) shortcutSettings {
	var s shortcutSettings
	if b, err := os.ReadFile(filepath.Join(dir, ShortcutsFile)); err == nil {
		_ = json.Unmarshal(b, &s) // a torn file reads as "nothing hidden"
	}
	return s
}

// HiddenShortcuts is the set of shortcut slugs the reviewer hid from the composer.
func HiddenShortcuts(dir string) map[string]bool {
	out := map[string]bool{}
	for _, slug := range loadShortcutSettings(dir).Hidden {
		out[slug] = true
	}
	return out
}

// SetShortcutHidden hides or shows slug, a read-modify-write under the library lock.
func SetShortcutHidden(dir, slug string, hidden bool) error {
	unlock, err := lockLibrary(dir)
	if err != nil {
		return err
	}
	defer unlock()
	s := loadShortcutSettings(dir)
	s.Hidden = slices.DeleteFunc(s.Hidden, func(h string) bool { return h == slug })
	if hidden {
		s.Hidden = append(s.Hidden, slug)
	}
	slices.Sort(s.Hidden)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(dir, ShortcutsFile), append(b, '\n'), 0o644)
}
