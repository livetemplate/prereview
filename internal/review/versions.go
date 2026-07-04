package review

// versions.go is the artifact version store (#90): a dependency-free,
// content-addressed snapshot log of the reviewed files as the LLM edits them on
// disk. It is the safety net that makes continuous auto-apply reversible — the
// reviewer can see prior versions of a file and roll back — WITHOUT committing to
// git, and it works identically on git repos, non-git dirs, and single files.
//
// Storage layout under <store>/.prereview/versions/:
//
//	blobs/<sha256>   deduplicated file contents (unchanged file reuses its blob)
//	timeline.jsonl   append-only Checkpoint log, one JSON object per line
//
// A Checkpoint captures the whole review scope at one moment; a single file's
// history is the subsequence of checkpoints where that path's content changed.
// The store is pure (no controller/git dependency) so it unit-tests against a
// temp dir; the controller decides WHEN to Checkpoint (baseline at startup,
// llm-done via the status watcher, rollback via RestoreVersion).

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Version triggers recorded on each Checkpoint. Baseline is the v0 snapshot of
// the working tree at server start; LLMDone fires when the agent finishes a
// batch (status working→done); Rollback is written by RestoreVersion so a
// rollback is itself a new, append-only version (history is never rewritten).
const (
	VersionTriggerBaseline = "baseline"
	VersionTriggerLLMDone  = "llm-done"
	VersionTriggerRollback = "rollback"
)

// ErrVersionTombstone is returned by Restore when the requested version is a
// tombstone (the file did not exist at that checkpoint) — the caller restores
// "deleted" by removing the working file rather than writing bytes.
var ErrVersionTombstone = errors.New("version is a tombstone (file absent at checkpoint)")

// FileRef identifies one file to snapshot: Path is the stable timeline key
// (repo-relative, slash-separated) and AbsPath is where to read the bytes now.
// Keeping the two separate lets the timeline key stay stable across absolute
// path differences (single-file review stores by basename, etc.).
type FileRef struct {
	Path    string
	AbsPath string
}

// FileVersion is one file's recorded state within a Checkpoint. SHA == "" is a
// tombstone: the file was absent (deleted) when the checkpoint was taken. A
// file's previous content is derivable from the prior checkpoint, so it isn't
// stored here.
type FileVersion struct {
	Path string `json:"path"`
	SHA  string `json:"sha"`
}

// Checkpoint is one append-only entry in the timeline. Seq is its 0-based
// position (also its index in the log). Files lists every scope file's state at
// that moment.
type Checkpoint struct {
	Seq     int           `json:"seq"`
	TS      time.Time     `json:"ts"`
	Trigger string        `json:"trigger"`
	Files   []FileVersion `json:"files"`
}

// FileHistoryEntry is one point in a single file's timeline: the checkpoint at
// which the file took on content SHA. Emitted only for checkpoints where the
// file actually changed (or first appeared).
type FileHistoryEntry struct {
	Seq     int
	TS      time.Time
	Trigger string
	SHA     string
}

// VersionStore is the append-only content-snapshot store rooted at a versions
// directory. Safe for concurrent use: the mutex serialises the read-modify-append
// of the timeline (the watcher goroutine, Mount, and RestoreVersion all call in).
type VersionStore struct {
	root string
	now  func() time.Time
	mu   sync.Mutex
}

// NewVersionStore opens (creating if needed) a version store at root, which is
// <store>/.prereview/versions. It never wipes existing history — the store is
// the uncommitted version log and must survive server restarts.
func NewVersionStore(root string) (*VersionStore, error) {
	if err := os.MkdirAll(filepath.Join(root, "blobs"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir version store: %w", err)
	}
	return &VersionStore{root: root, now: time.Now}, nil
}

// VersionsDir returns the version-store directory for a store whose CSV lives at
// csvPath — i.e. <csv dir>/versions, alongside events.jsonl and llm-status.json.
// Centralised (like LLMStatusPath) so launch wiring and any future caller agree
// on one location, including single-file reviews where the store dir is the
// file's parent.
func VersionsDir(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), "versions")
}

func (s *VersionStore) timelinePath() string       { return filepath.Join(s.root, "timeline.jsonl") }
func (s *VersionStore) blobPath(sha string) string { return filepath.Join(s.root, "blobs", sha) }

// Checkpoint snapshots every ref and appends a timeline entry. Each file is
// hashed; unchanged content reuses its existing blob (dedup). A ref whose file
// is absent is recorded as a tombstone (SHA "") rather than failing the whole
// checkpoint — a deleted file is still restorable state. To keep the timeline
// meaningful, a checkpoint whose scope is byte-identical to the previous one is
// skipped (created=false) — EXCEPT the very first (baseline), which always lands.
func (s *VersionStore) Checkpoint(refs []FileRef, trigger string) (cp Checkpoint, created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cps, err := s.checkpointsLocked()
	if err != nil {
		return Checkpoint{}, false, err
	}
	prev := map[string]string{}
	if n := len(cps); n > 0 {
		for _, fv := range cps[n-1].Files {
			prev[fv.Path] = fv.SHA
		}
	}

	files := make([]FileVersion, 0, len(refs))
	changed := false
	for _, ref := range refs {
		sha, werr := s.writeBlob(ref.AbsPath)
		if werr != nil {
			return Checkpoint{}, false, werr
		}
		if sha != prev[ref.Path] {
			changed = true
		}
		files = append(files, FileVersion{Path: ref.Path, SHA: sha})
	}

	if !changed && len(cps) > 0 {
		return cps[len(cps)-1], false, nil
	}

	cp = Checkpoint{Seq: len(cps), TS: s.now().UTC(), Trigger: trigger, Files: files}
	if err := s.appendCheckpointLocked(cp); err != nil {
		return Checkpoint{}, false, err
	}
	return cp, true, nil
}

// writeBlob reads absPath, stores its content-addressed blob (deduped), and
// returns the sha. A missing file yields ("", nil) — a tombstone, not an error —
// so a checkpoint taken after the LLM deletes a file still records that state.
func (s *VersionStore) writeBlob(absPath string) (string, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", absPath, err)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	blob := s.blobPath(sha)
	if _, err := os.Stat(blob); err == nil {
		return sha, nil // dedup: identical content already stored
	}
	if err := writeFileAtomicVersions(blob, data); err != nil {
		return "", err
	}
	return sha, nil
}

// appendCheckpointLocked appends one JSON line to the timeline. Caller holds mu.
func (s *VersionStore) appendCheckpointLocked(cp Checkpoint) error {
	line, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}
	f, err := os.OpenFile(s.timelinePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open timeline: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append timeline: %w", err)
	}
	return nil
}

// Checkpoints returns the full timeline in order. Missing timeline ⇒ empty.
func (s *VersionStore) Checkpoints() ([]Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpointsLocked()
}

// checkpointsLocked reads and parses timeline.jsonl. Caller holds mu.
func (s *VersionStore) checkpointsLocked() ([]Checkpoint, error) {
	f, err := os.Open(s.timelinePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open timeline: %w", err)
	}
	defer f.Close()

	var cps []Checkpoint
	sc := bufio.NewScanner(f)
	// Checkpoints can hold many files; allow generous line lengths.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var cp Checkpoint
		if err := json.Unmarshal(line, &cp); err != nil {
			return nil, fmt.Errorf("parse timeline entry: %w", err)
		}
		cps = append(cps, cp)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read timeline: %w", err)
	}
	return cps, nil
}

// Blob returns the raw content stored under sha.
func (s *VersionStore) Blob(sha string) ([]byte, error) {
	if sha == "" {
		return nil, ErrVersionTombstone
	}
	return os.ReadFile(s.blobPath(sha))
}

// FileHistory returns path's timeline: one entry per checkpoint where the file's
// content changed (or first appeared), oldest first. A tombstone (deletion) is
// included as an entry with SHA "".
func (s *VersionStore) FileHistory(path string) ([]FileHistoryEntry, error) {
	cps, err := s.Checkpoints()
	if err != nil {
		return nil, err
	}
	var out []FileHistoryEntry
	last := ""
	for _, cp := range cps {
		for _, fv := range cp.Files {
			if fv.Path != path {
				continue
			}
			// First appearance (out empty) or a content change since the last
			// recorded version. A tombstone (SHA "") is itself a change worth
			// recording (the file was deleted).
			if len(out) == 0 || fv.SHA != last {
				out = append(out, FileHistoryEntry{Seq: cp.Seq, TS: cp.TS, Trigger: cp.Trigger, SHA: fv.SHA})
				last = fv.SHA
			}
		}
	}
	return out, nil
}

// Restore returns path's content as recorded at checkpoint seq. It returns
// ErrVersionTombstone when the file was absent at that checkpoint (the caller
// restores by deleting the working file), and an error when seq is out of range
// or the path is not in that checkpoint.
func (s *VersionStore) Restore(path string, seq int) ([]byte, error) {
	cps, err := s.Checkpoints()
	if err != nil {
		return nil, err
	}
	if seq < 0 || seq >= len(cps) {
		return nil, fmt.Errorf("checkpoint seq %d out of range (have %d)", seq, len(cps))
	}
	for _, fv := range cps[seq].Files {
		if fv.Path == path {
			return s.Blob(fv.SHA)
		}
	}
	return nil, fmt.Errorf("path %q not in checkpoint %d", path, seq)
}

// writeFileAtomicVersions writes data to path via a temp file + rename so a blob
// is never observed half-written. Mirrors the temp+rename idiom used by
// decision.go / uiprefs.go / the CSV writer.
func writeFileAtomicVersions(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".prereview-blob-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp blob: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp blob: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename blob into place: %w", err)
	}
	return nil
}
