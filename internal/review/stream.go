package review

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// EventStream emits the --stream mode JSON event log: one JSON object per
// line, written to both a live channel (stdout, polled by the consuming LLM)
// and an append-only durable mirror (.prereview/events.jsonl, for replay after
// a context reset). It is the single writer of these events — controller
// actions call it under its mutex, so emitted lines are naturally ordered and
// carry a monotonic seq. A nil *EventStream is never emitted to; callers gate
// on stream mode before constructing one.
type EventStream struct {
	mu       sync.Mutex
	seq      int
	out      io.Writer // live channel (os.Stdout in production)
	filePath string    // append-only durable mirror
}

// NewEventStream returns an emitter targeting out (live) and filePath (durable
// mirror). filePath may be "" to disable the durable mirror (tests).
func NewEventStream(out io.Writer, filePath string) *EventStream {
	return &EventStream{out: out, filePath: filePath}
}

// StreamEvent is one line of the event log. Per-event-type fields are
// omitempty so each event carries only what it needs; the leading
// event/seq/ts are always present and, being struct fields, keep their
// declared order (a map would sort them alphabetically).
type StreamEvent struct {
	Event string `json:"event"`
	Seq   int    `json:"seq"`
	Ts    string `json:"ts"`
	Repo  string `json:"repo,omitempty"` // ready
	CSV   string `json:"csv,omitempty"`  // ready
	// Comments is a pointer so a handoff always serializes the key — `[]` when
	// empty, never absent — while ready/session_end (nil) omit it. A consumer
	// keying event["comments"] on a handoff never chokes on a missing field.
	Comments *[]StreamComment `json:"comments,omitempty"` // handoff
}

// CommentList returns the event's comment snapshot, or nil for events that
// carry none (ready / session_end).
func (e StreamEvent) CommentList() []StreamComment {
	if e.Comments == nil {
		return nil
	}
	return *e.Comments
}

// StreamComment is the consumer-facing shape of a Comment: the CSV fields
// minus the opaque `anchor` fingerprint (the consumer must not parse it) and
// minus `resolved` (a handoff snapshot is pre-filtered to actionable rows).
// Area is a nested object (or null) rather than a JSON-in-a-string blob, so
// the consumer never parses nested JSON.
type StreamComment struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	File         string `json:"file"`
	FromLine     int    `json:"from_line"`
	ToLine       int    `json:"to_line"`
	Side         string `json:"side"`
	Body         string `json:"body"`
	URL          string `json:"url"`
	Area         *Area  `json:"area"`
	CreatedAt    string `json:"created_at"`
	AnchorStatus string `json:"anchor_status"`
}

// toStreamComment maps a Comment to its consumer-facing shape. Area is set
// only when the comment actually carries a rectangle (kind=area/region); line
// and file comments serialize as "area":null.
func toStreamComment(c Comment) StreamComment {
	sc := StreamComment{
		ID:           c.ID,
		Kind:         c.Kind,
		File:         c.File,
		FromLine:     c.FromLine,
		ToLine:       c.ToLine,
		Side:         c.Side,
		Body:         c.Body,
		URL:          c.URL,
		CreatedAt:    c.Created.UTC().Format(time.RFC3339),
		AnchorStatus: c.AnchorStatus,
	}
	if !c.Area.Empty() {
		a := c.Area
		sc.Area = &a
	}
	return sc
}

// actionableComments returns the comments the skill should act on — every
// unresolved, non-outdated comment, mapped to its stream shape. This is the
// payload of a handoff event: a full snapshot, deduped by id on the consumer
// side, so the human's resolve-clicks naturally prune later rounds.
func actionableComments(comments []Comment) []StreamComment {
	out := make([]StreamComment, 0, len(comments))
	for _, c := range comments {
		if c.Resolved || c.AnchorOutdated() {
			continue
		}
		out = append(out, toStreamComment(c))
	}
	return out
}

// emit stamps ev with the next seq + the given timestamp and writes it as one
// JSON line to both sinks. ts is passed in (not read from the clock) so tests
// are deterministic. The live channel is written first — it's the channel the
// LLM is actively polling — then the durable mirror is appended.
func (e *EventStream) emit(ev StreamEvent, ts time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	ev.Seq = e.seq
	e.seq++
	ev.Ts = ts.UTC().Format(time.RFC3339)

	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", ev.Event, err)
	}
	b = append(b, '\n')

	if _, err := e.out.Write(b); err != nil {
		return fmt.Errorf("write %s event to stdout: %w", ev.Event, err)
	}
	if e.filePath != "" {
		f, err := os.OpenFile(e.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open events file: %w", err)
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			return fmt.Errorf("append %s event: %w", ev.Event, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close events file: %w", err)
		}
	}
	return nil
}

// EmitReady announces the session is live. Emitted once, after the
// READY/REPO stdout preamble, so the preamble parse is never interleaved
// with JSON.
func (e *EventStream) EmitReady(repo, csvPath string, ts time.Time) error {
	return e.emit(StreamEvent{Event: "ready", Repo: repo, CSV: csvPath}, ts)
}

// EmitHandoff emits a full actionable snapshot — one per "Hand off" click.
// The snapshot is always a non-nil slice so the comments key is always present
// (`[]` when nothing is actionable).
func (e *EventStream) EmitHandoff(comments []Comment, ts time.Time) error {
	snap := actionableComments(comments)
	return e.emit(StreamEvent{Event: "handoff", Comments: &snap}, ts)
}

// EmitSessionEnd emits the single terminator — the only event the consumer
// loop should treat as "stop".
func (e *EventStream) EmitSessionEnd(ts time.Time) error {
	return e.emit(StreamEvent{Event: "session_end"}, ts)
}
