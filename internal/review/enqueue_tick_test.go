package review

import "testing"

// TestEnqueueTick_BumpsOnGenuineEnqueueOnly (#129): EnqueueTick drives the Queue
// button's one-shot pulse, so it must bump when a comment enters the queue and NOT
// on an unrelated mutation — otherwise the button would pulse on resolve/delete and
// the confirmation would be meaningless.
func TestEnqueueTick_BumpsOnGenuineEnqueueOnly(t *testing.T) {
	c := draftController(t)
	st := PrereviewState{Comments: []Comment{
		{ID: "x", File: "a.go", FromLine: 1, ToLine: 1, Side: "new", Body: "n", Draft: true},
	}}
	if err := c.persist(st.Comments); err != nil {
		t.Fatal(err)
	}

	// Enqueuing a held draft bumps the tick (a comment entered the queue).
	before := st.EnqueueTick
	st, _ = c.EnqueueComment(st, draftCtx("enqueueComment", "x"))
	if st.EnqueueTick != before+1 {
		t.Fatalf("enqueue should bump the tick: %d → %d", before, st.EnqueueTick)
	}

	// Resolving is not an enqueue — the tick must stay put.
	before = st.EnqueueTick
	st, _ = c.ToggleResolved(st, draftCtx("toggleResolved", "x"))
	if st.EnqueueTick != before {
		t.Errorf("resolving must not bump the enqueue tick: %d → %d", before, st.EnqueueTick)
	}

	// Re-opening it (another toggle) also isn't an enqueue.
	before = st.EnqueueTick
	st, _ = c.ToggleResolved(st, draftCtx("toggleResolved", "x"))
	if st.EnqueueTick != before {
		t.Errorf("reopening must not bump the enqueue tick: %d → %d", before, st.EnqueueTick)
	}
}
