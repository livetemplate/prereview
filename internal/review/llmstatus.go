package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LLMStatusFileName is the fixed name of the status file the agent writes under
// .prereview/ and the server watches. It is the INBOUND counterpart to
// events.jsonl: where events.jsonl carries handoffs server→agent, llm-status.json
// carries the agent's "what am I doing" echo agent→server, so the review UI can
// show live status across every open tab. Reset on each launch by openStore.
const LLMStatusFileName = "llm-status.json"

// LLM status values written by the agent into llm-status.json's "state" field.
// Anything else (including "") is treated as idle.
const (
	LLMStateWorking = "working"
	LLMStateDone    = "done"
)

// LLMStatusPollInterval is how often the server stats the status file. It trades
// UI latency against wakeups; ~0.75s feels live without busy-polling, and the
// stat is cheap. Mirrors the polling shape of the existing DONE marker rather
// than adding an fsnotify dependency.
const LLMStatusPollInterval = 750 * time.Millisecond

// LLMStatus is the inbound signal the agent writes to
// <REPO>/.prereview/llm-status.json so the running review server can show what
// the agent is doing. It is the reverse of the outbound event stream
// (stream.go): the agent is the writer, the server the reader.
type LLMStatus struct {
	// State is "working" while the agent applies a handoff batch, "done" once it
	// has finished and reported back. "" (missing/blank) means idle.
	State string `json:"state"`
	// Message is an optional human-readable detail shown in the status pill,
	// e.g. "Applying 5 comments".
	Message string `json:"message,omitempty"`
	// UpdatedAt is an RFC3339 timestamp the agent stamps on each write. The
	// watcher uses the file's mtime for change detection; this is a secondary
	// human/debug field.
	UpdatedAt string `json:"updated_at,omitempty"`
}

// LLMStatusPath returns the status-file path for a store whose CSV lives at
// csvPath — i.e. <csv dir>/llm-status.json, the same .prereview/ directory that
// holds events.jsonl. Centralised so the launch reset (store.go), the watcher,
// and Mount all agree on one location, including single-file reviews where the
// store dir is the file's parent.
func LLMStatusPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), LLMStatusFileName)
}

// readLLMStatus reads and parses the status file. It is deliberately tolerant so
// the caller can distinguish three cases via the returned error:
//   - nil error            → a well-formed status (State may be "" for idle)
//   - os.IsNotExist(err)   → no file yet (reset on launch, agent hasn't written)
//   - any other error      → unreadable/malformed (e.g. a torn mid-write read)
//
// Callers reset to idle on not-exist but KEEP their previous value on a
// malformed read, so a momentary torn read doesn't flicker the UI; the agent
// writes atomically (temp+rename) so torn reads are rare and self-correct on the
// next poll regardless.
func readLLMStatus(path string) (LLMStatus, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return LLMStatus{}, err
	}
	var s LLMStatus
	if err := json.Unmarshal(b, &s); err != nil {
		return LLMStatus{}, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return s, nil
}

// statusFingerprint returns a cheap change key (mtime+size) for the status file,
// or "" when the file is absent. The watcher fans a change out only when this
// differs from the previous tick, so an idle server does no work and an
// unchanged file isn't re-broadcast.
func statusFingerprint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.ModTime().UnixNano(), fi.Size())
}
