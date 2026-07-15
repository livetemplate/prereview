package review

import "testing"

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
			mk("old", func(c *Comment) { c.AnchorStatus = anchorOutdated }), // excluded
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

	// QueueItems: queued first, then done, then drafts; resolved/outdated excluded.
	items := s.QueueItems()
	if len(items) != 4 {
		t.Fatalf("QueueItems = %d, want 4 (excl. resolved+outdated)", len(items))
	}
	wantOrder := []string{queueQueued, queueQueued, queueDone, queueDraft}
	for i, w := range wantOrder {
		if items[i].State != w {
			t.Errorf("item %d state = %q, want %q", i, items[i].State, w)
		}
	}
	for _, it := range items {
		if it.ID == "res" || it.ID == "old" {
			t.Errorf("resolved/outdated comment %q leaked into the queue", it.ID)
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

	if got := s.suggestionQueueState("acc"); got != queueQueued {
		t.Errorf("accepted suggestion state = %q, want queued", got)
	}
	if got := s.suggestionQueueState("app"); got != queueDone {
		t.Errorf("applied suggestion state = %q, want done (Applied beats the accept decision)", got)
	}
	if got := s.suggestionQueueState("rej"); got != "" {
		t.Errorf("rejected suggestion state = %q, want excluded", got)
	}
	if got := s.suggestionQueueState("und"); got != "" {
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
