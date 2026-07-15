package review

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// uiprefs.go persists the durable, per-user view-style preferences that mean the
// same thing in every repo (theme scheme, light/dark mode, focus, file-view, the
// raw-source toggles, and show-resolved).
//
// Why a file instead of the framework's lvt:"persist" session store: that store
// is an in-memory SessionStore keyed by a browser cookie. A page reload keeps it,
// but prereview is a CLI the user relaunches constantly (often from a phone) —
// every relaunch spawns a fresh empty store, so the old cookie misses and EVERY
// persisted view pref resets to its zero value. Writing these prefs to a per-user
// file on disk bypasses the session entirely, so a preference set on the phone
// survives a page reload AND a server relaunch — the same "disk is the source of
// truth" contract comments.csv already relies on.
//
// Only genuinely global view preferences live here. Per-repo / per-session state
// (Base, SelectedFile, DraftBody, EditingCommentID, selection ranges, …) stays
// lvt:"persist": that is session-continuity across a reconnect, not a durable
// user preference — and Base in particular (`HEAD~3`) is meaningless across repos.
type UIPrefs struct {
	ShowResolved bool   `json:"show_resolved"`
	SchemeName   string `json:"scheme_name"`
	ThemeMode    string `json:"theme_mode"`
	FocusMode    bool   `json:"focus_mode"`
	// TocCollapsed hides the desktop table-of-contents column (#137). A durable
	// desktop reading preference like FocusMode — its per-side twin — so it lives
	// here, not in the relaunch-wiped session store.
	TocCollapsed bool `json:"toc_collapsed"`
	HideMarks    bool `json:"hide_marks"`
	FileView     bool `json:"file_view"`
	RawMarkdown  bool `json:"raw_markdown"`
	RawHTML      bool `json:"raw_html"`
	// QueueGlobal shows the whole review's work in the queue panel instead of just
	// the current file's (#171). Default false = this file — the queue answers "what
	// is happening to the document in front of me"; flip it to see the review-wide
	// backlog. A view preference like the rest, so it belongs here and not in the
	// session store.
	QueueGlobal bool `json:"queue_global"`
}

// uiPrefsMu serialises writes to the prefs file — multiple tabs (sharing one
// per-user file) can toggle concurrently.
var uiPrefsMu sync.Mutex

// loadUIPrefs reads the prefs file. A missing file (first run) or an
// unreadable/torn one yields the zero value (all defaults): a preferences file
// must never break a review session, so no error is surfaced to the UI — the
// next saveUIPrefs self-heals a corrupt file.
func loadUIPrefs(path string) UIPrefs {
	var p UIPrefs
	if path == "" {
		return p
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return p // missing (first run) or unreadable → defaults
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return UIPrefs{} // torn/partial write → defaults; next save rewrites it
	}
	return p
}

// saveUIPrefs atomically writes the prefs file (temp + rename in the same dir)
// so a concurrent reader never observes a partial document, mirroring the CSV
// writer's durability contract.
func saveUIPrefs(path string, p UIPrefs) error {
	if path == "" {
		return nil
	}
	uiPrefsMu.Lock()
	defer uiPrefsMu.Unlock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".ui-prefs-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

// applyUIPrefs overlays the durable per-user view preferences onto state. Called
// on every Mount and OnConnect (exactly like applyLLMStatus) so these fields are
// always sourced from disk — the single source of truth — regardless of what the
// relaunch-wiped session store held. Dropped fields would otherwise come back
// zeroed on a reconnect.
func (c *PrereviewController) applyUIPrefs(state *PrereviewState) {
	p := loadUIPrefs(c.UIPrefsPath)
	state.ShowResolved = p.ShowResolved
	state.SchemeName = p.SchemeName
	state.ThemeMode = p.ThemeMode
	state.FocusMode = p.FocusMode
	state.TocCollapsed = p.TocCollapsed
	state.HideMarks = p.HideMarks
	state.FileView = p.FileView
	state.RawMarkdown = p.RawMarkdown
	state.RawHTML = p.RawHTML
	state.QueueGlobal = p.QueueGlobal
}

// savePrefs persists the current view-style preferences to the per-user prefs
// file. Every view-toggle action calls it so the choice survives reload and
// relaunch. Best-effort: a write error is logged, not surfaced — a failed prefs
// write must not fail the toggle the user just made.
func (c *PrereviewController) savePrefs(state PrereviewState) {
	if err := saveUIPrefs(c.UIPrefsPath, UIPrefs{
		ShowResolved: state.ShowResolved,
		SchemeName:   state.SchemeName,
		ThemeMode:    state.ThemeMode,
		FocusMode:    state.FocusMode,
		TocCollapsed: state.TocCollapsed,
		HideMarks:    state.HideMarks,
		FileView:     state.FileView,
		RawMarkdown:  state.RawMarkdown,
		RawHTML:      state.RawHTML,
		QueueGlobal:  state.QueueGlobal,
	}); err != nil {
		slog.Warn("savePrefs", "err", err)
	}
}
