package review

// state_queue.go derives the work-queue view (#119) — "what's queued, what was
// picked up, what's done" — from the comment set + the agent's processed markers
// + accepted suggestions (#159) + the llm-status echo. Nothing new is persisted:
// the queue is a pure projection, so it can never drift from the source-of-truth
// files.
//
// Both a comment and a suggestion ride the SAME three-state lifecycle: a comment
// is queued→done via processed.jsonl; an accepted-but-not-yet-applied suggestion
// is "queued" (the agent still has to write the edit) and an applied one is
// "done" (via applied.jsonl). So the toolbar button, counts, and enqueue pulse
// light up for a suggestion accept with no separate machinery.

// Queue states, in lifecycle order. A comment/suggestion is in exactly one.
const (
	queueDraft  = "draft"  // held back — not yet enqueued for the agent
	queueQueued = "queued" // enqueued, waiting/remaining (the agent hasn't marked it)
	queueDone   = "done"   // the agent marked it worked-on (processed.jsonl / applied.jsonl)
)

// Queue item kinds — a queue row is either a comment or an accepted suggestion.
const (
	queueKindComment    = "comment"
	queueKindSuggestion = "suggestion"
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

// suggestionQueueState classifies a suggestion for the queue view (#159). An
// applied suggestion is "done" (the edit is on disk — checked FIRST, since an
// applied suggestion is also anchor-outdated); an accepted-but-not-applied one is
// "queued" (the agent still has to write it). Anything else — rejected, revised,
// or undecided — has not entered the queue and returns "". A suggestion is never
// a draft (there's no held-back state for an accept).
func (s PrereviewState) suggestionQueueState(id string) string {
	if s.Applied[id] {
		return queueDone
	}
	for _, d := range s.Decisions {
		if d.SuggestionID == id {
			if d.Verdict == verdictAccept {
				return queueQueued
			}
			return "" // reject / revise: not queued
		}
	}
	return "" // undecided
}

func (s PrereviewState) countQueue(state string) int {
	n := 0
	for _, c := range s.scopedComments() {
		if c.QueueState() == state {
			n++
		}
	}
	for _, sg := range s.scopedSuggestions() {
		if s.suggestionQueueState(sg.ID) == state {
			n++
		}
	}
	return n
}

// QueuedCount is the number of enqueued-but-not-yet-done comments + accepted-but-
// not-yet-applied suggestions — the "remaining" work the agent still has to pick up.
func (s PrereviewState) QueuedCount() int { return s.countQueue(queueQueued) }

// DoneCount is the number of comments the agent marked worked-on + suggestions it
// applied.
func (s PrereviewState) DoneCount() int { return s.countQueue(queueDone) }

// DraftCount is the number of held (not-yet-enqueued) comments.
func (s PrereviewState) DraftCount() int { return s.countQueue(queueDraft) }

// AwaitingApplyCount is how many suggestions the reviewer has ACCEPTED but the agent has
// not yet written to the file (#171).
//
// Accepting only records a verdict — prereview never writes user files (#103); the agent
// applies the edit and acks with `prereview applied <id>`. That works while an agent is
// looping on `prereview watch`, but accept an edit after its turn has ended and nothing
// ever applies it: the verdict sits in suggestion-decisions.jsonl, the file is untouched,
// and the card has already collapsed to an amber badge (#165) that is trivially missed.
// The document silently never becomes clean.
//
// This is the count that makes that state impossible to ignore — surfaced in the queue
// and warned about on End session. Revert-aware, because s.Applied nets reverted.jsonl:
// undoing an applied edit correctly puts it back in the awaiting-apply pile.
func (s PrereviewState) AwaitingApplyCount() int {
	n := 0
	for _, sg := range s.scopedSuggestions() {
		if s.suggestionAcceptedPending(sg.ID) {
			n++
		}
	}
	return n
}

// HasQueue reports whether there's anything to show in the queue panel (any
// draft/queued/done comment or accepted/applied suggestion). Gates the toolbar
// indicator so it stays hidden on an empty review.
func (s PrereviewState) HasQueue() bool {
	return s.QueuedCount()+s.DoneCount()+s.DraftCount() > 0
}

// AgentWorking mirrors the llm-status echo: true while the agent is applying a
// batch. Drives the "in progress" state of the queue (the queued set is being
// worked) and a live pill.
func (s PrereviewState) AgentWorking() bool { return s.LLMState == LLMStateWorking }

// QueueItem is one row of the queue panel — a presentation view over a comment or
// an accepted suggestion (Kind distinguishes them so the row can render the ✦
// marker and route its jump to the right target).
type QueueItem struct {
	ID    string
	Kind  string // queueKindComment | queueKindSuggestion
	File  string
	Line  int    // new-side line (0 for file/region/area comments)
	Body  string // the comment text / suggestion note, shown truncated by CSS
	State string // queueDraft | queueQueued | queueDone
}

// QueueItems returns the queue rows ordered by lifecycle — queued (remaining)
// first, then done, then drafts — so the panel leads with what still needs the
// agent's attention. Resolved/outdated comments and un-queued suggestions are
// excluded (QueueState "").
func (s PrereviewState) QueueItems() []QueueItem {
	var queued, done, drafts []QueueItem
	add := func(item QueueItem) {
		switch item.State {
		case queueQueued:
			queued = append(queued, item)
		case queueDone:
			done = append(done, item)
		case queueDraft:
			drafts = append(drafts, item)
		}
	}
	for _, c := range s.scopedComments() {
		add(QueueItem{ID: c.ID, Kind: queueKindComment, File: c.File, Line: c.ToLine, Body: c.Body, State: c.QueueState()})
	}
	for _, sg := range s.scopedSuggestions() {
		st := s.suggestionQueueState(sg.ID)
		if st == "" {
			continue
		}
		add(QueueItem{ID: sg.ID, Kind: queueKindSuggestion, File: sg.File, Line: sg.ToLine, Body: suggestionQueueBody(sg), State: st})
	}
	out := make([]QueueItem, 0, len(queued)+len(done)+len(drafts))
	out = append(out, queued...)
	out = append(out, done...)
	out = append(out, drafts...)
	return out
}

// suggestionQueueBody is the queue row's label for a suggestion: the LLM's note
// if it gave one, else a plain fallback (the diff itself is one tap away).
func suggestionQueueBody(sg Suggestion) string {
	if sg.Note != "" {
		return sg.Note
	}
	return "Suggested edit"
}
