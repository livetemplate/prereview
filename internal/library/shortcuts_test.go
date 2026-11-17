package library

import "testing"

func TestShortcutUsesAndHidden(t *testing.T) {
	dir := t.TempDir()
	if ShortcutUseCounts(dir) != nil || len(HiddenShortcuts(dir)) != 0 {
		t.Fatal("a fresh library has no uses and nothing hidden")
	}
	for _, s := range []string{"a", "b", "a"} {
		if err := RecordShortcutUse(dir, s); err != nil {
			t.Fatal(err)
		}
	}
	if c := ShortcutUseCounts(dir); c["a"] != 2 || c["b"] != 1 {
		t.Errorf("counts = %v", c)
	}
	if err := SetShortcutHidden(dir, "a", true); err != nil {
		t.Fatal(err)
	}
	if err := SetShortcutHidden(dir, "a", true); err != nil { // idempotent
		t.Fatal(err)
	}
	if h := HiddenShortcuts(dir); !h["a"] || len(h) != 1 {
		t.Errorf("hidden = %v", h)
	}
	if err := SetShortcutHidden(dir, "a", false); err != nil {
		t.Fatal(err)
	}
	if len(HiddenShortcuts(dir)) != 0 {
		t.Error("showing it again must clear the hide")
	}
}
