package review

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store-layout filenames under .prereview/ that the CLI subcommands resolve.
// Centralised so the server and the read subcommands (events, comments) agree on
// one location — mirroring ProcessedFileName / SuggestionFileName.
const (
	// CommentsFileName is the CSV the review server writes (the on-disk source of
	// truth) and the `prereview comments` reader parses.
	CommentsFileName = "comments.csv"
	// EventsFileName is the durable, append-only event log written only in
	// --agent mode and reset per launch (openStore); each line is a seq-stamped
	// StreamEvent consumed by `prereview events`.
	EventsFileName = "events.jsonl"
)

// EventsPath returns the event-log path for a store whose CSV lives at csvPath —
// i.e. <csv dir>/events.jsonl, alongside processed.jsonl and llm-status.json.
func EventsPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), EventsFileName)
}

// EventStream emits the --agent mode JSON event log: one JSON object per
// line, written to both a live channel (stdout, polled by the consuming LLM)
// and an append-only durable mirror (.prereview/events.jsonl, for replay after
// a context reset). It is the single writer of these events — controller
// actions call it under its mutex, so emitted lines are naturally ordered and
// carry a monotonic seq. A nil *EventStream is never emitted to; callers gate
// on agent mode before constructing one.
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
	// Comments is a pointer so a snapshot always serializes the key — `[]` when
	// empty, never absent — while ready/end (nil) omit it. A consumer
	// keying event["comments"] on a snapshot never chokes on a missing field.
	Comments *[]StreamComment `json:"comments,omitempty"` // snapshot
	// Suggestions carries the reviewer's decisions on the LLM's suggested edits
	// (issue #98): the decided, non-outdated suggestions the LLM should act on —
	// apply accepts, rework revises, drop rejects. Same pointer convention as
	// Comments: always present on a snapshot (`[]` when none), absent elsewhere.
	Suggestions *[]StreamDecision `json:"suggestions,omitempty"` // snapshot
	// Paused reports that the reviewer paused the queue (batching): the agent's
	// `watch` will block until resume, which then delivers one coalesced snapshot.
	Paused bool `json:"paused,omitempty"` // ready / snapshot
	// SkillUpdated is set on the `ready` event when this launch refreshed the
	// installed prereview skill to match the (possibly self-updated) binary — so
	// the agent's loaded skill is now stale. The agent should re-read the skill
	// from its install path before continuing and tell the user to reload it.
	SkillUpdated bool `json:"skill_updated,omitempty"` // ready
}

// CommentList returns the event's comment snapshot, or nil for events that
// carry none (ready / end).
func (e StreamEvent) CommentList() []StreamComment {
	if e.Comments == nil {
		return nil
	}
	return *e.Comments
}

// DecisionList returns the event's suggestion-decision snapshot, or nil for
// events that carry none.
func (e StreamEvent) DecisionList() []StreamDecision {
	if e.Suggestions == nil {
		return nil
	}
	return *e.Suggestions
}

// StreamDecision is the consumer-facing shape of a reviewer's decision on one LLM
// suggestion: the verdict + note joined with the suggestion's content and location,
// so the LLM has everything it needs to act without reading any file itself.
// Emitted only for decided, non-outdated suggestions (see actionableDecisions).
type StreamDecision struct {
	ID           string `json:"id"`
	File         string `json:"file"`
	FromLine     int    `json:"from_line"`
	ToLine       int    `json:"to_line"`
	Side         string `json:"side"`
	Verdict      string `json:"verdict"` // accept | reject | revise | revert (#159 M4.2, wire-only)
	Note         string `json:"note,omitempty"`
	Original     string `json:"original"`
	Proposed     string `json:"proposed"`
	AnchorStatus string `json:"anchor_status"`
	// Thread is the #149 conversation on this suggestion (see StreamComment.Thread).
	Thread []StreamReply `json:"thread,omitempty"`
}

// actionableDecisions returns the suggestions the LLM should act on: every
// fingerprint-matched decision (from state.DecisionsBySuggestion) whose suggestion is
// not outdated, PLUS any suggestion the reviewer replied on last (#149 unread), mapped
// to its stream shape with its thread. Outdated is excluded so an accepted edit, once
// the LLM applies it, drops off the next snapshot; a reworked revise drops because its
// fingerprint no longer matches. The consumer dedupes by id, exactly like comments.
func actionableDecisions(suggestions []Suggestion, decided map[string]SuggestionDecision, threadByID map[string][]ThreadEntry, applied map[string]bool) []StreamDecision {
	out := make([]StreamDecision, 0, len(decided))
	for _, sg := range suggestions {
		d, isDecided := decided[sg.ID]
		// #159 M4.2: a revert-PENDING suggestion (the reviewer asked to undo an applied
		// accept) must reach the agent EVEN THOUGH it's applied+outdated — that's the
		// whole point. decided is already effective (DecisionsBySuggestion drops
		// revert-complete), so d.Revert here ⟹ still applied ⟹ genuinely pending.
		revertPending := isDecided && d.Revert
		thread := threadByID[sg.ID]
		// #164: a fresh reviewer reply (thread ends with the reviewer) re-surfaces a
		// suggestion the agent already handled (outdated/applied) — the same override the
		// comment path applies — so the follow-up reaches the agent instead of being
		// stranded on disk. revertPending already bypasses the suppression for its own reason.
		unread := hasUnreadReviewerReply(thread)
		if !revertPending && !unread && (sg.AnchorOutdated() || applied[sg.ID]) {
			continue // outdated, or already applied by the agent (#159) → nothing to do
		}
		if !isDecided && !unread {
			continue // undecided and no reviewer reply → nothing for the agent
		}
		verdict := d.Verdict
		if revertPending {
			verdict = verdictRevert // restore Original over the applied Proposed, then `prereview reverted`
		}
		out = append(out, StreamDecision{
			ID:           sg.ID,
			File:         sg.File,
			FromLine:     sg.FromLine,
			ToLine:       sg.ToLine,
			Side:         sg.Side,
			Verdict:      verdict,
			Note:         d.Note,
			Original:     sg.OriginalText,
			Proposed:     sg.ProposedText,
			AnchorStatus: sg.AnchorStatus,
			Thread:       toStreamThread(thread),
		})
	}
	return out
}

// StreamComment is the consumer-facing shape of a Comment: the CSV fields
// minus the opaque `anchor` fingerprint (the consumer must not parse it) and
// minus `resolved` (a snapshot is pre-filtered to actionable rows).
// Area is a nested object (or null) rather than a JSON-in-a-string blob, so
// the consumer never parses nested JSON.
type StreamComment struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	File         string `json:"file"`
	FromLine     int    `json:"from_line"`
	ToLine       int    `json:"to_line"`
	FromCol      int    `json:"from_col"`
	ToCol        int    `json:"to_col"`
	Side         string `json:"side"`
	Body         string `json:"body"`
	URL          string `json:"url"`
	Area         *Area  `json:"area"`
	CreatedAt    string `json:"created_at"`
	AnchorStatus string `json:"anchor_status"`
	// Text is the exact selected substring for kind=text comments (the phrase
	// the reviewer highlighted). Lets the consuming LLM see WHAT was commented
	// on — essential for rendered-view (Preview) comments, which anchor at
	// line level (no columns) so the phrase is the only sub-line signal.
	Text string `json:"text,omitempty"`
	// Thread is the #149 conversation on this comment: the agent's prior replies
	// and the reviewer's follow-ups, oldest first. Present when non-empty so the
	// agent has the full exchange — and can tell a fresh comment (no thread) from a
	// reviewer reply it must respond to (the last entry's author is "reviewer").
	Thread []StreamReply `json:"thread,omitempty"`
}

// StreamReply is one consumer-facing thread entry (#149): who said it, what, when.
// At is RFC3339 (not the internal nanoseconds) to match CreatedAt's convention.
type StreamReply struct {
	Author string `json:"author"`
	Body   string `json:"body"`
	At     string `json:"at"`
}

// toStreamThread maps a target's thread entries to the consumer shape; nil when empty.
func toStreamThread(thread []ThreadEntry) []StreamReply {
	if len(thread) == 0 {
		return nil
	}
	out := make([]StreamReply, 0, len(thread))
	for _, e := range thread {
		out = append(out, StreamReply{
			Author: e.Author,
			Body:   e.Body,
			At:     time.Unix(0, e.At).UTC().Format(time.RFC3339),
		})
	}
	return out
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
		FromCol:      c.FromCol,
		ToCol:        c.ToCol,
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
	if c.IsTextLevel() {
		sc.Text = c.Anchor.Snippet
	}
	return sc
}

// actionableComments returns the comments the skill should act on — every
// unresolved, non-outdated comment, mapped to its stream shape. This is the
// payload of a snapshot event: a full snapshot, deduped by id on the consumer
// side, so the human's resolve-clicks naturally prune later rounds.
func actionableComments(comments []Comment, threadByID map[string][]ThreadEntry) []StreamComment {
	out := make([]StreamComment, 0, len(comments))
	for _, c := range comments {
		// Drafts (#119) are the reviewer's not-yet-enqueued notes — kept out of the
		// actionable snapshot until enqueued. A draft was never handed to the agent, so
		// it can't carry a thread; the reply override below never re-admits one.
		if c.Draft {
			continue
		}
		thread := threadByID[c.ID]
		// #149/#164 unread model, via threadActionable: a comment is actionable when it is
		// fresh-and-not-settled, or its thread ends with the reviewer. Passing "settled" as
		// resolved OR outdated means a reviewer reply overrides BOTH — so a follow-up on a
		// comment the agent edited (→ outdated) reaches the agent instead of being stranded
		// on disk, while an agent-last (or untouched-outdated) comment still drops.
		if !threadActionable(c.Resolved || c.AnchorOutdated(), thread) {
			continue
		}
		sc := toStreamComment(c)
		sc.Thread = toStreamThread(thread)
		out = append(out, sc)
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
func (e *EventStream) EmitReady(repo, csvPath string, paused, skillUpdated bool, ts time.Time) error {
	return e.emit(StreamEvent{Event: "ready", Repo: repo, CSV: csvPath, Paused: paused, SkillUpdated: skillUpdated}, ts)
}

// EmitSnapshot emits a full actionable snapshot — one per queue mutation (or the
// final EndSession flush): the open comments AND the reviewer's decisions on the
// LLM's suggestions.
// Both snapshots are always non-nil slices so their keys are always present
// (`[]` when nothing is actionable). decided is the fingerprint-matched decision
// map (state.DecisionsBySuggestion) — only decided, non-outdated suggestions ship.
func (e *EventStream) EmitSnapshot(comments []Comment, suggestions []Suggestion, decided map[string]SuggestionDecision, threadByID map[string][]ThreadEntry, applied map[string]bool, paused bool, ts time.Time) error {
	csnap := actionableComments(comments, threadByID)
	dsnap := actionableDecisions(suggestions, decided, threadByID, applied)
	return e.emit(StreamEvent{Event: "snapshot", Comments: &csnap, Suggestions: &dsnap, Paused: paused}, ts)
}

// EmitEnd emits the single terminator — the only event the consumer
// loop should treat as "stop".
func (e *EventStream) EmitEnd(ts time.Time) error {
	return e.emit(StreamEvent{Event: "end"}, ts)
}
