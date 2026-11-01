package review

import (
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// LossyWriter is an io.Writer that never blocks its caller (#215). Write copies
// the bytes onto a bounded queue and returns at once; a single goroutine drains
// the queue into the wrapped writer. When the queue is full the bytes are
// dropped, and once the wrapped writer fails (e.g. EPIPE on a closed stdout)
// everything after is dropped too.
//
// It wraps the --agent live channel (stdout), which the agent may never drain:
// events.jsonl is what `prereview watch` reads, and snapshots are full, so a
// dropped line on the courtesy mirror is superseded by the next one.
//
// Nothing here logs synchronously from Write or Close: the caller holds a lock
// (EventStream.emit holds e.mu), and stderr may be the same undrained pipe.
type LossyWriter struct {
	w       io.Writer
	q       chan []byte
	closing chan struct{}
	done    chan struct{}

	closeOnce sync.Once
	dead      atomic.Bool
	stalled   atomic.Bool  // inside a drop episode (queue was full)
	dropped   atomic.Int64 // lines dropped because the queue was full
}

// NewLossyWriter wraps w with a queue of depth lines and starts its drain.
func NewLossyWriter(w io.Writer, depth int) *LossyWriter {
	l := &LossyWriter{
		w:       w,
		q:       make(chan []byte, depth),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
	go l.drain()
	return l
}

// Write queues a copy of p and always reports success: losing a line is the
// documented contract, not an error the caller can act on.
func (l *LossyWriter) Write(p []byte) (int, error) {
	select {
	case <-l.closing:
		return len(p), nil
	default:
	}
	if l.dead.Load() {
		return len(p), nil
	}
	select {
	case l.q <- append([]byte(nil), p...):
	default:
		l.dropped.Add(1)
		if l.stalled.CompareAndSwap(false, true) {
			// The drain is stuck in the sink's Write, so it can't report; a
			// one-shot goroutine keeps the warning off the caller's lock.
			go slog.Warn("live event channel full; dropping stdout events (events.jsonl unaffected)")
		}
	}
	return len(p), nil
}

// Close stops accepting writes, flushes what is queued, and waits at most
// timeout for the flush — so queued lines (e.g. a final `end`) reach a reader
// that IS draining, while a wedged sink can't hang shutdown. Safe to call more
// than once.
func (l *LossyWriter) Close(timeout time.Duration) {
	l.closeOnce.Do(func() { close(l.closing) })
	select {
	case <-l.done:
	case <-time.After(timeout):
		go slog.Warn("live event channel: flush timed out; stdout is not being read")
	}
}

func (l *LossyWriter) drain() {
	defer close(l.done)
	for {
		select {
		case b := <-l.q:
			l.write(b)
		case <-l.closing:
			for len(l.q) > 0 {
				l.write(<-l.q)
			}
			return
		}
	}
}

func (l *LossyWriter) write(b []byte) {
	if l.dead.Load() {
		return
	}
	if _, err := l.w.Write(b); err != nil {
		l.dead.Store(true)
		slog.Warn("live event channel closed; events continue in events.jsonl only", "err", err)
		return
	}
	if l.stalled.Swap(false) {
		slog.Info("live event channel recovered", "dropped_total", l.dropped.Load())
	}
}
