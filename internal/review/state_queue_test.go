package review

import (
	"fmt"
	"testing"
)

func TestQueueDerivation(t *testing.T) {
	mk := func(id string, set func(*Comment)) Comment {
		c := Comment{ID: id, File: "a.go", ToLine: 1, Body: id}
		if set != nil {
			set(&c)
		}
		return c
	}
	s := PrereviewState{
		LLMState: LLMStateWorking,
		// The queue panel shows the CURRENT file's work (#171), so a queue test has to
		// sit on the file its comments are about.
		SelectedFile: "a.go",
		Comments: []Comment{
			mk("q1", nil), // queued (default)
			mk("q2", nil), // queued
			mk("done1", func(c *Comment) { c.Processed = true }),            // done
			mk("draft1", func(c *Comment) { c.Draft = true }),               // draft
			mk("res", func(c *Comment) { c.Resolved = true }),               // excluded
			mk("old", func(c *Comment) { c.AnchorStatus = anchorOutdated }), // blocked (#203)
		},
	}

	if s.QueuedCount() != 2 {
		t.Errorf("QueuedCount = %d, want 2", s.QueuedCount())
	}
	if s.DoneCount() != 1 {
		t.Errorf("DoneCount = %d, want 1", s.DoneCount())
	}
	if s.DraftCount() != 1 {
		t.Errorf("DraftCount = %d, want 1", s.DraftCount())
	}
	if !s.AgentWorking() {
		t.Error("AgentWorking should be true while llm-status=working")
	}
	if !s.HasQueue() {
		t.Error("HasQueue should be true")
	}

	if got := s.BlockedCount(); got != 1 {
		t.Errorf("BlockedCount = %d, want 1", got)
	}

	// QueueItems: queued first, then blocked, then done, then drafts. Only a RESOLVE —
	// an explicit human close — takes a row out of the queue; drift makes it blocked
	// (#203), because a vanished row is indistinguishable from finished work.
	items := s.QueueItems()
	if len(items) != 5 {
		t.Fatalf("QueueItems = %d, want 5 (excl. resolved only)", len(items))
	}
	wantOrder := []string{queueQueued, queueQueued, queueBlocked, queueDone, queueDraft}
	for i, w := range wantOrder {
		if items[i].State != w {
			t.Errorf("item %d state = %q, want %q", i, items[i].State, w)
		}
	}
	for _, it := range items {
		if it.ID == "res" {
			t.Errorf("resolved comment %q leaked into the queue", it.ID)
		}
	}

	// Empty review → no queue indicator.
	if (PrereviewState{}).HasQueue() {
		t.Error("HasQueue should be false on an empty review")
	}
}

// TestQueueReopensOnReviewerReply: a reviewer reply on a settled comment/suggestion
// (its thread ends with the reviewer) reopens it as "queued" work in the toolbar count
// and panel, whatever its base state — #164. This keeps the queue in step with the agent
// snapshot, which re-surfaces the same replied-on items. An agent-last thread does NOT
// reopen (the agent is waiting on the reviewer), and a draft has no thread so it stays put.
func TestQueueReopensOnReviewerReply(t *testing.T) {
	mk := func(id string, set func(*Comment)) Comment {
		c := Comment{ID: id, File: "a.go", ToLine: 1, Body: id}
		if set != nil {
			set(&c)
		}
		return c
	}
	replied := func(id string) []ThreadEntry { // reviewer speaks last → reopens
		return []ThreadEntry{
			{TargetID: id, Author: AuthorAgent, At: 1},
			{TargetID: id, Author: AuthorReviewer, Body: "one more thing", At: 2},
		}
	}
	var threads []ThreadEntry
	for _, id := range []string{"done1", "res", "old", "sapp"} {
		threads = append(threads, replied(id)...)
	}
	// done2: reviewer then agent — agent-last, still handled, must stay "done".
	threads = append(threads,
		ThreadEntry{TargetID: "done2", Author: AuthorReviewer, At: 1},
		ThreadEntry{TargetID: "done2", Author: AuthorAgent, At: 2})

	s := PrereviewState{
		SelectedFile: "a.go",
		Comments: []Comment{
			mk("q1", nil), // queued (default)
			mk("done1", func(c *Comment) { c.Processed = true }),            // done + reply → reopened
			mk("done2", func(c *Comment) { c.Processed = true }),            // done + agent-last → stays done
			mk("res", func(c *Comment) { c.Resolved = true }),               // resolved + reply → reopened
			mk("old", func(c *Comment) { c.AnchorStatus = anchorOutdated }), // outdated + reply → reopened
			mk("draft1", func(c *Comment) { c.Draft = true }),               // draft, no thread → stays draft
		},
		Suggestions:   []Suggestion{{ID: "sapp", File: "a.go", ToLine: 5}}, // applied + reply → reopened
		Decisions:     []SuggestionDecision{{SuggestionID: "sapp", Verdict: verdictAccept}},
		Applied:       map[string]bool{"sapp": true},
		ThreadEntries: threads,
	}

	// Reopened: done1, res, old (comments) + sapp (suggestion), plus the fresh q1 → 5 queued.
	if got := s.QueuedCount(); got != 5 {
		t.Errorf("QueuedCount = %d, want 5 (q1 + done1/res/old + sapp reopened)", got)
	}
	// Only the agent-last done comment stays "done"; done1 and sapp left the done pile.
	if got := s.DoneCount(); got != 1 {
		t.Errorf("DoneCount = %d, want 1 (only the agent-last done stays done)", got)
	}
	if got := s.DraftCount(); got != 1 {
		t.Errorf("DraftCount = %d, want 1 (a draft has no thread, never reopens)", got)
	}

	states := map[string]string{}
	for _, it := range s.QueueItems() {
		states[it.ID] = it.State
	}
	for _, id := range []string{"done1", "res", "old", "sapp"} {
		if states[id] != queueQueued {
			t.Errorf("reopened %q state = %q, want queued", id, states[id])
		}
	}
	if states["done2"] != queueDone {
		t.Errorf("agent-last done2 state = %q, want done (not reopened)", states["done2"])
	}
	if states["draft1"] != queueDraft {
		t.Errorf("draft1 state = %q, want draft", states["draft1"])
	}
}

// TestAwaitingReplyCount_PerReplyReviewWide: the toolbar reply tally (#164) is per-REPLY
// (three replies in a row on one comment count as three), whole-review (a reply on a
// non-selected file still counts, where the per-file QueuedCount would drop it), and drops
// to zero for a thread the moment the agent replies.
func TestAwaitingReplyCount_PerReplyReviewWide(t *testing.T) {
	s := PrereviewState{
		SelectedFile: "a.go",
		Comments: []Comment{
			{ID: "here", File: "a.go", ToLine: 1},
			{ID: "elsewhere", File: "b.go", ToLine: 2, Processed: true}, // different file
			{ID: "answered", File: "a.go", ToLine: 3, Processed: true},  // agent replied last
		},
		ThreadEntries: []ThreadEntry{
			// "here": reviewer replied THREE times in a row → 3 unaddressed.
			{TargetID: "here", Author: AuthorReviewer, At: 1},
			{TargetID: "here", Author: AuthorReviewer, At: 2},
			{TargetID: "here", Author: AuthorReviewer, At: 3},
			// "elsewhere": one reply, on another file → still counts (review-wide).
			{TargetID: "elsewhere", Author: AuthorAgent, At: 1},
			{TargetID: "elsewhere", Author: AuthorReviewer, At: 2},
			// "answered": reviewer replied, then the agent answered → 0 unaddressed.
			{TargetID: "answered", Author: AuthorReviewer, At: 1},
			{TargetID: "answered", Author: AuthorAgent, At: 2},
		},
	}

	// Per-file QueuedCount (viewing a.go) counts COMMENTS, not replies: "here" (reopened)
	// is one queued item however many times the reviewer replied; "elsewhere" is off-file.
	if got := s.QueuedCount(); got != 1 {
		t.Errorf("QueuedCount (per-file, a.go) = %d, want 1 (only 'here' as one item)", got)
	}
	// The tally is per-reply and whole-review: 3 (here) + 1 (elsewhere) + 0 (answered) = 4.
	if got := s.AwaitingReplyCount(); got != 4 {
		t.Errorf("AwaitingReplyCount = %d, want 4 (3 on 'here' + 1 cross-file + 0 answered)", got)
	}

	// A lone cross-file reply: per-file queue is empty (HasQueue false) yet the tally is 1,
	// so the badge must surface it independent of HasQueue.
	only := PrereviewState{
		SelectedFile:  "a.go",
		Comments:      []Comment{{ID: "elsewhere", File: "b.go", ToLine: 2, Processed: true}},
		ThreadEntries: []ThreadEntry{{TargetID: "elsewhere", Author: AuthorReviewer, At: 1}},
	}
	if only.HasQueue() {
		t.Error("HasQueue is per-file; a lone cross-file reply must not flip it true")
	}
	if got := only.AwaitingReplyCount(); got != 1 {
		t.Errorf("AwaitingReplyCount = %d, want 1 even when HasQueue is false", got)
	}
}

// TestTrailingReviewerReplies covers the per-reply counting primitive directly.
func TestTrailingReviewerReplies(t *testing.T) {
	rev, ag := ThreadEntry{Author: AuthorReviewer}, ThreadEntry{Author: AuthorAgent}
	cases := []struct {
		name   string
		thread []ThreadEntry
		want   int
	}{
		{"empty", nil, 0},
		{"ends with agent", []ThreadEntry{rev, ag}, 0},
		{"one trailing reviewer", []ThreadEntry{ag, rev}, 1},
		{"three trailing reviewers", []ThreadEntry{ag, rev, rev, rev}, 3},
		{"reviewer run reset by agent", []ThreadEntry{rev, rev, ag, rev}, 1},
		{"all reviewer, no agent", []ThreadEntry{rev, rev}, 2},
	}
	for _, c := range cases {
		if got := trailingReviewerReplies(c.thread); got != c.want {
			t.Errorf("%s: trailingReviewerReplies = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestAgentWorkingLabel: the live working pill/toast text (#164 secondary). Idle → empty;
// an explicit agent message always wins; otherwise it reflects pending replies.
func TestAgentWorkingLabel(t *testing.T) {
	// One comment with a reviewer-last thread, so AwaitingReplyCount tracks the entries.
	base := func(entries ...ThreadEntry) PrereviewState {
		return PrereviewState{
			SelectedFile:  "a.go",
			Comments:      []Comment{{ID: "c", File: "a.go", ToLine: 1}},
			ThreadEntries: entries,
		}
	}
	oneReply := []ThreadEntry{{TargetID: "c", Author: AuthorReviewer, At: 1}}
	twoReplies := []ThreadEntry{{TargetID: "c", Author: AuthorReviewer, At: 1}, {TargetID: "c", Author: AuthorReviewer, At: 2}}

	// Idle → no label (so the caller renders no pill).
	if got := base(oneReply...).AgentWorkingLabel(); got != "" {
		t.Errorf("idle label = %q, want empty", got)
	}

	// Working with an explicit agent message → the message wins, replies notwithstanding.
	s := base(oneReply...)
	s.LLMState, s.LLMMessage = LLMStateWorking, "editing a.go"
	if got := s.AgentWorkingLabel(); got != "editing a.go" {
		t.Errorf("label with message = %q, want %q", got, "editing a.go")
	}

	// Working, no message, one pending reply → reply-aware fallback.
	s = base(oneReply...)
	s.LLMState = LLMStateWorking
	if got := s.AgentWorkingLabel(); got != "Applying your reply…" {
		t.Errorf("label (1 reply) = %q, want %q", got, "Applying your reply…")
	}

	// Working, no message, multiple pending replies → plural.
	s = base(twoReplies...)
	s.LLMState = LLMStateWorking
	if got := s.AgentWorkingLabel(); got != "Applying your replies…" {
		t.Errorf("label (2 replies) = %q, want %q", got, "Applying your replies…")
	}

	// Working, no message, no pending replies → generic handoff fallback (unchanged).
	s = base()
	s.LLMState = LLMStateWorking
	if got := s.AgentWorkingLabel(); got != "Working on your handoff…" {
		t.Errorf("label (no replies) = %q, want %q", got, "Working on your handoff…")
	}
}

// TestSuggestionQueueProjection: an accepted suggestion is "queued" work (the
// agent still has to apply it), an applied one is "done", and reject/undecided
// stay out of the queue (#159). Suggestions ride the same counts/rows as comments.
func TestSuggestionQueueProjection(t *testing.T) {
	s := PrereviewState{
		SelectedFile: "a.go", // the queue panel is per-file (#171)
		Suggestions: []Suggestion{
			{ID: "acc", File: "a.go", ToLine: 3, Note: "fix grammar"}, // accepted → queued
			{ID: "app", File: "a.go", ToLine: 7},                      // applied → done (no note → fallback body)
			{ID: "rej", File: "a.go", ToLine: 9},                      // rejected → excluded
			{ID: "und", File: "a.go", ToLine: 11},                     // undecided → excluded
		},
		Decisions: []SuggestionDecision{
			{SuggestionID: "acc", Verdict: verdictAccept},
			{SuggestionID: "app", Verdict: verdictAccept}, // decision still accept; Applied wins
			{SuggestionID: "rej", Verdict: verdictReject},
		},
		Applied: map[string]bool{"app": true},
	}

	byID := map[string]Suggestion{}
	for _, sg := range s.Suggestions {
		byID[sg.ID] = sg
	}
	if got := s.suggestionQueueState(byID["acc"]); got != queueQueued {
		t.Errorf("accepted suggestion state = %q, want queued", got)
	}
	if got := s.suggestionQueueState(byID["app"]); got != queueDone {
		t.Errorf("applied suggestion state = %q, want done (Applied beats the accept decision)", got)
	}
	if got := s.suggestionQueueState(byID["rej"]); got != "" {
		t.Errorf("rejected suggestion state = %q, want excluded", got)
	}
	if got := s.suggestionQueueState(byID["und"]); got != "" {
		t.Errorf("undecided suggestion state = %q, want excluded", got)
	}

	if s.QueuedCount() != 1 || s.DoneCount() != 1 {
		t.Errorf("counts: queued=%d done=%d, want 1/1", s.QueuedCount(), s.DoneCount())
	}
	if !s.HasQueue() {
		t.Error("a review with only suggestions should still show the queue")
	}

	items := s.QueueItems()
	if len(items) != 2 {
		t.Fatalf("QueueItems = %d, want 2 (acc queued, app done)", len(items))
	}
	// queued first, then done.
	if items[0].ID != "acc" || items[0].State != queueQueued || items[0].Kind != queueKindSuggestion {
		t.Errorf("item[0] = %+v, want acc/queued/suggestion", items[0])
	}
	if items[0].Body != "fix grammar" {
		t.Errorf("item[0] body = %q, want the note", items[0].Body)
	}
	if items[1].ID != "app" || items[1].State != queueDone {
		t.Errorf("item[1] = %+v, want app/done", items[1])
	}
	if items[1].Body != "Suggested edit" {
		t.Errorf("item[1] body = %q, want the no-note fallback", items[1].Body)
	}
}

// TestQueueDoneSurvivesAnchorDrift is the #203 regression: the agent's OWN edit is
// what usually invalidates the anchor of the comment it just addressed, so keying the
// queue lifecycle off drift makes Done fall back down every time the agent succeeds.
// Small queues hid it — with 8 comments on one document the agent rewrites enough of
// the file that most of the already-done anchors are gone by the end, and the reviewer
// watches Done stall well short of the total.
//
// Drift is something that happens TO a comment, not a lifecycle transition: done is
// done. suggestionQueueState has always known this ("checked FIRST, since an applied
// suggestion is also anchor-outdated"); the comment path did not.
func TestQueueDoneSurvivesAnchorDrift(t *testing.T) {
	var comments []Comment
	for i := range 8 {
		c := Comment{ID: fmt.Sprintf("c%d", i), File: "a.go", ToLine: i + 1, Body: "b", Processed: true}
		// The agent edited these five regions hard enough that re-anchoring gave up.
		if i < 5 {
			c.AnchorStatus = anchorOutdated
		}
		comments = append(comments, c)
	}
	s := PrereviewState{SelectedFile: "a.go", Comments: comments}

	if got := s.DoneCount(); got != 8 {
		t.Errorf("DoneCount = %d, want 8 — every comment was marked done; the agent's own "+
			"edits breaking their anchors must not un-do them", got)
	}
	if got := s.QueuedCount(); got != 0 {
		t.Errorf("QueuedCount = %d, want 0 — nothing is still waiting on the agent", got)
	}
}

// TestQueueBlockedOnDrift: an enqueued comment the agent has NOT done, whose anchor is
// gone, is not queued (the agent is never handed outdated work) and not done — but it
// must not vanish either. It is blocked on the reviewer re-anchoring or resolving it.
// Before #203 this row silently left both counts, so the queue's total shrank with no
// badge, no legend entry and no log line.
func TestQueueBlockedOnDrift(t *testing.T) {
	s := PrereviewState{
		SelectedFile: "a.go",
		Comments: []Comment{
			{ID: "q", File: "a.go", ToLine: 1, Body: "b"},
			{ID: "drifted", File: "a.go", ToLine: 2, Body: "b", AnchorStatus: anchorOutdated},
			{ID: "d", File: "a.go", ToLine: 3, Body: "b", Processed: true},
		},
	}

	if got := s.Comments[1].QueueState(); got != queueBlocked {
		t.Errorf("drifted QueueState = %q, want %q", got, queueBlocked)
	}
	if got := s.BlockedCount(); got != 1 {
		t.Errorf("BlockedCount = %d, want 1", got)
	}
	if got := s.QueuedCount() + s.BlockedCount() + s.DoneCount(); got != 3 {
		t.Errorf("queued+blocked+done = %d, want 3 — the buckets must conserve the "+
			"enqueued total, or work is disappearing", got)
	}
	states := map[string]string{}
	for _, it := range s.QueueItems() {
		states[it.ID] = it.State
	}
	if states["drifted"] != queueBlocked {
		t.Errorf("drifted row state = %q, want %q (the row must still render)", states["drifted"], queueBlocked)
	}
}

// TestQueueHasQueueWithOnlyBlocked guards the trap in making drift visible: HasQueue
// gates the whole panel body, so a file whose ONLY work is blocked would collapse to
// the empty state and hide the very row this change exists to surface.
func TestQueueHasQueueWithOnlyBlocked(t *testing.T) {
	s := PrereviewState{
		SelectedFile: "a.go",
		Comments:     []Comment{{ID: "drifted", File: "a.go", ToLine: 1, Body: "b", AnchorStatus: anchorOutdated}},
	}
	if !s.HasQueue() {
		t.Error("HasQueue = false with a blocked row — the panel would render its empty " +
			"state and the blocked comment would be invisible")
	}
}

// TestQueueBucketsConserveTotal is the invariant that makes "the Done count never reaches
// the total" checkable at all: with the per-file default (#171), the buckets the reviewer
// can see — queued + blocked + done + draft on this file, plus QueueHiddenCount for the
// rest of the review — must account for EVERY unresolved comment. If the five together
// come up short, work has gone somewhere the reviewer cannot look, which is exactly the
// #203 failure.
func TestQueueBucketsConserveTotal(t *testing.T) {
	mk := func(id, file string, set func(*Comment)) Comment {
		c := Comment{ID: id, File: file, ToLine: 1, Body: id}
		if set != nil {
			set(&c)
		}
		return c
	}
	outdated := func(c *Comment) { c.AnchorStatus = anchorOutdated }
	processed := func(c *Comment) { c.Processed = true }
	draft := func(c *Comment) { c.Draft = true }

	s := PrereviewState{
		SelectedFile: "a.go",
		Comments: []Comment{
			mk("a-queued", "a.go", nil),
			mk("a-blocked", "a.go", outdated),
			mk("a-done", "a.go", processed),
			mk("a-draft", "a.go", draft),
			mk("b-queued", "b.go", nil),
			mk("b-blocked", "b.go", outdated),
			mk("b-done", "b.go", processed),
			mk("b-draft", "b.go", draft),
			mk("a-resolved", "a.go", func(c *Comment) { c.Resolved = true }),
		},
	}

	unresolved := 0
	for _, c := range s.Comments {
		if !c.Resolved {
			unresolved++
		}
	}
	got := s.QueuedCount() + s.BlockedCount() + s.DoneCount() + s.DraftCount() + s.QueueHiddenCount()
	if got != unresolved {
		t.Errorf("queued(%d)+blocked(%d)+done(%d)+draft(%d)+elsewhere(%d) = %d, want %d — the "+
			"visible buckets must account for every unresolved comment, or work is vanishing",
			s.QueuedCount(), s.BlockedCount(), s.DoneCount(), s.DraftCount(),
			s.QueueHiddenCount(), got, unresolved)
	}

	// Flipping to All files moves the same work from "elsewhere" into the visible buckets;
	// the total is unchanged.
	s.QueueGlobal = true
	if s.QueueHiddenCount() != 0 {
		t.Errorf("QueueHiddenCount = %d with Global on, want 0", s.QueueHiddenCount())
	}
	if got := s.QueuedCount() + s.BlockedCount() + s.DoneCount() + s.DraftCount(); got != unresolved {
		t.Errorf("global buckets = %d, want %d", got, unresolved)
	}
}

// TestSuggestionBlockedOnDrift: an ACCEPTED suggestion the agent has not applied yet, whose
// OriginalText has since vanished from the file, cannot be applied — actionableDecisions
// drops it, so the agent will never pick it up. Calling it "queued" promises a pickup that
// never comes; it is blocked on the reviewer, exactly like a drifted comment (#203).
//
// AnchorStatus is re-derived from OriginalText on every load, so this is reachable
// independently of Applied — the file only has to move under an unapplied accept.
func TestSuggestionBlockedOnDrift(t *testing.T) {
	s := PrereviewState{
		SelectedFile: "a.go",
		Suggestions: []Suggestion{
			{ID: "acc", File: "a.go", ToLine: 3, Note: "fix grammar"},
			{ID: "gone", File: "a.go", ToLine: 5, Note: "rewrite", AnchorStatus: anchorOutdated},
		},
		Decisions: []SuggestionDecision{
			{SuggestionID: "acc", Verdict: verdictAccept},
			{SuggestionID: "gone", Verdict: verdictAccept},
		},
	}
	byID := map[string]Suggestion{}
	for _, sg := range s.Suggestions {
		byID[sg.ID] = sg
	}
	if got := s.suggestionQueueState(byID["gone"]); got != queueBlocked {
		t.Errorf("accepted-but-drifted suggestion state = %q, want %q", got, queueBlocked)
	}
	if got := s.suggestionQueueState(byID["acc"]); got != queueQueued {
		t.Errorf("accepted, placeable suggestion state = %q, want %q", got, queueQueued)
	}
	if s.QueuedCount() != 1 || s.BlockedCount() != 1 {
		t.Errorf("counts: queued=%d blocked=%d, want 1/1", s.QueuedCount(), s.BlockedCount())
	}
}
