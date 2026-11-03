package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/livetemplate/prereview/internal/review"
)

// eventsPollInterval is how often the follow loop re-checks the event log for
// appended lines once it has caught up. The review is human-driven, so a
// sub-second poll is imperceptible and needs no fsnotify dependency (mirrors the
// server's WatchLLMStatus poll idiom).
const eventsPollInterval = 250 * time.Millisecond

// runWatch implements `prereview watch [--out <dir>] [--since <seq>]` — the ONE
// supported way for the coding agent to consume the review event stream. It
// reads the durable, append-only <store>/.prereview/events.jsonl (the same log
// the server writes in --agent mode), so it never drops events the way a bare
// `tail -f` does: the log is durable and each line carries a monotonic `seq`, so
// the agent resumes past anything that landed while it was away by re-running
// with the seq of the last line it saw.
//
// It MERGES catch-up and follow into one behaviour (there is no separate
// --follow): it prints every event whose seq is greater than --since, then
// blocks and streams new events as they are appended, exiting 0 only when it
// sees `end`. Run it with a long Bash timeout; on timeout, re-run with
// the last seq to pick up seamlessly.
func runWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	out := fs.String("out", "", "this review's store: the STORE path printed at launch (the reviewed file, or a directory whose .prereview/ holds the review, also work); defaults to the current directory")
	since := fs.Int("since", -1, "print only events whose seq is greater than this cursor (-1 = from the start); resume by passing the seq of the last line you saw")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview watch [--out <dir>] [--since <seq>]\n\n"+
				"  Deliver the next batch of review events (one JSON event per line). Prints\n"+
				"  every event after --since; if none are waiting, blocks for the next, then\n"+
				"  returns — exiting immediately on the terminating `end`. Each line\n"+
				"  carries a `seq`; loop by re-running with the highest seq you saw (anything\n"+
				"  that landed between rounds comes back instantly). Requires the review\n"+
				"  server running with --agent; --out must match its STORE directory.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, review.EventsFileName)
	return followEvents(path, *since, os.Stdout, eventsPollInterval, cursorStamper(dir))
}

// cursorStamper records this watch in the store's agent-cursor.json (#194), the
// signal the review UI reads to show that the queue is actually being consumed.
// The indicator is an enrichment and event delivery is the contract, so a failed
// stamp is reported once on stderr and never fails the command.
func cursorStamper(dir string) func(state string, seq int) {
	warned := false
	return func(state string, seq int) {
		err := review.WriteAgentCursor(dir, review.AgentCursor{Mode: review.AgentCursorWatch, State: state, DeliveredSeq: seq})
		if err != nil && !warned {
			fmt.Fprintf(os.Stderr, "prereview watch: could not record the agent cursor (the review UI will not show this watch): %v\n", err)
			warned = true
		}
	}
}

// followEvents delivers ONE batch of events from path to w and returns — the
// agent-friendly "merge of since and follow". It first drains everything already
// on disk past the cursor (seq > since); if that yielded anything, it returns so
// the agent can act on the batch. Only when already caught up does it block,
// polling until the next event(s) are appended, then emits them and returns. It
// always returns as soon as it sees `end` (the stream's terminator). The
// agent loops: run, act on the latest snapshot, re-run with the highest seq it
// saw — so anything that lands between rounds is caught instantly on the next run
// (the durable log + seq cursor is what closes the missed-events window a bare
// `tail -f` leaves open).
//
// It tolerates a not-yet-created log (waits for it), torn/partial trailing lines
// (skipped / awaited), and a per-launch reset: the server wipes events.jsonl
// each run, restarting seq at 0, so a stale cursor would silently filter the
// whole new run away — detected as "the log's highest seq is below the cursor"
// (impossible in a monotonic same-session log) and handled by dropping the
// cursor. poll is the block-for-next re-check interval.
//
// stamp (nil = none) records the reader in agent-cursor.json: "waiting" with the
// cursor once the log is open and every AgentCursorHeartbeat while blocked, then
// "delivered" with the highest seq printed. Never before waitOpen succeeds — a
// watch stuck on a missing log (a wrong --out) must not heartbeat as attached.
func followEvents(path string, since int, w io.Writer, poll time.Duration, stamp func(state string, seq int)) error {
	f, err := waitOpen(path, poll)
	if err != nil {
		return err
	}
	defer f.Close()
	if stamp == nil {
		stamp = func(string, int) {}
	}

	r := bufio.NewReader(f)

	// Catch-up. Buffer every complete line already on disk so we can compute the
	// max seq BEFORE filtering (reset detection needs it).
	catchup, partial := readComplete(r, nil)
	maxSeq := -1
	for _, ln := range catchup {
		if ev, ok := peekEvent(ln); ok && ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}
	if maxSeq >= 0 && maxSeq < since {
		fmt.Fprintln(os.Stderr, "prereview watch: event log reset (new session) — resuming from seq 0")
		since = -1
	}
	// Deliver the catch-up batch; when already caught up, block — heartbeating
	// "waiting" — until the next event(s) land, then deliver those and return.
	lines := catchup
	var beat time.Time
	for {
		last, done, err := emitBatch(lines, since, w)
		if err == nil && last > since { // a failed print is no delivery; nor is a bare end past the cursor
			stamp(review.AgentCursorDelivered, last)
		}
		if err != nil || done || last > since {
			return err
		}
		if time.Since(beat) >= review.AgentCursorHeartbeat {
			stamp(review.AgentCursorWaiting, since)
			beat = time.Now()
		}
		time.Sleep(poll)
		lines, partial = readComplete(r, partial)
	}
}

// emitBatch writes each line whose seq exceeds the cursor, returning the highest
// seq it wrote (since itself when it wrote none) and whether a `end` terminator
// was seen. Torn/malformed lines are skipped.
func emitBatch(lines [][]byte, since int, w io.Writer) (last int, done bool, err error) {
	last = since
	for _, ln := range lines {
		seq, term, err := emitEvent(ln, since, w)
		if err != nil {
			return last, false, err
		}
		last = max(last, seq)
		if term {
			return last, true, nil
		}
	}
	return last, false, nil
}

// readComplete drains r up to EOF, returning every newline-terminated line plus
// any trailing partial (no newline yet) so the caller can resume it on the next
// append. `carry` seeds the partial from a prior call.
func readComplete(r *bufio.Reader, carry []byte) (lines [][]byte, partial []byte) {
	partial = carry
	for {
		chunk, err := r.ReadBytes('\n')
		if len(chunk) > 0 {
			partial = append(partial, chunk...)
			if partial[len(partial)-1] == '\n' {
				lines = append(lines, partial)
				partial = nil
			}
		}
		if err != nil { // io.EOF (caught up) or a read error — either way, stop
			return lines, partial
		}
	}
}

// emitEvent prints line when its seq exceeds the cursor (returning that seq, else
// since-or-below), and
// reports term when the line is a `end` — the stream's only terminator —
// even if it was filtered out (a cursor already past it means the agent has seen
// it), so we never block waiting for events that will never come. Torn/malformed
// lines are silently skipped.
func emitEvent(line []byte, since int, w io.Writer) (seq int, term bool, err error) {
	ev, ok := peekEvent(line)
	if !ok || ev.Seq <= since {
		return since, ok && ev.Event == "end", nil
	}
	if _, err := w.Write(line); err != nil {
		return since, false, fmt.Errorf("write event: %w", err)
	}
	return ev.Seq, ev.Event == "end", nil
}

// peekEvent decodes only the routing fields (event + seq) from a log line. ok is
// false for a torn/malformed line or one missing the `event` field, so such
// lines are neither printed nor mistaken for a terminator.
func peekEvent(line []byte) (ev review.StreamEvent, ok bool) {
	if err := json.Unmarshal(line, &ev); err != nil || ev.Event == "" {
		return ev, false
	}
	return ev, true
}

// waitOpen opens path, waiting for it to appear if the review server hasn't
// created it yet (a launch race). It prints a one-time hint naming the path so a
// wrong --out surfaces instead of hanging silently. The caller's Bash timeout
// bounds the wait.
func waitOpen(path string, poll time.Duration) (*os.File, error) {
	warned := false
	for {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		if !warned {
			fmt.Fprintf(os.Stderr, "prereview watch: waiting for %s — is the review running with --agent (and --out correct)?\n", path)
			warned = true
		}
		time.Sleep(poll)
	}
}
