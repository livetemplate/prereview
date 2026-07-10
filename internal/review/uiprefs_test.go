package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestUIPrefsRoundTrip covers the happy path plus the two tolerance cases a
// prefs file must never crash on: a missing file (first run) and a torn/corrupt
// write. Both must yield the zero value (all defaults), not an error.
func TestUIPrefsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "ui-prefs.json") // sub/ tests MkdirAll

	// Missing file → defaults.
	if got := loadUIPrefs(path); got != (UIPrefs{}) {
		t.Fatalf("missing file: want zero UIPrefs, got %+v", got)
	}

	want := UIPrefs{
		ShowResolved: true,
		SchemeName:   "gruvbox",
		ThemeMode:    "dark",
		FocusMode:    true,
		FileView:     true,
		RawMarkdown:  true,
		RawHTML:      true,
	}
	if err := saveUIPrefs(path, want); err != nil {
		t.Fatalf("saveUIPrefs: %v", err)
	}
	if got := loadUIPrefs(path); got != want {
		t.Fatalf("round-trip mismatch:\n want %+v\n got  %+v", want, got)
	}

	// Torn/corrupt write → defaults (next save self-heals).
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadUIPrefs(path); got != (UIPrefs{}) {
		t.Fatalf("corrupt file: want zero UIPrefs, got %+v", got)
	}

	// Empty path disables durable prefs without error.
	if got := loadUIPrefs(""); got != (UIPrefs{}) {
		t.Fatalf(`loadUIPrefs(""): want zero, got %+v`, got)
	}
	if err := saveUIPrefs("", want); err != nil {
		t.Fatalf(`saveUIPrefs(""): want nil, got %v`, err)
	}
}

// TestSavePrefsWritesOnlyViewFields is the guard for the advisor's correctness
// caveat: only the global view-style fields belong in the prefs file — the
// per-repo Base (and every other lvt:"persist" field) must NOT be swept in, or it
// would corrupt across repos. UIPrefs has no Base field, so the file can never
// carry one; this asserts the on-disk JSON key set stays exactly the view fields.
func TestSavePrefsWritesOnlyViewFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui-prefs.json")
	c := &PrereviewController{UIPrefsPath: path}
	c.savePrefs(PrereviewState{
		ShowResolved: true,
		SchemeName:   "catppuccin",
		Base:         "HEAD~3", // per-repo — must not leak into the file
		SelectedFile: "a.go",   // session continuity — must not leak either
	})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prefs: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal prefs: %v", err)
	}
	wantKeys := map[string]bool{
		"show_resolved": true, "scheme_name": true, "theme_mode": true,
		"focus_mode": true, "hide_marks": true, "file_view": true,
		"raw_markdown": true, "raw_html": true,
	}
	for k := range m {
		if !wantKeys[k] {
			t.Errorf("prefs file leaked a non-view field: %q", k)
		}
	}
	if len(m) != len(wantKeys) {
		t.Errorf("prefs file has %d keys, want %d: %v", len(m), len(wantKeys), m)
	}
	if _, ok := m["base"]; ok {
		t.Error("prefs file must never contain per-repo Base")
	}
}

// TestApplyUIPrefsOverlays confirms applyUIPrefs sources the seven fields from
// disk and leaves per-repo/session fields (Base) untouched.
func TestApplyUIPrefsOverlays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui-prefs.json")
	if err := saveUIPrefs(path, UIPrefs{ShowResolved: true, SchemeName: "gruvbox", ThemeMode: "light"}); err != nil {
		t.Fatal(err)
	}
	c := &PrereviewController{UIPrefsPath: path}
	st := PrereviewState{Base: "HEAD~5", ShowResolved: false, SchemeName: "solarized"}
	c.applyUIPrefs(&st)

	if !st.ShowResolved || st.SchemeName != "gruvbox" || st.ThemeMode != "light" {
		t.Errorf("applyUIPrefs did not overlay disk prefs: %+v", st)
	}
	if st.Base != "HEAD~5" {
		t.Errorf("applyUIPrefs clobbered per-repo Base: %q", st.Base)
	}
}
