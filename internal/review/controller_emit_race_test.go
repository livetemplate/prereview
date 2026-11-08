package review

import (
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
)

// #228: a save that lands while a snapshot emit is in flight. These tests hold a REAL
// emitSnapshot open through emitSnapshotHook — toggling inEmit by hand would pass against
// both the bug and a broken fix. None of them may run in parallel: the hook is a global.

// holdEmit installs a hook that blocks c's FIRST emit right after it has read the CSV,
// runs during() there, and lets later emits through untouched.
func holdEmit(t *testing.T, c *PrereviewController, during func()) {
	t.Helper()
	var once sync.Once
	emitSnapshotHook = func() { once.Do(during) }
	t.Cleanup(func() { emitSnapshotHook = nil })
	quiesce(t, c) // registered last, so it runs first: no emit reads the hook once reset
}

// eventually polls cond until it holds or the deadline passes. A deadline, not a sleep:
// the debounce is timer-driven and a loaded CI box runs late (emit-debounce-ci-flake).
func eventually(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// quiesce stops c's emit engine and waits for an in-flight emit to finish, at cleanup.
// Registered after fastDebounce/holdEmit, so it runs BEFORE they reset the globals a
// trailing emit still reads (cleanups run last-in, first-out).
func quiesce(t *testing.T, c *PrereviewController) {
	t.Cleanup(func() {
		c.emitDisabled.Store(true)
		c.stopPendingEmit()
		eventually(t, func() bool {
			c.emitMu.Lock()
			defer c.emitMu.Unlock()
			return !c.inEmit
		})
	})
}

// newRepoStreamController is newStreamController in repo mode (not external), so a comment
// on a file that is not on disk is "gone" and relocateAll self-heals it to outdated — the
// cheapest way to make the emit WRITE, with no git fixture.
func newRepoStreamController(t *testing.T) (*PrereviewController, *syncBuffer) {
	t.Helper()
	c, buf, _ := newStreamController(t)
	c.ExternalMode = false
	c.RepoPath = filepath.Dir(c.CSVPath)
	quiesce(t, c)
	return c, buf
}

func goneFileComment(id string) Comment {
	return Comment{ID: id, File: "gone.md", FromLine: 1, ToLine: 1, Side: "new", Body: "on a deleted file", Created: fixedTS}
}

func readRows(t *testing.T, c *PrereviewController) map[string]csv.Row {
	t.Helper()
	rows, err := csv.Read(c.CSVPath)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]csv.Row{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

func snapshotHas(t *testing.T, buf *syncBuffer, id string) bool {
	for _, ev := range decodeEvents(t, buf.Bytes()) {
		if slices.Contains(ids(ev.CommentList()), id) {
			return true
		}
	}
	return false
}

// D1: the save mid-emit must reach the agent without any further mutation.
func TestEmit_SaveDuringEmitStillEmits(t *testing.T) {
	fastDebounce(t)
	c, buf, _ := newStreamController(t)
	holdEmit(t, c, func() {
		if err := c.persist([]Comment{regionComment("a", "one"), regionComment("b", "two")}); err != nil {
			t.Error(err)
		}
	})
	if err := c.persist([]Comment{regionComment("a", "one")}); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, func() bool { return snapshotHas(t, buf, "b") }) {
		t.Fatalf("comment b was saved during an in-flight emit and never reached a snapshot: %d events",
			len(decodeEvents(t, buf.Bytes())))
	}
}

// D2: the emit's self-heal must not delete a comment saved after it read the CSV.
func TestEmit_SelfHealDoesNotDeleteConcurrentSave(t *testing.T) {
	fastDebounce(t)
	c, buf := newRepoStreamController(t)
	holdEmit(t, c, func() {
		if err := c.persist([]Comment{goneFileComment("a"), regionComment("b", "saved mid-emit")}); err != nil {
			t.Error(err)
		}
	})
	if err := c.persist([]Comment{goneFileComment("a")}); err != nil {
		t.Fatal(err)
	}
	ok := eventually(t, func() bool {
		rows := readRows(t, c)
		_, hasB := rows["b"]
		return hasB && rows["a"].AnchorStatus == anchorOutdated && snapshotHas(t, buf, "b")
	})
	if !ok {
		t.Fatalf("after the self-heal, comments.csv = %+v; want b kept and a outdated", readRows(t, c))
	}
}

// D2's compare-and-set: a row the reviewer re-anchored mid-emit keeps the reviewer's
// version. Pausing in the hook stops the follow-up emit, so only the in-flight one writes.
func TestEmit_SelfHealSkipsRowEditedMidEmit(t *testing.T) {
	fastDebounce(t)
	c, buf := newRepoStreamController(t)
	holdEmit(t, c, func() {
		edited := goneFileComment("a")
		edited.FromLine, edited.ToLine, edited.Body = 7, 9, "re-anchored by the reviewer"
		if err := c.persist([]Comment{edited}); err != nil {
			t.Error(err)
		}
		c.setAgentPaused(true)
	})
	if err := c.persist([]Comment{goneFileComment("a")}); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, func() bool { return len(decodeEvents(t, buf.Bytes())) > 0 }) {
		t.Fatal("the in-flight emit never finished")
	}
	a := readRows(t, c)["a"]
	if a.FromLine != 7 || a.ToLine != 9 || a.AnchorStatus == anchorOutdated {
		t.Fatalf("the emit overwrote a row edited after it read it: %+v", a)
	}
}

// The self-heal write must not re-arm an emit of its own: a drifting comment settles in
// one snapshot (a regression guard: persistAnchors must not schedule an emit).
func TestEmit_NoEmitSpin(t *testing.T) {
	fastDebounce(t)
	c, buf := newRepoStreamController(t)
	if err := c.persist([]Comment{goneFileComment("a"), regionComment("b", "two")}); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, func() bool { return readRows(t, c)["a"].AnchorStatus == anchorOutdated }) {
		t.Fatal("the self-heal never ran")
	}
	time.Sleep(20 * emitDebounce)
	if n := len(decodeEvents(t, buf.Bytes())); n != 1 {
		t.Fatalf("a self-healing emit produced %d snapshots, want 1 (it re-armed itself)", n)
	}
}

// A save mid-emit followed by End session: nothing may follow `end`, neither the pending
// re-arm nor the in-flight emit finishing late.
func TestEmit_NoSnapshotAfterEndWithEmitInFlight(t *testing.T) {
	fastDebounce(t)
	c, buf, _ := newStreamController(t)
	holdEmit(t, c, func() {
		if err := c.persist([]Comment{regionComment("a", "one"), regionComment("b", "two")}); err != nil {
			t.Error(err)
		}
		if _, err := c.EndSession(PrereviewState{Comments: []Comment{regionComment("a", "one"), regionComment("b", "two")}}, regionCtx("endSession", nil)); err != nil {
			t.Error(err)
		}
	})
	if err := c.persist([]Comment{regionComment("a", "one")}); err != nil {
		t.Fatal(err)
	}
	if !eventually(t, func() bool {
		for _, ev := range decodeEvents(t, buf.Bytes()) {
			if ev.Event == "end" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("no end event")
	}
	time.Sleep(20 * emitDebounce)
	evs := decodeEvents(t, buf.Bytes())
	if last := evs[len(evs)-1]; last.Event != "end" {
		t.Fatalf("a %q event followed end: %d events", last.Event, len(evs))
	}
}
