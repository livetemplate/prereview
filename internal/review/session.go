package review

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SessionFileName records the CURRENT session's review scope inside the store (#171).
//
// The .prereview/ store is keyed to a DIRECTORY (for a single-file review, the file's
// parent — see resolveTarget), so it is shared by every file ever reviewed from there.
// The server knows the session's scope in memory, but the agent subcommands don't: they
// are handed `--out <dir>` and nothing else, so `prereview comments` / `done` would list
// the whole directory's rows while the reviewer's UI shows one file. This file is how
// that scope reaches them.
//
// It is a PER-SESSION file: openStore removes it every launch (like the event log, the
// agent-status file and the paused marker), and the server rewrites it only in
// single-file mode. That reset is load-bearing — a leftover from an earlier single-file
// run would otherwise scope a later DIRECTORY review down to one file: the same bug,
// inverted.
const SessionFileName = "session.json"

// SessionPath returns the session-scope file living beside the review's CSV.
func SessionPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), SessionFileName)
}

type sessionScope struct {
	// SingleFile is the one file under review, relative to the store's directory.
	// Absent/empty means a directory or git review, which scopes to nothing.
	SingleFile string `json:"single_file,omitempty"`
}

// WriteSessionScope records singleFile as this session's scope. A "" scope writes
// nothing: a directory review narrows nothing, and the absent file is exactly what
// SessionScope reads as "unscoped".
func WriteSessionScope(csvPath, singleFile string) error {
	if singleFile == "" {
		return nil
	}
	b, err := json.Marshal(sessionScope{SingleFile: singleFile})
	if err != nil {
		return err
	}
	return os.WriteFile(SessionPath(csvPath), b, 0o644)
}

// SessionScope reports the file this session's review is scoped to, or "" when the
// review spans the whole store (a directory / git review, or a store written by a
// version of prereview that predates this file).
//
// Every failure reads as unscoped. That is the safe direction: an unreadable scope
// shows the reviewer MORE than they asked for, which is the pre-#171 behaviour — never
// less, which would look like their comments had vanished.
func SessionScope(csvPath string) string {
	b, err := os.ReadFile(SessionPath(csvPath))
	if err != nil {
		return ""
	}
	var s sessionScope
	if err := json.Unmarshal(b, &s); err != nil {
		return ""
	}
	return s.SingleFile
}
