package review

// state_queue.go derives the work-queue view (#119) — "what's queued, what was
// picked up, what's done" — from the comment set + the agent's processed markers
// + the llm-status echo. Nothing new is persisted: the queue is a pure
// projection, so it can never drift from the source-of-truth files.

// Queue states, in lifecycle order. A comment is in exactly one.
const (
	queueDraft  = "draft"  // held back — not yet enqueued for the agent
	queueQueued = "queued" // enqueued, waiting/remaining (the agent hasn't marked it)
	queueDone   = "done"   // the agent marked it worked-on (processed.jsonl)
)

// QueueState classifies a comment for the queue view. Resolved and outdated
// comments have left the queue (the human closed them / the anchor vanished) and
// return "" — the panel skips them.
func (c Comment) QueueState() string {
	switch {
	case c.Resolved || c.AnchorOutdated():
		return ""
	case c.Draft:
		return queueDraft
	case c.Processed:
		return queueDone
	default:
		return queueQueued
	}
}

// QueueItem is one row of the queue panel — a presentation view over a comment.
type QueueItem struct {
	ID    string
	File  string
	Line  int    // new-side line (0 for file/region/area comments)
	Body  string // the comment text, shown truncated by CSS
	State string // queueDraft | queueQueued | queueDone
}

func (s PrereviewState) countQueue(state string) int {
	n := 0
	for _, c := range s.Comments {
		if c.QueueState() == state {
			n++
		}
	}
	return n
}

// QueuedCount is the number of enqueued-but-not-yet-done comments — the
// "remaining" work the agent still has to pick up.
func (s PrereviewState) QueuedCount() int { return s.countQueue(queueQueued) }

// DoneCount is the number of comments the agent has marked worked-on.
func (s PrereviewState) DoneCount() int { return s.countQueue(queueDone) }

// DraftCount is the number of held (not-yet-enqueued) comments.
func (s PrereviewState) DraftCount() int { return s.countQueue(queueDraft) }

// HasQueue reports whether there's anything to show in the queue panel (any
// draft/queued/done comment). Gates the toolbar indicator so it stays hidden on
// an empty review.
func (s PrereviewState) HasQueue() bool {
	return s.QueuedCount()+s.DoneCount()+s.DraftCount() > 0
}

// AgentWorking mirrors the llm-status echo: true while the agent is applying a
// batch. Drives the "in progress" state of the queue (the queued set is being
// worked) and a live pill.
func (s PrereviewState) AgentWorking() bool { return s.LLMState == LLMStateWorking }

// QueueItems returns the queue rows ordered by lifecycle — queued (remaining)
// first, then done, then drafts — so the panel leads with what still needs the
// agent's attention. Resolved/outdated comments are excluded (QueueState "").
func (s PrereviewState) QueueItems() []QueueItem {
	var queued, done, drafts []QueueItem
	for _, c := range s.Comments {
		item := QueueItem{ID: c.ID, File: c.File, Line: c.ToLine, Body: c.Body, State: c.QueueState()}
		switch item.State {
		case queueQueued:
			queued = append(queued, item)
		case queueDone:
			done = append(done, item)
		case queueDraft:
			drafts = append(drafts, item)
		}
	}
	out := make([]QueueItem, 0, len(queued)+len(done)+len(drafts))
	out = append(out, queued...)
	out = append(out, done...)
	out = append(out, drafts...)
	return out
}
