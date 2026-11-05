package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AgentCursorFileName is the sidecar the event-stream READER writes under the
// store (#194): `prereview watch` while it blocks and when it hands a batch
// over, and `prereview comments --json` when a one-shot client polls. It is
// INBOUND like llm-status.json — the server only reads it — and it is the one
// signal the agent does not have to volunteer: reading the queue writes it.
// Reset on each launch by openStore.
const AgentCursorFileName = "agent-cursor.json"

// Agent cursor modes and states.
const (
	// AgentCursorWatch: a blocking `watch` wrote it, so a fresh mtime is a
	// heartbeat — something is attached to the stream right now.
	AgentCursorWatch = "watch"
	// AgentCursorPoll: `comments --json` wrote it. The mtime is when the queue
	// was read, never a claim that anything is still attached.
	AgentCursorPoll = "poll"

	AgentCursorWaiting   = "waiting"   // blocked in watch, alive
	AgentCursorDelivered = "delivered" // handed a batch over and exited
)

// The timing knobs. Vars, not consts (like emitDebounce), so tests can shrink
// them; ApplyAgentTimingEnv lets the browser e2e — which runs the binary as a
// separate process — do the same.
var (
	// AgentCursorHeartbeat is how often a blocked watch re-stamps the file.
	AgentCursorHeartbeat = 3 * time.Second
	// AgentCursorStale is how old a watch heartbeat may be and still count as
	// attached. ≥3× the beat: a tighter window flaps on a loaded box, and every
	// flip is a fan-out re-render.
	AgentCursorStale = 10 * time.Second
	// AgentStallAfter is how long the agent may stay silent, with work
	// outstanding and nothing attached, before the UI says so. Generous: a
	// reasoning turn writes nothing at all.
	AgentStallAfter = 3 * time.Minute
	// EditSettle is how long the reviewed files must stay quiet, with the agent
	// not "working", before a run of edits counts as one finished change and the
	// reviewer is offered a refresh (#198). Long, because the measured gaps inside
	// one agent burst reached 34s; a "done" write closes the window at once.
	EditSettle = 45 * time.Second
)

// ApplyAgentTimingEnv overrides the timing knobs from PREREVIEW_AGENT_HEARTBEAT,
// PREREVIEW_AGENT_STALE, PREREVIEW_AGENT_STALL_AFTER and PREREVIEW_EDIT_SETTLE
// (Go durations). A test hook, like PREREVIEW_PROMPTS_DIR: the browser e2e cannot
// otherwise reach the stale or stalled phases, or settle edits in seconds.
// Malformed values are ignored.
func ApplyAgentTimingEnv() {
	for env, knob := range map[string]*time.Duration{
		"PREREVIEW_AGENT_HEARTBEAT":   &AgentCursorHeartbeat,
		"PREREVIEW_AGENT_STALE":       &AgentCursorStale,
		"PREREVIEW_AGENT_STALL_AFTER": &AgentStallAfter,
		"PREREVIEW_EDIT_SETTLE":       &EditSettle,
	} {
		if d, err := time.ParseDuration(os.Getenv(env)); err == nil && d > 0 {
			*knob = d
		}
	}
}

// AgentCursor is the JSON shape of agent-cursor.json.
type AgentCursor struct {
	Mode  string `json:"mode"`
	State string `json:"state"`
	// DeliveredSeq is the highest event seq the reader has seen: the batch it
	// just printed, or the --since cursor it is waiting past. -1 = none.
	DeliveredSeq int `json:"delivered_seq"`
	// PID and UpdatedAt are debug fields; the server derives nothing from them
	// (liveness is the file's mtime — processAlive cannot work on Windows).
	PID       int    `json:"pid,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// AgentCursorPath returns the sidecar path for the store whose CSV is csvPath.
func AgentCursorPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), AgentCursorFileName)
}

// WriteAgentCursor atomically (temp + rename, same dir) writes c to dir's
// sidecar, so the server's poll never reads a torn file.
func WriteAgentCursor(dir string, c AgentCursor) error {
	c.PID = os.Getpid()
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeFileAtomicVersions(filepath.Join(dir, AgentCursorFileName), append(b, '\n'))
}

// readAgentCursor returns the parsed sidecar and its mtime, with
// readLLMStatus's three-way error contract: nil, os.IsNotExist (no reader yet),
// or a malformed/torn read the caller should ignore, keeping its last value.
func readAgentCursor(path string) (AgentCursor, time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return AgentCursor{}, time.Time{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return AgentCursor{}, time.Time{}, err
	}
	var c AgentCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return AgentCursor{}, time.Time{}, fmt.Errorf("parse %s: %w", AgentCursorFileName, err)
	}
	return c, fi.ModTime(), nil
}

// Attached reports whether a blocking watch is on the stream right now: it
// says it is waiting and its heartbeat is fresh.
func (c AgentCursor) Attached(mtime, now time.Time) bool {
	return c.Mode == AgentCursorWatch && c.State == AgentCursorWaiting && now.Sub(mtime) < AgentCursorStale
}

// PickedUp reports whether the queue has ever been read: a batch delivered, a
// watch already past some seq, or a one-shot poll.
func (c AgentCursor) PickedUp() bool {
	return c.DeliveredSeq >= 0 || c.Mode == AgentCursorPoll
}

// RecordPoll stamps a one-shot `comments --json` read (#194) — pickup, never
// attachment. It leaves a live watch's stamp alone: overwriting an attached
// cursor with a poll would flip it to "not attached" until the next heartbeat,
// two needless fan-outs for one read.
func RecordPoll(dir string) error {
	cur, mtime, err := readAgentCursor(filepath.Join(dir, AgentCursorFileName))
	if err == nil && cur.Attached(mtime, time.Now()) {
		return nil
	}
	return WriteAgentCursor(dir, AgentCursor{Mode: AgentCursorPoll, State: AgentCursorDelivered, DeliveredSeq: -1})
}
