package review

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/library"
)

// TestAutoLearn: an idle agent is asked to learn from this review only once it
// holds AutoLearnMinSignals new reviewer signals, only one tab queues the request,
// never a tab with a comment being written, and queueing resets the count.
func TestAutoLearn(t *testing.T) {
	c := checksController(t)
	c.LibraryDir = t.TempDir()
	store := filepath.Dir(c.CSVPath)
	seed := func(n int) {
		var rows []csv.Row
		for i := 0; i < n; i++ {
			rows = append(rows, csv.Row{ID: fmt.Sprint("c", i), File: "doc.md", FromLine: 1, ToLine: 1, Body: "tighten", CreatedAt: time.Now()})
		}
		if err := c.CSVWriter.Write(rows); err != nil {
			t.Fatal(err)
		}
	}
	seed(AutoLearnMinSignals - 1)
	if c.maybeAutoLearn(); c.autoLearn.Load() != nil {
		t.Fatal("asked with too few signals")
	}
	seed(AutoLearnMinSignals)
	c.maybeAutoLearn()
	if p := c.autoLearn.Load(); p == nil || *p != store {
		t.Fatalf("not asked with %d signals", AutoLearnMinSignals)
	}

	base := PrereviewState{AgentMode: true, SelectedFile: "doc.md", Comments: c.loadCommentsFromDisk()}
	composing := base
	composing.DraftBody = "half a thought"
	if got, _ := c.AutoLearn(composing, checksCtx("AutoLearn", nil)); got.extractionOpen() || c.autoLearn.Load() == nil {
		t.Fatal("a tab with a comment being written claimed the request (queueing resets its composer)")
	}
	first, err := c.AutoLearn(base, checksCtx("AutoLearn", nil))
	if err != nil || !first.extractionOpen() {
		t.Fatalf("the first idle tab didn't queue the request: %v", err)
	}
	if second, _ := c.AutoLearn(base, checksCtx("AutoLearn", nil)); second.extractionOpen() {
		t.Fatal("a second tab queued it again")
	}
	if e := library.LastExtractions(c.LibraryDir)[store]; e.Signals != AutoLearnMinSignals {
		t.Fatalf("extraction recorded %d signals, want %d (the request itself isn't one)", e.Signals, AutoLearnMinSignals)
	}
	if c.maybeAutoLearn(); c.autoLearn.Load() != nil {
		t.Fatal("asked again with no new signals")
	}

	seed(2 * AutoLearnMinSignals)
	saveUIPrefs(c.UIPrefsPath, UIPrefs{AutoLearnOff: true})
	if c.maybeAutoLearn(); c.autoLearn.Load() != nil {
		t.Fatal("asked with Learn after each review off")
	}
}
