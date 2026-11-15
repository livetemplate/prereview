package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
)

// judgeController is a checks controller with one judge in its rule folder.
func judgeController(t *testing.T) *PrereviewController {
	t.Helper()
	c := checksController(t)
	os.MkdirAll(c.ChecksDir, 0o755)
	os.WriteFile(filepath.Join(c.ChecksDir, "reader-first.md"),
		[]byte("---\nkind: judge\nname: Reader first\ncategory: Reader-first\n---\nDoes it lead with what the reader must do?\n"), 0o644)
	return c
}

func judgeRule(t *testing.T, c *PrereviewController) *checks.Rule {
	t.Helper()
	for _, r := range c.runner().lib.Rules {
		if r.ID == "reader-first" {
			return r
		}
	}
	t.Fatal("judge not loaded")
	return nil
}

func TestJudgeResult_Statuses(t *testing.T) {
	c := judgeController(t)
	r := judgeRule(t, c)
	lines := []string{"Plain line.", "This is very good."}
	hash := checks.ContentHash(lines)
	get := func() []string { return lines }

	if res := judgeResult(r, Judgment{}, false, hash, get); res.Status != checks.StatusNotRun {
		t.Errorf("no verdict → %s", res.Status)
	}
	j := Judgment{File: "doc.md", Judge: r.ID, Verdict: "fail", Critique: "Buries the action.", Quote: "very good",
		ContentHash: hash, RubricHash: r.RubricHash, At: "2026-11-15T10:00:00Z"}
	res := judgeResult(r, j, true, hash, get)
	if res.Status != checks.StatusFail || res.Critique != "Buries the action." || len(res.Findings) != 1 {
		t.Fatalf("current verdict %+v", res)
	}
	if f := res.Findings[0]; f.Line != 2 || f.FromCol != 8 || f.ToCol != 17 || f.Message != "Buries the action." {
		t.Errorf("quote located at %+v", f)
	}
	if res := judgeResult(r, j, true, "other-content", get); res.Status != checks.StatusStale || len(res.Findings) != 0 {
		t.Errorf("a changed file makes the verdict stale: %+v", res)
	}
	j2 := j
	j2.RubricHash = "old-rubric"
	if res := judgeResult(r, j2, true, hash, get); res.Status != checks.StatusStale {
		t.Errorf("an edited rubric makes the verdict stale: %s", res.Status)
	}
	j3 := j
	j3.Quote = "not in the file"
	if res := judgeResult(r, j3, true, hash, get); res.Status != checks.StatusFail || len(res.Findings) != 0 {
		t.Errorf("a quote that isn't found leaves the verdict without a finding: %+v", res)
	}
}

func TestKeyContentHashMatchesContentHash(t *testing.T) {
	lines := []string{"a", "b c", ""}
	if got, want := keyContentHash(checkKey("x.md", lines, "lib")), checks.ContentHash(lines); got != want {
		t.Errorf("checkKey's content part %q must equal ContentHash %q (the page reads one as the other)", got, want)
	}
}

// judgedState shows mdDiff with the judge's verdict recorded as given.
func judgedState(t *testing.T, c *PrereviewController, verdict, quote string, mutate func(*Judgment)) PrereviewState {
	t.Helper()
	d := mdDiff()
	r := judgeRule(t, c)
	if verdict != "" {
		j := Judgment{File: d.Path, Judge: r.ID, Verdict: verdict, Critique: "Buries the action.", Quote: quote,
			ContentHash: checks.ContentHash(sideContent(d, "new")), RubricHash: r.RubricHash}
		if mutate != nil {
			mutate(&j)
		}
		if err := AppendJudgment(c.CSVPath, j); err != nil {
			t.Fatal(err)
		}
	}
	s := PrereviewState{SelectedFile: d.Path, AgentMode: true}
	c.showDiff(&s, d)
	return s
}

func TestCheckSummary_MergesJudges(t *testing.T) {
	c := judgeController(t)
	// mdDiff: filler-intensifiers fails; the other two built-ins pass.
	if got := judgedState(t, c, "", "", nil).CheckSummary(); got.Total != 3 || got.Passed != 2 {
		t.Errorf("a judge with no verdict counts in neither number: %+v", got)
	}
	calibrated := func(s PrereviewState) PrereviewState { // as applyChecks marks a calibrated judge
		for i := range s.Checks.Results {
			if s.Checks.Results[i].ID == "reader-first" {
				s.Checks.Results[i].Advisory = false
			}
		}
		return s
	}
	if got := judgedState(t, c, "pass", "", nil).CheckSummary(); got.Total != 3 || got.Passed != 2 {
		t.Errorf("an advisory judge (not calibrated) counts in neither number, even with a verdict: %+v", got)
	}
	c1 := judgeController(t)
	if got := calibrated(judgedState(t, c1, "pass", "", nil)).CheckSummary(); got.Total != 4 || got.Passed != 3 {
		t.Errorf("a calibrated passing judge counts in both: %+v", got)
	}
	c2 := judgeController(t)
	if got := calibrated(judgedState(t, c2, "fail", "", nil)).CheckSummary(); got.Total != 4 || got.Passed != 2 || !got.Failing {
		t.Errorf("a calibrated failing judge counts in the total and marks failing: %+v", got)
	}
	c2b, c2c := judgeController(t), judgeController(t)
	if got, none := judgedState(t, c2b, "fail", "", nil).CheckSummary(), judgedState(t, c2c, "", "", nil).CheckSummary(); got != none {
		t.Errorf("an advisory failing judge changed the summary: %+v vs no verdict %+v", got, none)
	}
	c3 := judgeController(t)
	if got := judgedState(t, c3, "pass", "", func(j *Judgment) { j.ContentHash = "old" }).CheckSummary(); got.Total != 3 {
		t.Errorf("a stale verdict counts in neither number: %+v", got)
	}
}

func TestCheckStrip_Judges(t *testing.T) {
	c := judgeController(t)
	s := judgedState(t, c, "", "", nil)
	st := s.CheckStrip()
	if st == nil || st.JudgesRun != 1 || st.NeedsAgent || st.JudgeWaiting != 0 {
		t.Fatalf("idle bar offers Run judges (1): %+v", st)
	}
	s.AgentMode = false
	if !s.CheckStrip().NeedsAgent {
		t.Error("without an agent the bar says Run judges needs one")
	}
	s.AgentMode = true

	s, err := c.RunJudges(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := s.Comments[len(s.Comments)-1]
	if last.Kind != "file" || !strings.Contains(last.Body, "--judge reader-first") || !strings.Contains(last.Body, "Does it lead with what the reader must do?") ||
		!strings.Contains(last.Body, "--content-hash "+checks.ContentHash(sideContent(s.CurrentDiff, "new"))) {
		t.Errorf("Run judges queues one file-level request naming the command and rubric: %+v", last)
	}
	if st := s.CheckStrip(); st.JudgeWaiting != 1 {
		t.Errorf("after Run judges the bar waits: %+v", st)
	}
	if last.Request != csv.RequestJudge {
		t.Errorf("Run judges marks its comment a judge request: %q", last.Request)
	}
	// The marker decides, not the text: the reviewer's own comment with the same
	// words is not a request, so it never makes the bar wait.
	said := last
	said.Request = ""
	if st := (PrereviewState{SelectedFile: s.SelectedFile, Comments: []Comment{said}, Checks: s.Checks, AgentMode: true}).CheckStrip(); st.JudgeWaiting != 0 {
		t.Errorf("an unmarked comment that reads like a judge request made the bar wait: %+v", st)
	}
	// Waiting is the queue's own: the open request comment, so another tab (or a
	// reload) sees it too.
	other := PrereviewState{SelectedFile: s.SelectedFile, Comments: s.Comments, Checks: s.Checks, AgentMode: true}
	if st := other.CheckStrip(); st.JudgeWaiting != 1 {
		t.Errorf("another tab sees the wait: %+v", st)
	}
	req := len(s.Comments) - 1
	s.Comments[req].Created = time.Now().Add(-11 * time.Minute)
	if st := s.CheckStrip(); st.JudgeWaiting != 0 || !st.JudgeTimedOut {
		t.Errorf("after 10 minutes with no answer it offers to run again: %+v", st)
	}
	s.Comments[req].Processed = true
	if st := s.CheckStrip(); st.JudgeWaiting != 0 || st.JudgeTimedOut {
		t.Errorf("a request the agent marked done stops the wait: %+v", st)
	}

	// The agent answers (fail, with a quote): the judge joins the failing list,
	// and picking it shows its card.
	r := judgeRule(t, c)
	AppendJudgment(c.CSVPath, Judgment{File: s.SelectedFile, Judge: r.ID, Verdict: "fail", Critique: "Buries the action.",
		Quote: "very good", ContentHash: checks.ContentHash(sideContent(s.CurrentDiff, "new")), RubricHash: r.RubricHash})
	s, _ = c.ChecksUpdated(s, nil)
	st = s.CheckStrip()
	var judgePick bool
	for _, p := range st.Failing {
		judgePick = judgePick || (p.ID == "reader-first" && p.Judge)
	}
	if !judgePick || st.JudgeWaiting != 0 || st.JudgeTimedOut {
		t.Errorf("the answered judge joins the failing list: %+v", st)
	}
	s, _ = c.SelectCheck(s, checksCtx("selectCheck", map[string]interface{}{"id": "reader-first"}))
	st = s.CheckStrip()
	if st == nil || st.Judge == nil || st.Judge.Critique != "Buries the action." || !st.Judge.CanShow {
		t.Fatalf("picked judge card: %+v", st)
	}
	if s.CursorKey != "L25-25" {
		t.Errorf("picking a judge moves to its quote (line 25): %q", s.CursorKey)
	}
	if len(s.FindingsByRow()) != 0 || len(s.findingMarksByLine()[25]) != 1 {
		t.Errorf("a judge's quote is marked, not carded (its card is above the file)")
	}
}

func TestLabelJudgment(t *testing.T) {
	c := judgeController(t)
	c.LibraryDir = t.TempDir() // the answer also lands in the per-user calibration log
	s := judgedState(t, c, "fail", "", nil)
	s, err := c.LabelJudgment(s, checksCtx("labelJudgment", map[string]interface{}{"id": "reader-first", "label": "agree"}))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.checkResult("reader-first")
	if s.JudgeLabels[labelKey(s.SelectedFile, "reader-first", r.JudgedAt)] != LabelAgree {
		t.Fatalf("labels %+v", s.JudgeLabels)
	}
	s.ShowLibrary, s.LibraryTab = true, libTabChecks
	s.ExpandedCheck = "reader-first"
	p := s.CheckPage()
	var row CheckRow
	for _, g := range p.Groups {
		for _, rr := range g.Rows {
			if rr.ID == "reader-first" {
				row = rr
			}
		}
	}
	if !row.IsJudge || !strings.HasPrefix(row.Agreement, "agrees with you 1 of 1 · counts once 10 answers") || len(row.Verdicts) != 1 || row.Verdicts[0].Label != LabelAgree {
		t.Errorf("judge row %+v", row)
	}
	if cal := library.LoadCalibration(c.LibraryDir, map[string]string{"reader-first": judgeRule(t, c).RubricHash})["reader-first"]; cal.Total != 1 || cal.Agree != 1 {
		t.Errorf("the calibration log didn't get the answer with its rubric version: %+v", cal)
	}
	// The same answer again takes it back.
	s, _ = c.LabelJudgment(s, checksCtx("labelJudgment", map[string]interface{}{"id": "reader-first", "label": "agree"}))
	if len(s.JudgeLabels) != 0 || s.JudgeCal["reader-first"].Total != 0 {
		t.Errorf("taking the answer back: %+v %+v", s.JudgeLabels, s.JudgeCal["reader-first"])
	}
}

func TestCheckPage_KindChips(t *testing.T) {
	c := judgeController(t)
	s := judgedState(t, c, "", "", nil)
	s.ShowLibrary, s.LibraryTab = true, libTabChecks
	s.ShowPassingGroups = true
	p := s.CheckPage()
	if p.Judges != 1 || p.Checkers != 3 {
		t.Fatalf("kind counts checkers %d judges %d", p.Checkers, p.Judges)
	}
	s.ChecksKind = checks.TypeJudge
	p = s.CheckPage()
	if len(p.Groups) != 1 || p.Groups[0].Rows[0].ID != "reader-first" || p.Groups[0].Rows[0].Summary != "not run" {
		t.Errorf("judges only: %+v", p.Groups)
	}
}

// A queued critique shows Queued on its card instead of offering Queue again, on
// both branches (the quote found: a text comment on it; no quote: a file-level
// comment), across a reload, until a newer verdict lands or it's resolved.
func TestQueueJudgment(t *testing.T) {
	for _, tc := range []struct{ name, quote, kind string }{
		{"quoted", "very good", "text"},
		{"file-level", "", "file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := judgeController(t)
			s := judgedState(t, c, "fail", tc.quote, nil)
			s, _ = c.SelectCheck(s, checksCtx("selectCheck", map[string]interface{}{"id": "reader-first"}))
			queued := func(s PrereviewState) bool { return s.CheckStrip().Judge.Queued }
			if queued(s) {
				t.Fatal("Queued before anything was queued")
			}
			queue := func(s PrereviewState) PrereviewState {
				t.Helper()
				s, err := c.QueueJudgment(s, checksCtx("queueJudgment", map[string]interface{}{"id": "reader-first"}))
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			s = queue(s)
			if len(s.Comments) != 1 || s.Comments[0].Kind != tc.kind || s.Comments[0].Check != "reader-first" {
				t.Fatalf("queued %+v, want one %s comment marked reader-first", s.Comments, tc.kind)
			}
			if !queued(s) {
				t.Error("the card isn't Queued after Queue as comment")
			}
			if s = queue(s); len(s.Comments) != 1 {
				t.Errorf("queued twice: %d comments", len(s.Comments))
			}

			// A reload reads created_at back in whole seconds; still Queued.
			s.Comments = c.loadCommentsFromDisk()
			if len(s.Comments) != 1 || !queued(s) {
				t.Errorf("not Queued after a reload: %+v", s.Comments)
			}

			// A newer verdict can be queued again.
			later := judgedState(t, c, "fail", tc.quote, func(j *Judgment) {
				j.Critique, j.At = "Still buries it.", time.Now().UTC().Add(2*time.Second).Format(time.RFC3339Nano)
			})
			later.Comments = s.Comments
			later, _ = c.SelectCheck(later, checksCtx("selectCheck", map[string]interface{}{"id": "reader-first"}))
			if queued(later) {
				t.Error("a newer verdict reads as already queued")
			}

			// Resolved: Queue is offered again.
			s.Comments[0].Resolved = true
			if queued(s) {
				t.Error("a resolved comment still reads as queued")
			}
		})
	}
}
