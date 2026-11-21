package review

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// FirstSeenFileName is the server-owned record of the diff fingerprint each piece
// of agent-written text was first seen against, keyed "<kind>/<id>" (explain/…
// today; the A′ whole-change quiz is next). The agent can't stamp this itself: its
// commands don't know the review's base. A key is written once and never moved,
// so comparing it with the live fingerprint says whether the files changed after
// the text was written.
const FirstSeenFileName = "first-seen.json"

func firstSeenPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), FirstSeenFileName)
}

// stampFirstSeen records fp for each key not yet seen and returns each key's
// first-seen fingerprint. Missing or torn reads as empty, and the write is
// atomic, so a crash can't leave a torn file behind.
func (c *PrereviewController) stampFirstSeen(keys []string, fp string) map[string]string {
	c.firstSeenMu.Lock()
	defer c.firstSeenMu.Unlock()
	path := firstSeenPath(c.CSVPath)
	seen := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &seen)
	}
	changed := false
	for _, k := range keys {
		if seen[k] == "" {
			seen[k] = fp
			changed = true
		}
	}
	if changed {
		b, _ := json.Marshal(seen) // a map[string]string always marshals
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			slog.Warn("first-seen: mkdir", "err", err)
		} else if err := writeFileAtomicVersions(path, b); err != nil {
			slog.Warn("first-seen: save", "err", err)
		}
	}
	return seen
}
