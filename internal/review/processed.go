package review

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// processed.go wires the agent's per-comment "I worked on this" signal into the
// review UI (issue #88 item 3). It is the per-comment sibling of llm-status.json:
// where llm-status.json is a single GLOBAL "what am I doing" echo, processed.jsonl
// is an append-only log of the specific comment IDs the agent has addressed.
//
// The agent appends one line per comment via `prereview processed <id>` (see the
// main package's processed subcommand + the skill helper). The server reads the
// ID set on every Mount and whenever the file changes (the llm-status watcher
// covers it too), and marks Comment.Processed so the card shows a "worked on"
// badge. Nothing reads processed-state from comments.csv — the sidecar is already
// durable (append-only, and unlike llm-status.json it is NOT reset on launch, so
// "worked on" is history that survives a relaunch), so there is no CSV column and
// the server stays the sole writer of comments.csv.

// ProcessedFileName is the fixed name of the agent-written, append-only markers
// file under .prereview/. Durable across launches (openStore does NOT reset it).
const ProcessedFileName = "processed.jsonl"

// ReenqueuedFileName holds the server-owned "un-processed" tombstones (#119): a
// reviewer who re-enqueues a done comment appends its id here. A comment counts
// as done only while its processed-marks OUTNUMBER its re-enqueue marks, so a
// process → re-enqueue → re-process cycle resolves correctly (each re-enqueue
// cancels one process mark). Append-only, same one-id-per-line shape as
// processed.jsonl.
const ReenqueuedFileName = "reenqueued.jsonl"

// ProcessedMark is one line of processed.jsonl: the comment ID the agent
// addressed plus an informational timestamp (the server only uses ID).
type ProcessedMark struct {
	ID string `json:"id"`
	At string `json:"at,omitempty"`
}

// ProcessedPath returns the markers-file path for a store whose CSV lives at
// csvPath — i.e. <csv dir>/processed.jsonl, alongside llm-status.json. Centralised
// so the subcommand (main package), the watcher, and Mount all agree on one
// location, including single-file reviews where the store dir is the file's parent.
func ProcessedPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), ProcessedFileName)
}

// ReenqueuePath is the re-enqueue tombstone file alongside processed.jsonl.
func ReenqueuePath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), ReenqueuedFileName)
}

// loadMarkCounts reads an append-only {"id":…} marks file into per-id counts.
// Shared by processed.jsonl and reenqueued.jsonl; tolerant of a missing file
// (nil) and torn lines (skipped), so a review never breaks on it.
func loadMarkCounts(path string) map[string]int {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	counts := make(map[string]int)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m ProcessedMark
		if err := json.Unmarshal(line, &m); err != nil || m.ID == "" {
			continue
		}
		counts[m.ID]++
	}
	return counts
}

// applyProcessed flips Comment.Processed on the comments already in state to
// match the markers file — a cheap by-ID overlay (no reload, no re-anchor), so it
// is safe to call from both Mount (freshly-loaded comments) and the watcher's
// LLMStatusChanged fan-out (existing comments). Markers are append-only, so this
// only ever turns the badge ON.
func (c *PrereviewController) applyProcessed(state *PrereviewState) {
	pc := loadMarkCounts(c.processedPath())
	if len(pc) == 0 {
		return
	}
	rc := loadMarkCounts(c.reenqueuedPath()) // re-enqueue tombstones (#119)
	for i := range state.Comments {
		id := state.Comments[i].ID
		// Done only while processed marks outnumber re-enqueue marks.
		state.Comments[i].Processed = pc[id] > rc[id]
	}
}

// reenqueueComment records a re-enqueue tombstone (#119) so a done comment moves
// back to "queued", then refreshes the derived Processed flags and re-arms the
// snapshot emit so the agent sees the fresh set.
func (c *PrereviewController) reenqueueComment(state *PrereviewState, id string) error {
	if id == "" {
		return fmt.Errorf("reenqueue: missing id")
	}
	if err := appendMark(c.reenqueuedPath(), id); err != nil {
		return err
	}
	c.applyProcessed(state)
	c.scheduleEmit()
	return nil
}

// appendMark appends one {"id":…} line to an append-only marks file.
func appendMark(path, id string) error {
	line, err := json.Marshal(ProcessedMark{ID: id})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append %s: %w", filepath.Base(path), err)
	}
	return nil
}

// processedPath is the .prereview/processed.jsonl path for this session's store.
func (c *PrereviewController) processedPath() string {
	return ProcessedPath(c.CSVPath)
}

// reenqueuedPath is the .prereview/reenqueued.jsonl tombstone path.
func (c *PrereviewController) reenqueuedPath() string {
	return ReenqueuePath(c.CSVPath)
}
