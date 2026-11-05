package review

// #213: a settled change to a reviewed file reaches the AGENT as one
// files_changed event, attributed via the #194 agent cursor. Driven through the
// real watcher (settleHarness) and asserted on the durable events.jsonl the
// agent's `watch` reads.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// filesChanged returns the files_changed events in the harness's event log.
func (h *settleHarness) filesChanged() []StreamEvent {
	h.t.Helper()
	b, err := os.ReadFile(EventsPath(h.c.CSVPath))
	if err != nil && !os.IsNotExist(err) {
		h.t.Fatal(err)
	}
	var out []StreamEvent
	for _, ev := range decodeEvents(h.t, b) {
		if ev.Event == "files_changed" {
			out = append(out, ev)
		}
	}
	return out
}

// watching stamps a blocking watch's "waiting" heartbeat, as `prereview watch` does.
func (h *settleHarness) watching() {
	h.t.Helper()
	err := WriteAgentCursor(filepath.Dir(h.c.CSVPath), AgentCursor{Mode: AgentCursorWatch, State: AgentCursorWaiting, DeliveredSeq: -1})
	if err != nil {
		h.t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond) // the watcher reads the cursor on its next tick
}

func (h *settleHarness) settled(gen int64) {
	h.t.Helper()
	// The settling flag clears AFTER the version and its event land (same tick).
	h.waitFor("the edits to settle", 2*time.Second, func() bool {
		return h.c.reviewedGen.Load() == gen && !h.c.editsSettling.Load()
	})
}

func TestFilesChangedWhileAgentWatches(t *testing.T) {
	h := newSettleHarness(t, 100*time.Millisecond)
	h.watching()
	for _, body := range []string{"v1 a", "v1 b", "v1 c"} { // a burst is one event
		h.edit(body)
		time.Sleep(30 * time.Millisecond)
	}
	h.settled(1)
	evs := h.filesChanged()
	if len(evs) != 1 {
		t.Fatalf("%d files_changed events for one burst, want 1: %+v", len(evs), evs)
	}
	want := StreamEvent{Event: "files_changed", Seq: evs[0].Seq, Ts: evs[0].Ts, Files: []string{"doc.md"}, Version: 1, Cause: FilesChangedEdit, AgentWatching: true}
	if !reflect.DeepEqual(evs[0], want) {
		t.Fatalf("event %+v, want %+v", evs[0], want)
	}
}

// No watch attached → the edit may be the agent's own; still reported, unattributed.
func TestFilesChangedWithoutWatchIsUnattributed(t *testing.T) {
	h := newSettleHarness(t, 100*time.Millisecond)
	h.edit("v1")
	h.settled(1)
	if evs := h.filesChanged(); len(evs) != 1 || evs[0].AgentWatching {
		t.Fatalf("want one event with agent_watching=false, got %+v", evs)
	}
}

// One edit while the watch was NOT attached taints the whole run.
func TestFilesChangedAttributionNeedsEveryEdit(t *testing.T) {
	h := newSettleHarness(t, 150*time.Millisecond)
	h.edit("v1 by the agent")
	time.Sleep(40 * time.Millisecond)
	h.watching()
	h.edit("v1 then the reviewer")
	h.settled(1)
	if evs := h.filesChanged(); len(evs) != 1 || evs[0].AgentWatching {
		t.Fatalf("want one event with agent_watching=false, got %+v", evs)
	}
}

// The agent's own done closes its batch silently; a no-op rewrite says nothing.
func TestFilesChangedSilentCases(t *testing.T) {
	h := newSettleHarness(t, 100*time.Millisecond)
	h.edit("v1 by the agent")
	h.waitFor("pending edits", time.Second, h.c.editsSettling.Load)
	h.status(`{"state":"done","message":"applied"}`)
	h.settled(1)

	h.edit("v2 temporary")
	h.waitFor("pending edits", time.Second, h.c.editsSettling.Load)
	h.edit("v1 by the agent") // back to the newest version before settling
	h.waitFor("the window to close", 2*time.Second, func() bool { return !h.c.editsSettling.Load() })
	if evs := h.filesChanged(); len(evs) != 0 {
		t.Fatalf("want no files_changed, got %+v", evs)
	}
}

// Nothing may follow `end`: a window still pending at EndSession stays silent.
func TestFilesChangedNeverAfterEnd(t *testing.T) {
	h := newSettleHarness(t, 100*time.Millisecond)
	h.edit("v1")
	h.waitFor("pending edits", time.Second, h.c.editsSettling.Load)
	h.c.emitDisabled.Store(true) // what EndSession / Quit set before emitting end
	h.settled(1)
	if evs := h.filesChanged(); len(evs) != 0 {
		t.Fatalf("files_changed after end: %+v", evs)
	}
}

// A file leaving the scope records no version, but the agent still hears of it
// (the same fail-toward-telling as the reviewer's nudge), with no version number.
func TestFilesChangedScopeShrink(t *testing.T) {
	h := newSettleHarness(t, 100*time.Millisecond, "notes.md")
	if err := os.Remove(filepath.Join(h.c.RepoPath, "notes.md")); err != nil {
		t.Fatal(err)
	}
	h.settled(1)
	evs := h.filesChanged()
	if len(evs) != 1 || fmt.Sprint(evs[0].Files) != "[notes.md]" || evs[0].Version != 0 {
		t.Fatalf("want one event for notes.md with no version, got %+v", evs)
	}
}

// Checkpoint reports what moved: edited, removed (tombstoned) and added files in
// a new version, and a file that left the scope even when no version is recorded.
func TestCheckpointChanged(t *testing.T) {
	h := newSettleHarness(t, time.Hour, "a.md", "b.md")
	repo := h.c.RepoPath
	ref := func(name string) FileRef { return FileRef{Path: name, AbsPath: filepath.Join(repo, name)} }
	if err := os.WriteFile(filepath.Join(repo, "a.md"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "b.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "c.md"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp, created, err := h.vs.Checkpoint([]FileRef{ref("a.md"), ref("b.md"), ref("c.md"), ref("doc.md")}, VersionTriggerFileEdit, "")
	if err != nil || !created || !reflect.DeepEqual(cp.Changed, []string{"a.md", "b.md", "c.md"}) {
		t.Fatalf("edit+remove+add: changed=%v created=%v err=%v, want [a.md b.md c.md]", cp.Changed, created, err)
	}
	cp, created, err = h.vs.Checkpoint([]FileRef{ref("a.md"), ref("doc.md")}, VersionTriggerFileEdit, "")
	if err != nil || created || !reflect.DeepEqual(cp.Changed, []string{"b.md", "c.md"}) {
		t.Fatalf("scope shrink: changed=%v created=%v err=%v, want [b.md c.md] with no version", cp.Changed, created, err)
	}
}
