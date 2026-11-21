package review

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/prereview/internal/checks"
)

// veryLines is n lines that each trip filler-intensifiers once ("very").
func veryLines(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "Line %d is very good.\n", i+1)
	}
	return b.String()
}

// TestQueueAllFindings: Queue all queues every finding the row counts, across
// files and past the list's cap, in one write; it skips what's already queued,
// and a search narrows it like the row.
func TestQueueAllFindings(t *testing.T) {
	c := checksController(t)
	c.NoGit = true
	writeRepoFile(t, c, "a.md", veryLines(maxPageFindings))
	writeRepoFile(t, c, "b.md", "Plain.\nThis is very nice.\nIt is really fine.\n")
	s := reviewState("a.md", "b.md")
	s.ExpandedCheck = "filler-intensifiers"
	c.applyReviewChecks(&s)
	want := maxPageFindings + 2 // a.md's, and b.md's "very" and "really"
	row := pageRow(t, s.CheckPage(), "filler-intensifiers")
	if row.More == 0 || row.QueueAll != want || row.IgnoreAll != want {
		t.Fatalf("row: More %d QueueAll %d Ignore %d, want the cap passed and %d each", row.More, row.QueueAll, row.IgnoreAll, want)
	}

	s, err := c.QueueFinding(s, checksCtx("queueFinding", map[string]interface{}{"path": "b.md", "id": "filler-intensifiers", "index": 0}))
	if err != nil {
		t.Fatal(err)
	}
	s, err = c.QueueAllFindings(s, checksCtx("queueAllFindings", map[string]interface{}{"id": "filler-intensifiers"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Comments) != want {
		t.Fatalf("%d comments, want %d (one queued before, the rest by Queue all)", len(s.Comments), want)
	}
	if got := len(c.loadCommentsFromDisk()); got != want {
		t.Errorf("%d comments on disk, want %d", got, want)
	}
	if row := pageRow(t, s.CheckPage(), "filler-intensifiers"); row.QueueAll != 0 || row.IgnoreAll != want {
		t.Errorf("after Queue all: QueueAll %d Ignore %d, want 0 and %d", row.QueueAll, row.IgnoreAll, want)
	}
	if !strings.HasPrefix(s.Flash, fmt.Sprintf("Queued %d", want-1)) {
		t.Errorf("flash %q", s.Flash)
	}

	// Under a search, the row and the button count only the matches.
	s.ChecksQuery = "really"
	if row := pageRow(t, s.CheckPage(), "filler-intensifiers"); row.IgnoreAll != 1 {
		t.Errorf("search 'really': Ignore %d, want 1", row.IgnoreAll)
	}
}

// TestDismissAllFindings_Checker: Ignore all drops every counted finding, the
// row passes, and Show again (no path: every file) brings them back.
func TestDismissAllFindings_Checker(t *testing.T) {
	c := checksController(t)
	c.NoGit = true
	writeRepoFile(t, c, "a.md", veryLines(3))
	writeRepoFile(t, c, "b.md", "This is very nice.\n")
	s := reviewState("a.md", "b.md")
	s.ExpandedCheck = "filler-intensifiers"
	c.applyReviewChecks(&s)
	s, err := c.DismissAllFindings(s, checksCtx("dismissAllFindings", map[string]interface{}{"id": "filler-intensifiers"}))
	if err != nil {
		t.Fatal(err)
	}
	s.ShowPassingGroups = true
	if row := pageRow(t, s.CheckPage(), "filler-intensifiers"); row.Status != checks.StatusPass || row.IgnoreAll != 0 {
		t.Errorf("after Ignore all: status %s Ignore %d", row.Status, row.IgnoreAll)
	}
	if s.Flash != "Ignored 4 findings" {
		t.Errorf("flash %q", s.Flash)
	}
	s, err = c.RestoreDismissed(s, checksCtx("restoreDismissed", map[string]interface{}{"id": "filler-intensifiers"}))
	if err != nil {
		t.Fatal(err)
	}
	if row := pageRow(t, s.CheckPage(), "filler-intensifiers"); row.Status != checks.StatusFail || row.IgnoreAll != 4 {
		t.Errorf("after Show again: status %s Ignore %d", row.Status, row.IgnoreAll)
	}
}

// TestDismissAllFindings_Judge: an ignored judge failure reads "ignored", stops
// counting as failing everywhere, survives a re-run on the same text, and fails
// again when the text changes. Show again brings it back.
func TestDismissAllFindings_Judge(t *testing.T) {
	c := judgeController(t)
	s := judgedState(t, c, "fail", "very good", nil)
	s.ShowLibrary, s.LibraryTab, s.ExpandedCheck = true, libTabChecks, "reader-first"
	if row := pageRow(t, s.CheckPage(), "reader-first"); row.Status != checks.StatusFail || row.QueueAll != 1 || row.IgnoreAll != 1 {
		t.Fatalf("before: %+v", row)
	}
	s, err := c.DismissAllFindings(s, checksCtx("dismissAllFindings", map[string]interface{}{"id": "reader-first"}))
	if err != nil {
		t.Fatal(err)
	}
	s.ShowPassingGroups = true
	row := pageRow(t, s.CheckPage(), "reader-first")
	if row.Status != checks.StatusPass || row.Summary != "1 ignored" || row.Ignored != 1 || row.IgnoreAll != 0 || len(row.Verdicts) != 1 || row.Verdicts[0].Status != verdictIgnored {
		t.Errorf("after Ignore all: %+v", row)
	}
	for _, p := range s.CheckStrip().Failing { // the file's built-in checker still fails
		if p.ID == "reader-first" {
			t.Errorf("the strip still lists the judge: %+v", p)
		}
	}
	if s.Flash != "Ignored 1 verdict" {
		t.Errorf("flash %q", s.Flash)
	}

	// A re-run on the same text keeps it ignored.
	s = judgedState(t, c, "fail", "very good", func(j *Judgment) {
		j.Critique, j.At = "Still buries it.", time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
	})
	if r, _ := s.checkResult("reader-first"); r.Status != checks.StatusPass || r.Dismissed != 1 {
		t.Errorf("re-run on unchanged text: %+v", r)
	}
	// A verdict on changed text (or a changed rubric) isn't the one ignored.
	j := c.verdicts()[judgmentKey("doc.md", "reader-first")]
	j.ContentHash = "changed"
	if ignoredVerdict(c.dismissals(), "doc.md", "reader-first", j) {
		t.Error("a verdict on changed text is still ignored")
	}

	s, err = c.RestoreDismissed(s, checksCtx("restoreDismissed", map[string]interface{}{"id": "reader-first"}))
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s.checkResult("reader-first"); r.Status != checks.StatusFail {
		t.Errorf("after Show again: %+v", r)
	}
}

// TestQueueAllFindings_JudgeSkipsUnquoted: a judge failure whose quote wasn't
// found has no span, so Queue all leaves it (Ignore all still counts it).
func TestQueueAllFindings_JudgeSkipsUnquoted(t *testing.T) {
	c := judgeController(t)
	s := judgedState(t, c, "fail", "", nil)
	s.ShowLibrary, s.LibraryTab, s.ExpandedCheck = true, libTabChecks, "reader-first"
	if row := pageRow(t, s.CheckPage(), "reader-first"); row.QueueAll != 0 || row.IgnoreAll != 1 {
		t.Errorf("unquoted failure: QueueAll %d Ignore %d, want 0 and 1", row.QueueAll, row.IgnoreAll)
	}
	s, err := c.QueueAllFindings(s, checksCtx("queueAllFindings", map[string]interface{}{"id": "reader-first"}))
	if err != nil || len(s.Comments) != 0 {
		t.Errorf("Queue all queued %d (err %v), want none", len(s.Comments), err)
	}
}

// TestResolveAll: Resolve all resolves only the listed, unresolved comments in
// one write; Undo reopens exactly those, and closing the screen drops the Undo.
func TestResolveAll(t *testing.T) {
	c := checksController(t)
	s := PrereviewState{ShowAllComments: true, Comments: []Comment{
		{ID: "a", File: "x.md", Body: "one", Kind: "line", FromLine: 1, ToLine: 1, Side: "new"},
		{ID: "b", File: "x.md", Body: "two", Kind: "line", FromLine: 2, ToLine: 2, Side: "new"},
		{ID: "r", File: "x.md", Body: "done", Kind: "line", FromLine: 3, ToLine: 3, Side: "new", Resolved: true},
		{ID: "o", File: "y.md", Body: "other file", Kind: "line", FromLine: 1, ToLine: 1, Side: "new"},
	}}
	s.SingleFile = "x.md" // y.md is out of scope, so not listed
	if n := s.UnresolvedListed(); n != 2 {
		t.Fatalf("UnresolvedListed = %d, want 2", n)
	}
	s, err := c.ResolveAll(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved := func(cms []Comment) string {
		var out []string
		for _, cm := range cms {
			if cm.Resolved {
				out = append(out, cm.ID)
			}
		}
		return strings.Join(out, ",")
	}
	if got := resolved(s.Comments); got != "a,b,r" || strings.Join(s.LastResolvedIDs, ",") != "a,b" {
		t.Fatalf("resolved %s, undo %v", got, s.LastResolvedIDs)
	}
	if s.UnresolvedListed() != 0 {
		t.Error("Resolve all still offered")
	}
	s, err = c.UndoResolveAll(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved(s.Comments); got != "r" || s.LastResolvedIDs != nil {
		t.Errorf("after Undo: resolved %s, undo %v", got, s.LastResolvedIDs)
	}
	if got := resolved(c.loadCommentsFromDisk()); got != "r" {
		t.Errorf("on disk resolved %s", got)
	}

	s, _ = c.ResolveAll(s, nil)
	s, _ = c.ToggleCommentList(s, nil)
	if s.LastResolvedIDs != nil {
		t.Error("closing All comments keeps the Undo")
	}
}

// TestBulkActions_PageWide: with no check id, Queue all / Ignore all act on
// every check the page lists, so the page's filters narrow them: here the kind
// filter leaves only the judge.
func TestBulkActions_PageWide(t *testing.T) {
	c := judgeController(t)
	s := judgedState(t, c, "fail", "very good", nil) // the judge and filler-intensifiers both fail doc.md
	s.ShowLibrary, s.LibraryTab = true, libTabChecks
	if p := s.CheckPage(); p.QueueAll != 2 || p.IgnoreAll != 2 {
		t.Fatalf("page-wide QueueAll %d IgnoreAll %d, want 2 and 2", p.QueueAll, p.IgnoreAll)
	}
	s.ChecksKind = checks.TypeJudge
	if p := s.CheckPage(); p.QueueAll != 1 || p.IgnoreAll != 1 {
		t.Fatalf("judges only: QueueAll %d IgnoreAll %d, want 1 and 1", p.QueueAll, p.IgnoreAll)
	}
	s, err := c.DismissAllFindings(s, checksCtx("dismissAllFindings", nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.Flash != "Ignored 1 verdict" {
		t.Errorf("flash %q", s.Flash)
	}
	if r, _ := s.checkResult("filler-intensifiers"); r.Status != checks.StatusFail {
		t.Errorf("Ignore all under the judges filter touched a checker: %+v", r)
	}
	s.ChecksKind = ""
	s, err = c.QueueAllFindings(s, checksCtx("queueAllFindings", nil))
	if err != nil || len(s.Comments) != 1 || s.Comments[0].Check != "filler-intensifiers" {
		t.Errorf("page-wide Queue all queued %+v (err %v), want only the checker's finding", s.Comments, err)
	}
}

// TestIgnoreToggles: Ignore is a toggle at every level. A verdict's own button
// ignores and brings it back; once everything in a row or on the page is
// ignored, the pressed Ignored N brings it all back — on the page even after
// every group passes and folds.
func TestIgnoreToggles(t *testing.T) {
	c := judgeController(t)
	s := judgedState(t, c, "fail", "very good", nil)                                     // the judge and filler-intensifiers both fail doc.md
	writeRepoFile(t, c, "doc.md", strings.Join(sideContent(mdDiff(), "new"), "\n")+"\n") // a checker's Ignore reads the line
	s.ShowLibrary, s.LibraryTab, s.ExpandedCheck = true, libTabChecks, "reader-first"
	status := func(s PrereviewState, id string) string { r, _ := s.checkResult(id); return r.Status }

	toggle := func(s PrereviewState) PrereviewState {
		t.Helper()
		s, err := c.ToggleIgnoreVerdict(s, checksCtx("toggleIgnoreVerdict", map[string]interface{}{"id": "reader-first"}))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s = toggle(s); status(s, "reader-first") != checks.StatusPass || pageRow(t, s.CheckPage(), "reader-first").Verdicts[0].Status != verdictIgnored {
		t.Fatalf("the verdict's Ignore didn't ignore it: %s", status(s, "reader-first"))
	}
	if row := pageRow(t, s.CheckPage(), "reader-first"); row.IgnoreAll != 0 || row.Ignored != 1 {
		t.Errorf("row after ignoring its only failure: IgnoreAll %d Ignored %d, want 0 and 1", row.IgnoreAll, row.Ignored)
	}
	if s = toggle(s); status(s, "reader-first") != checks.StatusFail {
		t.Fatalf("pressing Ignored didn't bring it back: %s", status(s, "reader-first"))
	}

	// The row's pressed Ignored N.
	s, _ = c.DismissAllFindings(s, checksCtx("dismissAllFindings", map[string]interface{}{"id": "reader-first"}))
	s, _ = c.RestoreAllDismissed(s, checksCtx("restoreAllDismissed", map[string]interface{}{"id": "reader-first"}))
	if status(s, "reader-first") != checks.StatusFail {
		t.Errorf("the row's Ignored N didn't bring it back")
	}

	// The page's: everything ignored, every group folds, the toggle stays.
	s.ExpandedCheck = ""
	s, err := c.DismissAllFindings(s, checksCtx("dismissAllFindings", nil))
	if err != nil {
		t.Fatal(err)
	}
	p := s.CheckPage()
	if len(p.Groups) != 0 || p.IgnoreAll != 0 || p.Ignored != 2 {
		t.Fatalf("page after Ignore all: %d groups listed, IgnoreAll %d, Ignored %d; want 0, 0, 2", len(p.Groups), p.IgnoreAll, p.Ignored)
	}
	s, _ = c.RestoreAllDismissed(s, checksCtx("restoreAllDismissed", nil))
	if p := s.CheckPage(); p.IgnoreAll != 2 || p.Ignored != 0 {
		t.Errorf("page after Ignored N: IgnoreAll %d Ignored %d, want 2 and 0", p.IgnoreAll, p.Ignored)
	}
}
