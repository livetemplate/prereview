package review

import (
	"bufio"
	"encoding/json"
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

// loadProcessedIDs reads the append-only markers file into a set of comment IDs.
// Tolerant by design: a missing file (no agent has marked anything) yields an
// empty set, and any unparseable/torn line is skipped rather than failing the
// whole load — the file is agent-appended and a review must never break on it.
func loadProcessedIDs(path string) map[string]bool {
	f, err := os.Open(path)
	if err != nil {
		return nil // missing (common) or unreadable → nothing marked
	}
	defer f.Close()
	ids := make(map[string]bool)
	sc := bufio.NewScanner(f)
	// Comment bodies aren't in this file (only IDs), but be generous so a long
	// line never silently truncates a valid mark.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m ProcessedMark
		if err := json.Unmarshal(line, &m); err != nil || m.ID == "" {
			continue // torn/partial/blank line — skip, next line may be fine
		}
		ids[m.ID] = true
	}
	return ids
}

// applyProcessed flips Comment.Processed on the comments already in state to
// match the markers file — a cheap by-ID overlay (no reload, no re-anchor), so it
// is safe to call from both Mount (freshly-loaded comments) and the watcher's
// LLMStatusChanged fan-out (existing comments). Markers are append-only, so this
// only ever turns the badge ON.
func (c *PrereviewController) applyProcessed(state *PrereviewState) {
	ids := loadProcessedIDs(c.processedPath())
	if len(ids) == 0 {
		return
	}
	for i := range state.Comments {
		if ids[state.Comments[i].ID] {
			state.Comments[i].Processed = true
		}
	}
}

// processedPath is the .prereview/processed.jsonl path for this session's store.
func (c *PrereviewController) processedPath() string {
	return ProcessedPath(c.CSVPath)
}
