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
// holds AutoLearnMinSignals new reviewer signals, only one tab queues the request
// (a half-typed draft in it survives), and queueing resets the count.
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
	if c.maybeAutoLearn(); c.autoLearn.Load() != 0 {
		t.Fatal("asked with too few signals")
	}
	seed(AutoLearnMinSignals)
	c.maybeAutoLearn()
	if n := c.autoLearn.Load(); n != AutoLearnMinSignals {
		t.Fatalf("asked with %d signals, want %d", n, AutoLearnMinSignals)
	}

	base := PrereviewState{AgentMode: true, SelectedFile: "doc.md", Comments: c.loadCommentsFromDisk()}
	if got, _ := c.AutoLearn(PrereviewState{SelectedFile: "doc.md"}, checksCtx("AutoLearn", nil)); got.extractionOpen() || c.autoLearn.Load() == 0 {
		t.Fatal("a tab with no agent claimed the request")
	}
	composing := base
	composing.CommentMode, composing.DraftBody = "line", "half a thought"
	first, err := c.AutoLearn(composing, checksCtx("AutoLearn", nil))
	if err != nil || !first.extractionOpen() {
		t.Fatalf("the first idle tab didn't queue the request: %v", err)
	}
	if first.DraftBody != "half a thought" || first.CommentMode != "line" {
		t.Fatalf("queueing the request touched the composer: %q %q", first.CommentMode, first.DraftBody)
	}
	if disk := c.loadCommentsFromDisk(); disk[len(disk)-1].Request != csv.RequestExtraction {
		t.Fatalf("the extraction request isn't marked on disk: %+v", disk[len(disk)-1])
	}
	if second, _ := c.AutoLearn(base, checksCtx("AutoLearn", nil)); second.extractionOpen() {
		t.Fatal("a second tab queued it again")
	}
	if e := library.LastExtractions(c.LibraryDir)[store]; e.Signals != AutoLearnMinSignals {
		t.Fatalf("extraction recorded %d signals, want %d (the request itself isn't one)", e.Signals, AutoLearnMinSignals)
	}
	if c.maybeAutoLearn(); c.autoLearn.Load() != 0 {
		t.Fatal("asked again with no new signals")
	}

	seed(2 * AutoLearnMinSignals)
	saveUIPrefs(c.UIPrefsPath, UIPrefs{AutoLearnOff: true})
	if c.maybeAutoLearn(); c.autoLearn.Load() != 0 {
		t.Fatal("asked with Learn after each review off")
	}
}
