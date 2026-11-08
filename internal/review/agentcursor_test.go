package review

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentCursor_ReadContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, AgentCursorFileName)

	if _, _, err := readAgentCursor(path); !os.IsNotExist(err) {
		t.Fatalf("missing file: err = %v, want not-exist", err)
	}
	if err := WriteAgentCursor(dir, AgentCursor{Mode: AgentCursorWatch, State: AgentCursorWaiting, DeliveredSeq: 4}); err != nil {
		t.Fatal(err)
	}
	c, mtime, err := readAgentCursor(path)
	if err != nil || c.Mode != AgentCursorWatch || c.State != AgentCursorWaiting || c.DeliveredSeq != 4 || mtime.IsZero() {
		t.Fatalf("round-trip = %+v, %v, %v", c, mtime, err)
	}
	if err := os.WriteFile(path, []byte(`{"mode":"wat`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readAgentCursor(path); err == nil || os.IsNotExist(err) {
		t.Fatalf("torn file: err = %v, want a parse error", err)
	}
}

func TestAgentCursor_AttachedAndPickedUp(t *testing.T) {
	now := time.Now()
	waiting := AgentCursor{Mode: AgentCursorWatch, State: AgentCursorWaiting, DeliveredSeq: -1}
	if !waiting.Attached(now.Add(-AgentCursorStale/2), now) {
		t.Error("a fresh waiting watch should be attached")
	}
	if waiting.Attached(now.Add(-AgentCursorStale), now) {
		t.Error("a heartbeat AgentCursorStale old should not be attached")
	}
	if waiting.PickedUp() {
		t.Error("a watch that has seen nothing has not picked the queue up")
	}
	delivered := AgentCursor{Mode: AgentCursorWatch, State: AgentCursorDelivered, DeliveredSeq: 3}
	if delivered.Attached(now, now) || !delivered.PickedUp() {
		t.Error("a delivered watch is picked up and not attached")
	}
	poll := AgentCursor{Mode: AgentCursorPoll, State: AgentCursorDelivered, DeliveredSeq: -1}
	if poll.Attached(now, now) || !poll.PickedUp() {
		t.Error("a poll never claims attachment but counts as pickup")
	}
}

// TestAgentWatchKey_StableAcrossHeartbeats is the "no periodic fan-out" guard
// (#194): heartbeats that change nothing else must move neither the sidecar
// fingerprint nor the presence key, or every beat re-renders every tab.
func TestAgentWatchKey_StableAcrossHeartbeats(t *testing.T) {
	dir := t.TempDir()
	c := &PrereviewController{CSVPath: filepath.Join(dir, CommentsFileName)}
	key := func() string { return c.agentSignalFingerprint() + c.agentPresence(time.Now(), time.Time{}).key() }
	beat := func() {
		if err := WriteAgentCursor(dir, AgentCursor{Mode: AgentCursorWatch, State: AgentCursorWaiting, DeliveredSeq: 2}); err != nil {
			t.Fatal(err)
		}
	}
	beat()
	first := key()
	for range 3 {
		time.Sleep(15 * time.Millisecond) // a distinct mtime each beat
		beat()
		if got := key(); got != first {
			t.Fatalf("watch key moved on a heartbeat:\n  %q\n  %q", first, got)
		}
	}
	// A real transition — the watch delivers — does move it.
	if err := WriteAgentCursor(dir, AgentCursor{Mode: AgentCursorWatch, State: AgentCursorDelivered, DeliveredSeq: 3}); err != nil {
		t.Fatal(err)
	}
	if key() == first {
		t.Fatal("watch key unchanged on a delivery")
	}
}

func TestAgentPresence(t *testing.T) {
	dir := t.TempDir()
	es := NewEventStream(&syncBuffer{}, "")
	c := &PrereviewController{CSVPath: filepath.Join(dir, CommentsFileName), Emitter: es}
	now := time.Now()
	if p := c.agentPresence(now, time.Time{}); p != (AgentPresence{}) {
		t.Fatalf("no cursor: %+v, want zero (never picked up)", p)
	}
	for range 3 {
		_ = es.EmitFilesChanged(nil, 0, "", false, fixedTS) // seqs 0..2 (not `end`: nothing follows that)
	}
	if err := WriteAgentCursor(dir, AgentCursor{Mode: AgentCursorWatch, State: AgentCursorDelivered, DeliveredSeq: 2}); err != nil {
		t.Fatal(err)
	}
	p := c.agentPresence(now, time.Time{})
	if p.Quiet || p.Attached || !p.PickedUp || p.Behind || p.PickupAt == "" {
		t.Fatalf("just delivered the latest: %+v", p)
	}
	_ = es.EmitFilesChanged(nil, 0, "", false, fixedTS) // seq 3, not delivered
	if p := c.agentPresence(now, time.Time{}); !p.Behind {
		t.Fatalf("an undelivered emit: %+v, want behind", p)
	}
	stall := now.Add(AgentStallAfter + time.Second)
	if p := c.agentPresence(stall, time.Time{}); !p.Quiet {
		t.Fatalf("past AgentStallAfter: %+v, want quiet", p)
	}
	// Any other agent signal the watcher saw resets the clock.
	if p := c.agentPresence(stall, now.Add(AgentStallAfter)); p.Quiet {
		t.Fatalf("a recent agent signal should reset the stall clock: %+v", p)
	}
}

func TestEventStream_LastSeq(t *testing.T) {
	var nilStream *EventStream
	if got := nilStream.LastSeq(); got != -1 {
		t.Errorf("nil stream LastSeq = %d, want -1", got)
	}
	es := NewEventStream(&syncBuffer{}, "")
	if got := es.LastSeq(); got != -1 {
		t.Errorf("fresh stream LastSeq = %d, want -1", got)
	}
	_ = es.EmitFilesChanged(nil, 0, "", false, fixedTS)
	_ = es.EmitEnd(fixedTS)
	if got := es.LastSeq(); got != 1 {
		t.Errorf("after seq 0,1 LastSeq = %d, want 1", got)
	}
	// #228: nothing follows `end`, so a later emit takes no seq.
	_ = es.EmitFilesChanged(nil, 0, "", false, fixedTS)
	if got := es.LastSeq(); got != 1 {
		t.Errorf("an emit after end took a seq: LastSeq = %d, want 1", got)
	}
}

func TestAgentPhase(t *testing.T) {
	queued := []Comment{{ID: "c", File: "a.go", ToLine: 1}}
	agent := func(p AgentPresence) PrereviewState { return PrereviewState{AgentMode: true, Agent: p} }
	picked := AgentPresence{PickedUp: true, PickupAt: "2:03 PM"}
	with := func(s PrereviewState, f func(*PrereviewState)) PrereviewState { f(&s); return s }
	cases := []struct {
		name  string
		s     PrereviewState
		phase string
		label string
	}{
		{"not agent mode", PrereviewState{Agent: AgentPresence{Attached: true}}, "", ""},
		{"never read", agent(AgentPresence{}), AgentPhaseIdle, "No agent has picked this queue up yet"},
		{"paused wins", with(agent(AgentPresence{Attached: true}), func(s *PrereviewState) { s.AgentPaused = true }), AgentPhasePaused, ""},
		{"attached", agent(AgentPresence{Attached: true}), AgentPhaseWatching, "Agent connected — watching your queue"},
		{"picked up, has latest", agent(picked), AgentPhaseWorking, "Agent picked up your queue at 2:03 PM"},
		{"picked up, newer comments", agent(AgentPresence{PickedUp: true, PickupAt: "2:03 PM", Behind: true}), AgentPhaseWorking, "Agent read your queue at 2:03 PM — newer comments reach it on its next prereview watch"},
		{"poll", agent(AgentPresence{PickedUp: true, Polled: true, PickupAt: "2:03 PM"}), AgentPhaseWorking, "Agent last read your queue at 2:03 PM"},
		{"quiet, nothing outstanding", agent(AgentPresence{PickedUp: true, Quiet: true, PickupAt: "2:03 PM"}), AgentPhaseWorking, "Agent picked up your queue at 2:03 PM"},
		{"quiet with queued work on another file", with(agent(AgentPresence{PickedUp: true, Quiet: true, QuietSince: "2:05 PM"}), func(s *PrereviewState) { s.Comments, s.SelectedFile = queued, "other.go" }), AgentPhaseStalled, "No word from the agent since 2:05 PM — if its turn ended, ask it to run prereview watch"},
		{"a stale working echo does not hide the stall", with(agent(AgentPresence{PickedUp: true, Quiet: true, QuietSince: "2:05 PM"}), func(s *PrereviewState) {
			s.Comments, s.LLMState, s.LLMMessage = queued, LLMStateWorking, "Applying your review"
		}), AgentPhaseStalled, "No word from the agent since 2:05 PM — if its turn ended, ask it to run prereview watch"},
		{"agent's own message wins", with(agent(AgentPresence{Attached: true}), func(s *PrereviewState) { s.LLMState, s.LLMMessage = LLMStateWorking, "editing a.go" }), AgentPhaseWatching, "editing a.go"},
	}
	for _, c := range cases {
		if got := c.s.AgentPhase(); got != c.phase {
			t.Errorf("%s: phase = %q, want %q", c.name, got, c.phase)
		}
		if got := c.s.AgentPhaseLabel(); got != c.label {
			t.Errorf("%s: label = %q, want %q", c.name, got, c.label)
		}
	}
}

func TestRecordPoll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, AgentCursorFileName)
	if err := RecordPoll(dir); err != nil {
		t.Fatal(err)
	}
	if c, _, err := readAgentCursor(path); err != nil || c.Mode != AgentCursorPoll || !c.PickedUp() {
		t.Fatalf("poll stamp = %+v, %v", c, err)
	}
	// A live watch keeps its stamp: a poll must not knock it off "attached".
	if err := WriteAgentCursor(dir, AgentCursor{Mode: AgentCursorWatch, State: AgentCursorWaiting, DeliveredSeq: 3}); err != nil {
		t.Fatal(err)
	}
	if err := RecordPoll(dir); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := readAgentCursor(path); c.Mode != AgentCursorWatch {
		t.Fatalf("poll overwrote an attached watch: %+v", c)
	}
}
