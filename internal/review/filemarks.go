package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
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
//   - last-look.json: what each file showed the last time it was on screen. Its
//     content is kept as a blob in the version store, so a later look can mark
//     what changed since (versions cover only changed files).
//
// One writer each (the server), written atomically.
const (
	ViewedFileName   = "viewed.json"
	LastLookFileName = "last-look.json"
)

// FileMark is what one file held when the reviewer marked or looked at it.
type FileMark struct {
	SHA string    `json:"sha256"`
	At  time.Time `json:"at,omitempty"`
}

func (c *PrereviewController) viewedPath() string {
	return filepath.Join(filepath.Dir(c.CSVPath), ViewedFileName)
}

func (c *PrereviewController) lastLookPath() string {
	return filepath.Join(filepath.Dir(c.CSVPath), LastLookFileName)
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

// recordLook notes that the open file's current content is on screen (#115 B4):
// opening it, a Refresh, a reconnect. It keeps the content as a blob and records
// it in last-look.json. When the file held something else at the previous look,
// that look becomes this session's baseline (SinceLook), kept across reconnects
// until the reviewer dismisses it, so the changes since stay marked.
func (c *PrereviewController) recordLook(state *PrereviewState) {
	file := state.SelectedFile
	if file == "" || c.Versions == nil || state.ViewingVersion {
		return
	}
	full, err := gitdiff.ResolveWithin(c.RepoPath, file)
	if err != nil {
		return
	}
	sha, err := c.Versions.SaveBlob(full)
	if err != nil || sha == "" {
		return
	}
	marks := loadFileMarks(c.lastLookPath())
	prev := marks[file]
	switch base, held := state.SinceLook[file]; {
	case held && base.SHA == sha:
		delete(state.SinceLook, file) // back to what it was: nothing changed since
	case !held && prev.SHA != "" && prev.SHA != sha:
		if state.SinceLook == nil {
			state.SinceLook = map[string]FileMark{}
		}
		state.SinceLook[file] = prev
	}
	if prev.SHA != sha {
		marks[file] = FileMark{SHA: sha, At: time.Now().UTC()}
		if err := saveFileMarks(c.lastLookPath(), marks); err != nil {
			slog.Warn("record look", "file", file, "err", err)
		}
	}
	c.applySinceLook(state)
}

// applySinceLook works out which of the open file's lines are new since its
// baseline look: the added lines of a diff from that look's blob to the file now,
// and how many lines were removed. Once per show, never per render.
func (c *PrereviewController) applySinceLook(state *PrereviewState) {
	state.SinceLookAdded, state.SinceLookRemoved = nil, 0
	base, held := state.SinceLook[state.SelectedFile]
	if !held || c.Versions == nil {
		return
	}
	old, err := c.Versions.Blob(base.SHA)
	if err != nil {
		return
	}
	full, err := gitdiff.ResolveWithin(c.RepoPath, state.SelectedFile)
	if err != nil {
		return
	}
	cur, err := os.ReadFile(full)
	if err != nil {
		return
	}
	for _, l := range gitdiff.DiffContentsNoHighlight(state.SelectedFile, old, cur).Lines {
		switch l.Kind {
		case "add":
			state.SinceLookAdded = append(state.SinceLookAdded, l.NewNum)
		case "del":
			state.SinceLookRemoved++
		}
	}
}
