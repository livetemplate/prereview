package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/livetemplate/prereview/gitdiff"
)

// filemarks.go keeps what the reviewer has seen of each file (#115 B3, B4), keyed
// by the content's hash, in server-owned files under the store:
//
//   - viewed.json: the files marked viewed, and what each held then. A file is
//     viewed while its content still matches; after an edit the drawer says it
//     changed since you viewed it, and undoing the edit makes it viewed again.
//
// One writer (the server), written atomically.
const ViewedFileName = "viewed.json"

// FileMark is what one file held when the reviewer marked or looked at it.
type FileMark struct {
	SHA string    `json:"sha256"`
	At  time.Time `json:"at,omitempty"`
}

func (c *PrereviewController) viewedPath() string {
	return filepath.Join(filepath.Dir(c.CSVPath), ViewedFileName)
}

// loadFileMarks reads a marks file; missing or unreadable is empty.
func loadFileMarks(path string) map[string]FileMark {
	out := map[string]FileMark{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func saveFileMarks(path string, m map[string]FileMark) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomicVersions(path, b)
}

// fileSHA is the hex SHA-256 of path's working-tree content, "" when it can't be
// read (deleted, outside the repo).
func (c *PrereviewController) fileSHA(path string) string {
	full, err := gitdiff.ResolveWithin(c.RepoPath, path)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// applyViewed derives which marked files are still viewed and which changed
// since: each marked file is hashed now and compared. Called on Mount (which
// Refresh reuses), so the drawer agrees with the diff on screen.
func (c *PrereviewController) applyViewed(state *PrereviewState) {
	state.ViewedFiles, state.ChangedSinceViewed = map[string]bool{}, map[string]bool{}
	for path, m := range loadFileMarks(c.viewedPath()) {
		if c.fileSHA(path) == m.SHA {
			state.ViewedFiles[path] = true
		} else {
			state.ChangedSinceViewed[path] = true
		}
	}
}
