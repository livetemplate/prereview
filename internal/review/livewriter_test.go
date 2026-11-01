package review

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// blockingWriter never returns from Write until release is closed — an
// undrained stdout pipe whose kernel buffer is full.
type blockingWriter struct{ release chan struct{} }

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	return len(p), nil
}

// syncBuffer is a goroutine-safe bytes.Buffer, for sinks written from another
// goroutine (the LossyWriter drain, the debounced emit timer).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string { return string(s.Bytes()) }

// Bytes returns a copy, so a reader never aliases memory a writer is appending to.
func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.b.Bytes())
}

type failWriter struct {
	mu sync.Mutex
	n  int
}

func (f *failWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return 0, errors.New("write /dev/stdout: broken pipe")
}

func (f *failWriter) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// returnsWithin fails the test if fn has not returned within d. The bound is
// deliberately loose (seconds, not ms): the property is "does not block
// forever", and a tight bound flakes on a loaded CI box.
func returnsWithin(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s blocked for more than %v", what, d)
	}
}

func TestLossyWriter_WedgedSinkNeverBlocksWrite(t *testing.T) {
	sink := &blockingWriter{release: make(chan struct{})}
	defer close(sink.release)
	l := NewLossyWriter(sink, 2)

	// Far more writes than the queue holds: the first is stuck in the sink, two
	// fill the queue, the rest must be dropped — never blocked on.
	returnsWithin(t, 2*time.Second, "Write against a wedged sink", func() {
		for range 50 {
			if n, err := l.Write([]byte("line\n")); err != nil || n != 5 {
				t.Errorf("Write = %d, %v; want 5, nil", n, err)
			}
		}
	})
	if l.dropped.Load() == 0 {
		t.Error("expected drops once the queue filled")
	}
	returnsWithin(t, 2*time.Second, "Close against a wedged sink", func() { l.Close(100 * time.Millisecond) })
}

func TestLossyWriter_CloseFlushesQueued(t *testing.T) {
	var out syncBuffer
	l := NewLossyWriter(&out, 64)
	for _, s := range []string{"a\n", "b\n", "end\n"} {
		_, _ = l.Write([]byte(s))
	}
	l.Close(2 * time.Second)
	if got := out.String(); got != "a\nb\nend\n" {
		t.Fatalf("flushed = %q, want every queued line in order", got)
	}
	// Writes after Close are dropped, not delivered or panicking.
	_, _ = l.Write([]byte("late\n"))
	if got := out.String(); got != "a\nb\nend\n" {
		t.Fatalf("write after Close was delivered: %q", got)
	}
}

func TestLossyWriter_DeadAfterSinkError(t *testing.T) {
	sink := &failWriter{}
	l := NewLossyWriter(sink, 64)
	for range 5 {
		if _, err := l.Write([]byte("x\n")); err != nil {
			t.Fatalf("Write surfaced the sink error: %v", err)
		}
	}
	l.Close(2 * time.Second)
	if c := sink.calls(); c != 1 {
		t.Fatalf("sink written %d times after EPIPE, want 1 (channel goes dead)", c)
	}
}

// TestEventStream_DurableWhenLiveWedged is the #215 property end to end at the
// emitter: with the live channel wedged, every event still lands in the durable
// mirror (what `prereview watch` reads), including the final `end`.
func TestEventStream_DurableWhenLiveWedged(t *testing.T) {
	sink := &blockingWriter{release: make(chan struct{})}
	defer close(sink.release)
	live := NewLossyWriter(sink, 1)
	file := filepath.Join(t.TempDir(), "events.jsonl")
	es := NewEventStream(live, file)

	returnsWithin(t, 2*time.Second, "emitting against a wedged stdout", func() {
		for range 20 {
			if err := es.EmitSnapshot(nil, nil, nil, nil, nil, nil, false, fixedTS); err != nil {
				t.Errorf("EmitSnapshot: %v", err)
			}
		}
		if err := es.EmitEnd(fixedTS); err != nil {
			t.Errorf("EmitEnd: %v", err)
		}
	})
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	evs := decodeEvents(t, b)
	if len(evs) != 21 || evs[20].Event != "end" {
		t.Fatalf("durable mirror has %d events (last %q), want 21 ending in end", len(evs), evs[len(evs)-1].Event)
	}
}

// TestEventStream_DurableBeforeLiveError: a failing live channel (unwrapped)
// still reports its error — but only after the durable line has landed.
func TestEventStream_DurableBeforeLiveError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "events.jsonl")
	es := NewEventStream(&failWriter{}, file)
	if err := es.EmitEnd(fixedTS); err == nil {
		t.Fatal("want the live write error reported")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if evs := decodeEvents(t, b); len(evs) != 1 || evs[0].Event != "end" {
		t.Fatalf("durable mirror = %q, want the end event despite the live failure", b)
	}
}
