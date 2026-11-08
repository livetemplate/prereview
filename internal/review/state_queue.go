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
	queueDraft    = "draft"    // held back — not yet enqueued for the agent
	queueQueued   = "queued"   // enqueued, waiting/remaining (the agent hasn't marked it)
	queueBlocked  = "blocked"  // enqueued, but the anchor is gone — needs re-anchoring
	queueAnswered = "answered" // the agent replied with no edit (#205): the reviewer's move
	queueDone     = "done"     // the agent marked it worked-on (processed.jsonl / applied.jsonl)
)

// Queue item kinds — a queue row is either a comment or an accepted suggestion.
const (
	queueKindComment    = "comment"
	queueKindSuggestion = "suggestion"
)

// QueueState classifies a comment for the queue view. A comment leaves the queue ("")
// only when the HUMAN closes it — a resolve. Nothing else removes it.
//
// Case order is load-bearing (#203). Done is checked BEFORE drift, because the agent's
// own edit is usually what invalidates the anchor of the comment it just addressed:
// keying the lifecycle off drift made Done fall back down every time the agent
// succeeded, so it never reached the total. suggestionQueueState below has always
// ordered it this way ("checked FIRST, since an applied suggestion is also
// anchor-outdated"); the comment path had it backwards.
//
// An enqueued, un-done comment whose anchor is gone is "blocked", not "": the agent is
// never handed outdated work, so it will not progress on its own, but it must stay
// visible so the reviewer can re-anchor or resolve it. Returning "" here silently
// deleted the row from every count with no badge and no total to notice it against.
//
// The #164 unread-reply reopen is layered ON TOP of this by reopenIfReplied at the
// state level (this method has no thread access), so a replied-on resolved / blocked /
// done comment counts as "queued" again.
func (c Comment) QueueState() string {
	switch {
	case c.Resolved:
		return ""
	case c.Draft:
		return queueDraft
	case c.Processed:
		return queueDone
	// After done (an edit that was made is the stronger fact), before blocked (the
	// agent has responded, so the next move is the reviewer's either way) (#205).
	case c.Answered:
		return queueAnswered
	case c.AnchorOutdated():
		return queueBlocked
	default:
		return queueQueued
	}
}

// reopenIfReplied overlays the #164 unread-reply signal on a queue lifecycle state: a
// comment or suggestion whose thread ends with the reviewer is pending agent work again,
// so it counts as "queued" whatever its base state (resolved / outdated / done / rejected
// → queued). This keeps the toolbar count and panel in step with the agent's actionable
// snapshot, which re-surfaces the same replied-on items (see actionableComments /
// actionableDecisions). awaiting is PrereviewState.AwaitingAgent(); a draft never has a
// thread, so it is never reopened here.
func reopenIfReplied(base, id string, awaiting map[string]bool) string {
	if awaiting[id] {
		return queueQueued
	}
	return base
}

// CommentQueueStates maps each comment id to the state its queue row shows — QueueState
// under the reply-reopen overlay — so a card badge says what the queue panel says (#205:
// a pushed-back answered comment reads queued, not answered). Zero-arg for the framework.
func (s PrereviewState) CommentQueueStates() map[string]string {
	awaiting := s.AwaitingAgent()
	out := make(map[string]string, len(s.Comments))
	for _, c := range s.Comments {
		out[c.ID] = reopenIfReplied(c.QueueState(), c.ID, awaiting)
	}
	return out
}

// suggestionQueueState classifies a suggestion for the queue view (#159). An
// applied suggestion is "done" (the edit is on disk — checked FIRST, since an
// applied suggestion is also anchor-outdated); an accepted-but-not-applied one is
// "queued" (the agent still has to write it). Anything else — rejected, revised,
// or undecided — has not entered the queue and returns "". A suggestion is never
// a draft (there's no held-back state for an accept).
func (s PrereviewState) suggestionQueueState(sg Suggestion) string {
	if s.Applied[sg.ID] {
		return queueDone
	}
	for _, d := range s.Decisions {
		if d.SuggestionID == sg.ID {
			if d.Verdict == verdictAccept {
				// Accepted, but the text it was written against is gone (#203). AnchorStatus
				// is re-derived from OriginalText on every load, so this happens whenever the
				// file moves on under an accept the agent has not applied yet — and
				// actionableDecisions drops an outdated suggestion, so calling it "queued"
				// would promise a pickup that never comes.
				if sg.AnchorOutdated() {
					return queueBlocked
				}
				return queueQueued
			}
			return "" // reject / revise: not queued
		}
	}
	return "" // undecided
}

// queueComments / queueSuggestions are what the QUEUE PANEL shows: by default the work
// that will be — or has been — applied to the file you are looking at (#171). The queue
// answers "what is happening to the document in front of me", which is what it's for in a
// single-file or doc review. QueueGlobal widens it to the whole review, for a many-file
// repo review where the point is draining a backlog across files.
//
// This is a VIEW filter ONLY. The agent's snapshot always carries every actionable item in
// the review (EmitSnapshot feeds off scopedComments / scopedSuggestions), so work queued on
// one file is never stranded because the reviewer was looking at another — or had the
// filter set to This file — when the agent read the queue.
//
// Both go through scoped* first, so an out-of-review file can't leak in through the Global
// door either: "global" means the whole REVIEW, never the whole shared store.
func (s PrereviewState) queueComments() []Comment {
	scoped := s.scopedComments()
	if s.QueueGlobal {
		return scoped
	}
	out := make([]Comment, 0, len(scoped))
	for _, c := range scoped {
		if c.File == s.SelectedFile {
			out = append(out, c)
		}
	}
	return out
}

func (s PrereviewState) queueSuggestions() []Suggestion {
	scoped := s.scopedSuggestions()
	if s.QueueGlobal {
		return scoped
	}
	out := make([]Suggestion, 0, len(scoped))
	for _, sg := range scoped {
		if sg.File == s.SelectedFile {
			out = append(out, sg)
		}
	}
	return out
}

// QueueScopeLabel names what the queue is currently showing — the switch's label, and the
// honest answer to "why is this count different from the one I saw a second ago".
func (s PrereviewState) QueueScopeLabel() string {
	if s.QueueGlobal {
		return "All files"
	}
	return "This file"
}

// QueueHiddenCount is how many queue rows the CURRENT filter is hiding — the work that
// exists elsewhere in the review. Zero when Global (nothing is hidden) and zero in a
// single-file review (there is nowhere else). It is what stops the per-file default from
// concealing a backlog: the switch advertises what is behind it.
func (s PrereviewState) QueueHiddenCount() int {
	if s.QueueGlobal {
		return 0
	}
	awaiting := s.AwaitingAgent()
	n := 0
	for _, c := range s.scopedComments() {
		if c.File != s.SelectedFile && reopenIfReplied(c.QueueState(), c.ID, awaiting) != "" {
			n++
		}
	}
	for _, sg := range s.scopedSuggestions() {
		if sg.File != s.SelectedFile && reopenIfReplied(s.suggestionQueueState(sg), sg.ID, awaiting) != "" {
			n++
		}
	}
	return n
}

func (s PrereviewState) countQueue(state string) int {
	awaiting := s.AwaitingAgent()
	n := 0
	for _, c := range s.queueComments() {
		if reopenIfReplied(c.QueueState(), c.ID, awaiting) == state {
			n++
		}
	}
	for _, sg := range s.queueSuggestions() {
		if reopenIfReplied(s.suggestionQueueState(sg), sg.ID, awaiting) == state {
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

// AnsweredCount is the number of comments the agent settled with a reply and no
// edit (#205).
func (s PrereviewState) AnsweredCount() int { return s.countQueue(queueAnswered) }

// BlockedCount is the number of enqueued comments the agent can no longer be handed
// because their anchor is gone.
func (s PrereviewState) BlockedCount() int { return s.countQueue(queueBlocked) }

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
//
// Deliberately scoped to the whole REVIEW, not the current file — unlike the rest of the
// queue panel (#171). It is a "you are about to lose work" guard, not a work list: a count
// that only saw the file you happen to be looking at would cheerfully let you end the
// session with unapplied accepts stranded on another file, which is the exact failure it
// exists to prevent.
func (s PrereviewState) AwaitingApplyCount() int {
	n := 0
	for _, sg := range s.scopedSuggestions() {
		if s.suggestionAcceptedPending(sg.ID) {
			n++
		}
	}
	return n
}

// AwaitingReplyCount tallies, across the WHOLE REVIEW, every reviewer reply still awaiting
// an agent response — the trailing run of reviewer entries on each comment/suggestion
// thread (#164). It is a per-REPLY counter, not per-comment: three replies in a row on one
// comment count as three, so the reviewer sees each reply register, and the tally drops to
// zero for a thread the moment the agent replies.
//
// reopenIfReplied separately reopens a replied-on comment as "queued" in the per-file
// counts, but QueuedCount/HasQueue are per-file (#171): reply on a comment, then look at
// another file, and that work vanishes from the toolbar. So — like AwaitingApplyCount, and
// for the same "don't strand cross-file work" reason — this tally is deliberately
// whole-review and surfaced independently of the per-file HasQueue gate, so replies are
// always visible on the Queue badge whatever file you are on. A draft never has a thread,
// so it never contributes.
func (s PrereviewState) AwaitingReplyCount() int {
	threads := s.Threads()
	if len(threads) == 0 {
		return 0
	}
	n := 0
	for _, c := range s.scopedComments() {
		n += trailingReviewerReplies(threads[c.ID])
	}
	for _, sg := range s.scopedSuggestions() {
		n += trailingReviewerReplies(threads[sg.ID])
	}
	return n
}

// HasQueue reports whether there's anything to show in the queue panel — any row in
// any lifecycle state, so a file whose only work is blocked (#203) or answered (#205)
// never renders the panel's empty state over the rows that need the reviewer. Gates
// the toolbar indicator so it stays hidden on an empty review.
func (s PrereviewState) HasQueue() bool {
	return len(s.QueueItems()) > 0
}

// AgentWorking mirrors the llm-status echo: true while the agent is applying a
// batch. Drives the "in progress" state of the queue (the queued set is being
// worked) and a live pill.
func (s PrereviewState) AgentWorking() bool { return s.LLMState == LLMStateWorking }

// AgentWorkingLabel is the text for the live "working" pill/toast — non-empty only while
// the agent is working (llm-status=working). It prefers the agent's own status message;
// with none, it tells the reviewer what is happening: applying their reply(ies) when any
// still await a response (#164 secondary — the reviewer wants to see their reply being
// applied), else the generic handoff message. Empty when the agent is idle, so the caller
// can gate on it directly.
func (s PrereviewState) AgentWorkingLabel() string {
	if !s.AgentWorking() {
		return ""
	}
	if s.LLMMessage != "" {
		return s.LLMMessage
	}
	switch n := s.AwaitingReplyCount(); {
	case n == 1:
		return "Applying your reply…"
	case n > 1:
		return "Applying your replies…"
	default:
		return "Working on your handoff…"
	}
}

// QueueItem is one row of the queue panel — a presentation view over a comment or
// an accepted suggestion (Kind distinguishes them so the row can render the ✦
// marker and route its jump to the right target).
type QueueItem struct {
	ID    string
	Kind  string // queueKindComment | queueKindSuggestion
	File  string
	Line  int    // new-side line (0 for file/region/area comments)
	Body  string // the comment text / suggestion note, shown truncated by CSS
	State string // queueDraft | queueQueued | queueBlocked | queueAnswered | queueDone
}

// queueOrder is the panel's row order: what still needs the agent first, then what
// needs the REVIEWER (a lost anchor, an answer to read), then the finished and the
// held-back.
var queueOrder = []string{queueQueued, queueBlocked, queueAnswered, queueDone, queueDraft}

// QueueItems returns the queue rows grouped in queueOrder.
// Resolved comments and un-queued suggestions are excluded (QueueState "") — UNLESS the
// reviewer replied on one last (#164), which reopens it as "queued" work via
// reopenIfReplied.
func (s PrereviewState) QueueItems() []QueueItem {
	buckets := map[string][]QueueItem{}
	add := func(item QueueItem) { buckets[item.State] = append(buckets[item.State], item) }
	awaiting := s.AwaitingAgent()
	for _, c := range s.queueComments() {
		add(QueueItem{ID: c.ID, Kind: queueKindComment, File: c.File, Line: c.ToLine, Body: c.Body, State: reopenIfReplied(c.QueueState(), c.ID, awaiting)})
	}
	for _, sg := range s.queueSuggestions() {
		st := reopenIfReplied(s.suggestionQueueState(sg), sg.ID, awaiting)
		if st == "" {
			continue
		}
		add(QueueItem{ID: sg.ID, Kind: queueKindSuggestion, File: sg.File, Line: sg.ToLine, Body: suggestionQueueBody(sg), State: st})
	}
	var out []QueueItem
	for _, st := range queueOrder { // a "" state (resolved, un-queued) has no bucket here
		out = append(out, buckets[st]...)
	}
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

// Agent phases (#194): what the reviewer can be told about the queue's consumer,
// from what the server observes (agent-cursor.json), not what the agent says.
const (
	AgentPhaseIdle     = "idle"     // nothing has ever read the queue
	AgentPhaseWatching = "watching" // a blocking `watch` is attached right now
	AgentPhaseWorking  = "working"  // the queue was read; nothing attached now
	AgentPhaseStalled  = "stalled"  // working, but silent too long with work outstanding
	AgentPhasePaused   = "paused"   // batching: the reviewer is holding the queue
)

// AgentPhase is the consumer phase, "" outside --agent mode. Precedence: paused
// (the reviewer's own choice explains any silence), watching, stalled, working,
// idle.
func (s PrereviewState) AgentPhase() string {
	switch {
	case !s.AgentMode:
		return ""
	case s.AgentPaused:
		return AgentPhasePaused
	case s.Agent.Attached:
		return AgentPhaseWatching
	case !s.Agent.PickedUp:
		return AgentPhaseIdle
	case s.Agent.Quiet && s.outstandingAgentWork() > 0:
		return AgentPhaseStalled
	default:
		return AgentPhaseWorking
	}
}

// AgentPhaseLabel is the Queue panel's status line (and the stall banner's). The
// agent's own working message wins over the inferred lines (an authored message
// beats inference) — except when stalled: a `working` echo the agent never closed
// with `done` is exactly what a stall looks like, and repeating it would hide the
// one prompt that helps (#203). Paused has none here because the panel's mode
// line already says "Batching". The stall copy never asserts the agent is dead: a
// reasoning turn writes nothing, so silence is ambiguous, and the line says what
// to do if its turn ended.
func (s PrereviewState) AgentPhaseLabel() string {
	phase := s.AgentPhase()
	if l := s.AgentWorkingLabel(); l != "" && phase != AgentPhaseStalled {
		return l
	}
	switch phase {
	case AgentPhaseWatching:
		return "Agent connected — watching your queue"
	case AgentPhaseIdle:
		return "No agent has picked this queue up yet"
	case AgentPhaseStalled:
		return "No word from the agent since " + s.Agent.QuietSince + " — if its turn ended, ask it to run prereview watch"
	case AgentPhaseWorking:
		switch {
		case s.Agent.Polled:
			return "Agent last read your queue at " + s.Agent.PickupAt
		case s.Agent.Behind:
			return "Agent read your queue at " + s.Agent.PickupAt + " — newer comments reach it on its next prereview watch"
		default:
			return "Agent picked up your queue at " + s.Agent.PickupAt
		}
	}
	return ""
}

// outstandingAgentWork counts queued + blocked items across the WHOLE review
// (not the per-file queue filter, #171): the stall warning is about work the
// agent has not finished anywhere, like AwaitingApplyCount.
//
// A comment counts exactly when the snapshot hands it over (commentActionable) or it is
// blocked (#205): a comment whose thread ends with the agent — `answered`, or a plain
// reply-only answer from a skill that predates `--answered` (#208) — gives the agent
// nothing to do, so its silence is not a stall.
func (s PrereviewState) outstandingAgentWork() int {
	awaiting := s.AwaitingAgent()
	threads := s.Threads()
	n := 0
	for _, c := range s.scopedComments() {
		if commentActionable(c, threads[c.ID]) || (c.QueueState() == queueBlocked && !agentRepliedLast(threads[c.ID])) {
			n++
		}
	}
	for _, sg := range s.scopedSuggestions() {
		if st := reopenIfReplied(s.suggestionQueueState(sg), sg.ID, awaiting); st == queueQueued || st == queueBlocked {
			n++
		}
	}
	return n
}

// AgentStalled gates the stall prompt (banners.tmpl).
func (s PrereviewState) AgentStalled() bool { return s.AgentPhase() == AgentPhaseStalled }
