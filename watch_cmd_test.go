package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/livetemplate/prereview/internal/review"
)

// syncBuf is a concurrency-safe io.Writer so the follow goroutine and the test
// assertion don't race on the captured output.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append: %v", err)
	}
}

const testPoll = 5 * time.Millisecond

// TestFollowEvents_SinceFilterAndSessionEnd: --since drops already-seen events
// and end terminates the stream (exit path, no follow needed since the
// terminator is already on disk).
func TestFollowEvents_SinceFilterAndSessionEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"event":"ready","seq":0}`,
		`{"event":"snapshot","seq":1}`,
		`{"event":"snapshot","seq":2}`,
		`{"event":"end","seq":3}`,
		"",
	}, "\n"))

	var buf syncBuf
	if err := followEvents(path, 1, &buf, testPoll); err != nil {
		t.Fatalf("followEvents: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, `"seq":0`) || strings.Contains(got, `"seq":1`) {
		t.Errorf("since=1 should drop seq 0 and 1; got:\n%s", got)
	}
	if !strings.Contains(got, `"seq":2`) || !strings.Contains(got, `"seq":3`) {
		t.Errorf("expected seq 2 and 3; got:\n%s", got)
	}
}

// TestFollowEvents_TornLineTolerance: a garbage line is skipped and the stream
// still terminates cleanly on end.
func TestFollowEvents_TornLineTolerance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"event":"ready","seq":0}`,
		`{not valid json at all`,
		`{"event":"end","seq":1}`,
		"",
	}, "\n"))

	var buf syncBuf
	if err := followEvents(path, -1, &buf, testPoll); err != nil {
		t.Fatalf("followEvents: %v", err)
	}
	if strings.Contains(buf.String(), "not valid json") {
		t.Errorf("torn line should be dropped; got:\n%s", buf.String())
	}
}

// TestFollowEvents_RealEmitterContract wires the REAL review.EventStream to the
// reader, so a format mismatch between what the server writes and what the reader
// parses (field names, seq semantics, end serialization) is caught —
// every other test hand-authors the fixture, which only confirms our own
// assumptions about the wire shape.
func TestFollowEvents_RealEmitterContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), review.EventsFileName)
	es := review.NewEventStream(io.Discard, path) // discard the live channel; assert the durable mirror
	ts := time.Unix(0, 0)

	if err := es.EmitReady("/repo", "/repo/.prereview/comments.csv", false, false, ts); err != nil {
		t.Fatalf("EmitReady: %v", err)
	}
	c := review.Comment{ID: "c1", File: "a.go", FromLine: 1, ToLine: 1, Side: "new", Body: "fix this", Created: ts}
	if err := es.EmitSnapshot([]review.Comment{c}, nil, nil, nil, false, ts); err != nil {
		t.Fatalf("EmitSnapshot: %v", err)
	}
	if err := es.EmitEnd(ts); err != nil {
		t.Fatalf("EmitEnd: %v", err)
	}

	var buf syncBuf
	if err := followEvents(path, -1, &buf, testPoll); err != nil {
		t.Fatalf("followEvents: %v", err)
	}
	out := buf.String()
	for _, want := range []string{`"event":"ready"`, `"event":"snapshot"`, `"c1"`, `"from_line":1`, `"event":"end"`} {
		if !strings.Contains(out, want) {
			t.Errorf("reader did not deliver %s from the real emitter output:\n%s", want, out)
		}
	}
}

// TestFollowEvents_ReturnsAfterCatchupBatch pins Model B's core promise: once
// catch-up has delivered a batch, the reader RETURNS (so the agent can act) — it
// does NOT keep following until end. This path has no end, so it
// isolates the emitted>0 return that the end tests would otherwise mask.
func TestFollowEvents_ReturnsAfterCatchupBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"event":"ready","seq":0}`,
		`{"event":"snapshot","seq":1}`,
		"",
	}, "\n"))

	var buf syncBuf
	done := make(chan error, 1)
	go func() { done <- followEvents(path, -1, &buf, testPoll) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("followEvents: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("followEvents must return after the catch-up batch (no end needed)")
	}
	if !strings.Contains(buf.String(), `"seq":1`) {
		t.Errorf("expected the catch-up batch; got:\n%s", buf.String())
	}
}

// TestFollowEvents_BlocksThenReturnsOnAppend: when the cursor is already caught
// up (since=0 filters the only on-disk event), the reader BLOCKS, then returns
// the next appended event (delivering control so the agent can act).
func TestFollowEvents_BlocksThenReturnsOnAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeFile(t, path, `{"event":"ready","seq":0}`+"\n")

	var buf syncBuf
	done := make(chan error, 1)
	go func() { done <- followEvents(path, 0, &buf, testPoll) }() // since=0 ⇒ ready@0 filtered ⇒ block

	time.Sleep(10 * testPoll)
	if s := buf.String(); s != "" {
		t.Fatalf("caught-up reader should block (emit nothing); got:\n%s", s)
	}
	appendLine(t, path, `{"event":"snapshot","seq":1}`+"\n")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("followEvents: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("followEvents did not return after the append")
	}
	if !strings.Contains(buf.String(), `"seq":1`) {
		t.Errorf("expected appended seq 1 to be delivered; got:\n%s", buf.String())
	}
}

// TestFollowEvents_PartialTailWaitsForNewline: a line written without its
// trailing newline is not delivered until the newline arrives (no half-line to
// the agent).
func TestFollowEvents_PartialTailWaitsForNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeFile(t, path, `{"event":"ready","seq":0}`+"\n")

	var buf syncBuf
	done := make(chan error, 1)
	go func() { done <- followEvents(path, 0, &buf, testPoll) }() // caught up ⇒ block

	time.Sleep(10 * testPoll)
	appendLine(t, path, `{"event":"snapshot","seq":1}`) // no newline yet
	time.Sleep(10 * testPoll)
	if buf.String() != "" {
		t.Errorf("partial line (no newline) must not be delivered yet; got:\n%s", buf.String())
	}
	appendLine(t, path, "\n") // complete the line

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("followEvents: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("followEvents did not return once the line completed")
	}
	if !strings.Contains(buf.String(), `"seq":1`) {
		t.Errorf("completed line should be delivered; got:\n%s", buf.String())
	}
}

// TestFollowEvents_ResetGuard: a stale cursor (since>0) against a freshly-wiped
// log (ready@0) is dropped so the new run's events aren't filtered away.
func TestFollowEvents_ResetGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"event":"ready","seq":0}`,
		`{"event":"snapshot","seq":1}`,
		`{"event":"end","seq":2}`,
		"",
	}, "\n"))

	var buf syncBuf
	if err := followEvents(path, 5, &buf, testPoll); err != nil {
		t.Fatalf("followEvents: %v", err)
	}
	got := buf.String()
	// Reset detected on ready@0 → since dropped → the whole new run prints.
	if !strings.Contains(got, `"seq":0`) || !strings.Contains(got, `"seq":1`) || !strings.Contains(got, `"seq":2`) {
		t.Errorf("reset guard should print the whole new run; got:\n%s", got)
	}
}

// TestFollowEvents_WaitsForLogThenReads: an absent log is waited on (launch
// race), then read once it appears.
func TestFollowEvents_WaitsForLogThenReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")

	var buf syncBuf
	done := make(chan error, 1)
	go func() { done <- followEvents(path, -1, &buf, testPoll) }()

	time.Sleep(10 * testPoll) // reader is waiting for the file
	writeFile(t, path, strings.Join([]string{
		`{"event":"ready","seq":0}`,
		`{"event":"end","seq":1}`,
		"",
	}, "\n"))

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("followEvents: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("followEvents did not read the log once it appeared")
	}
	if !strings.Contains(buf.String(), `"seq":0`) {
		t.Errorf("expected events once the log appeared; got:\n%s", buf.String())
	}
}
