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
		Comments: []Comment{
			mk("q1", nil),                                      // queued (default)
			mk("q2", nil),                                      // queued
			mk("done1", func(c *Comment) { c.Processed = true }), // done
			mk("draft1", func(c *Comment) { c.Draft = true }),  // draft
			mk("res", func(c *Comment) { c.Resolved = true }),  // excluded
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
