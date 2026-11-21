package review

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeAt(t *testing.T, dir, rel, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// explainFixture is a git repo with a.go and b.go changed and c.go unchanged, and
// a controller and state over it, as Mount would have them.
func explainFixture(t *testing.T) (*PrereviewController, PrereviewState) {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "t@example.com")
	gitIn(t, dir, "config", "user.name", "T")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	writeAt(t, dir, "a.go", "1\n2\n3\n4\n5\n")
	writeAt(t, dir, "b.go", "x\ny\n")
	writeAt(t, dir, "c.go", "c1\nc2\nc3\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "seed")
	writeAt(t, dir, "a.go", "1\n2\n5\n")          // old 5 lines, new 3
	writeAt(t, dir, "b.go", "x\ny\nz\nw\nv\nu\n") // new 6 lines
	files, err := gitdiff.ListFiles(dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(dir, ".prereview", "comments.csv")
	c := &PrereviewController{RepoPath: dir, Base: "HEAD", CSVPath: csvPath, CSVWriter: csv.NewWriter(csvPath)}
	return c, PrereviewState{Files: files, Base: "HEAD", SelectedFile: "a.go", ShowAllFiles: true}
}

func writeExplanations(t *testing.T, c *PrereviewController, exps ...Explanation) {
	t.Helper()
	var b strings.Builder
	for _, e := range exps {
		line, err := json.Marshal(NormalizeExplanation(e))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	writeAt(t, filepath.Dir(c.CSVPath), ExplainFileName, b.String())
}

// TestGroundExplain: a step can cite any file in the review, not only the open
// one; a line must exist on that file's side, and a path outside the review is
// never grounded.
func TestGroundExplain(t *testing.T) {
	c, state := explainFixture(t)
	cases := []struct {
		name string
		a    Anchor
		want string
	}{
		{"line in another changed file", Anchor{File: "b.go", FromLine: 3, ToLine: 6}, anchorOK},
		{"past the end of another file", Anchor{File: "b.go", FromLine: 5, ToLine: 7}, anchorUngrounded},
		{"old side of the open file", Anchor{File: "a.go", Side: "old", FromLine: 4, ToLine: 5}, anchorOK},
		{"new side is shorter", Anchor{File: "a.go", FromLine: 4, ToLine: 4}, anchorUngrounded},
		{"whole changed file", Anchor{File: "a.go", Kind: AnchorKindFile}, anchorOK},
		{"unchanged listed file", Anchor{File: "c.go", FromLine: 2, ToLine: 3}, anchorOK},
		{"past the end of an unchanged file", Anchor{File: "c.go", FromLine: 4, ToLine: 4}, anchorUngrounded},
		{"not in the review", Anchor{File: "z.go", Kind: AnchorKindFile}, anchorUngrounded},
	}
	var steps []Step
	for i, tc := range cases {
		steps = append(steps, Step{ID: string(rune('a' + i)), Title: tc.name, Anchors: []Anchor{tc.a}})
	}
	writeExplanations(t, c, Explanation{ID: "e1", Steps: steps})
	c.applyExplain(&state)
	if len(state.Explanations) != 1 {
		t.Fatalf("loaded %d explanations, want 1", len(state.Explanations))
	}
	for i, tc := range cases {
		if got := state.Explanations[0].Steps[i].Anchors[0].Status; got != tc.want {
			t.Errorf("%s: %+v grounded %q, want %q", tc.name, tc.a, got, tc.want)
		}
	}
	// The drawer showing only changed files doesn't change what's citable: an
	// unchanged file is still in the review, one "show all" away.
	state.ShowAllFiles = false
	c.applyExplain(&state)
	if got := state.Explanations[0].Steps[5].Anchors[0].Status; got != anchorOK {
		t.Errorf("unchanged c.go with the changed-only drawer: %q, want ok", got)
	}
}

// TestApplyExplainStale: an explanation keeps the fingerprint it was first seen
// against (on disk, so across relaunches); after an edit to a reviewed file it is
// stale, and a new explanation written after the edit is not.
func TestApplyExplainStale(t *testing.T) {
	c, state := explainFixture(t)
	step := []Step{{Title: "t", Anchors: []Anchor{{File: "a.go", Kind: AnchorKindFile}}}}
	writeExplanations(t, c, Explanation{ID: "e1", Steps: step})
	c.applyExplain(&state)
	if state.Explanations[0].Stale {
		t.Fatal("a just-written explanation is stale")
	}
	idx := c.changedIdx.idx
	c.applyExplain(&state)
	if c.changedIdx.idx != idx {
		t.Error("nothing changed, yet the index was rebuilt")
	}
	// Toggling the drawer is a view change, not a data change.
	state.ShowAllFiles = !state.ShowAllFiles
	c.applyExplain(&state)
	if c.changedIdx.idx != idx {
		t.Error("toggling show-all rebuilt the index")
	}

	writeAt(t, c.RepoPath, "b.go", "edited\n")
	writeExplanations(t, c, Explanation{ID: "e1", Steps: step}, Explanation{ID: "e2", Steps: step})
	relaunch := &PrereviewController{RepoPath: c.RepoPath, Base: c.Base, CSVPath: c.CSVPath}
	relaunch.applyExplain(&state)
	byID := map[string]bool{}
	for _, e := range state.Explanations {
		byID[e.ID] = e.Stale
	}
	if !byID["e1"] || byID["e2"] {
		t.Errorf("stale by id = %v, want e1 stale (written before the edit), e2 not", byID)
	}
}

// The watcher must notice explain.jsonl, or a walkthrough appears only when some
// other agent signal happens to fire.
func TestSignalFingerprintWatchesExplain(t *testing.T) {
	c, _ := explainFixture(t)
	before := c.agentSignalFingerprint()
	writeExplanations(t, c, Explanation{ID: "e1", Steps: []Step{{Title: "t", Anchors: []Anchor{{File: "a.go", Kind: AnchorKindFile}}}}})
	if c.agentSignalFingerprint() == before {
		t.Error("writing explain.jsonl did not move the agent signal fingerprint")
	}
}

// TestLoadExplanations: last write wins and moves to the end (the newest
// explanation is the one the reviewer reads); torn lines are skipped; defaults
// are filled at the read boundary.
func TestLoadExplanations(t *testing.T) {
	path := filepath.Join(t.TempDir(), ExplainFileName)
	lines := []string{
		`{"id":"a","steps":[{"title":"old a","anchors":[{"file":"x.go","from_line":1}]}]}`,
		`{"id":"b","steps":[{"title":"b","anchors":[{"file":"x.go","from_line":2}]}]}`,
		`{"id":"a","steps":[{"title":"new a","anchors":[{"file":"x.go","from_line":3}]}]}`,
		`{"id":"torn","steps":[{"ti`,
		`{"id":"empty","steps":[]}`,
	}
	writeAt(t, filepath.Dir(path), ExplainFileName, strings.Join(lines, "\n")+"\n")
	got := loadExplanations(path)
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" || got[1].Steps[0].Title != "new a" {
		t.Fatalf("got %+v, want [b, a(new)]", got)
	}
	if a := got[1].Steps[0].Anchors[0]; a.Kind != AnchorKindLine || a.Side != "new" || a.ToLine != 3 || got[1].Steps[0].ID != "s1" {
		t.Errorf("read-boundary defaults not filled: step %q anchor %+v", got[1].Steps[0].ID, a)
	}
}

// TestRequestExplain: "Explain this change" queues the walkthrough prompt as an
// explain request (never reviewer feedback), says the scope is the whole change,
// and reads pending until explain.jsonl moves.
func TestRequestExplain(t *testing.T) {
	c, state := explainFixture(t)
	if err := os.MkdirAll(filepath.Dir(c.CSVPath), 0o755); err != nil {
		t.Fatal(err)
	}
	state.ExplainPrompts = LoadExplainPrompts("")
	state.AgentMode = true
	c.applyExplain(&state)
	got, err := c.RequestExplain(state, checksCtx("requestExplain", nil))
	if err != nil {
		t.Fatal(err)
	}
	disk := c.loadCommentsFromDisk()
	last := disk[len(disk)-1]
	if last.Request != csv.RequestExplain || last.File != "a.go" || last.Kind != "file" {
		t.Fatalf("the request isn't an explain request on the open file: %+v", last)
	}
	if !strings.Contains(last.Body, "prereview explain") || !strings.Contains(last.Body, "2 changed file(s) against `HEAD`") {
		t.Errorf("the body must name the command and the whole-change scope:\n%s", last.Body)
	}
	if !got.ExplainPending() {
		t.Error("just requested, but not pending")
	}
	// Pending lives on the request row, so a fresh load (a reload, another tab)
	// still sees it.
	reloaded := got
	reloaded.Comments = c.loadCommentsFromDisk()
	if !reloaded.ExplainPending() {
		t.Error("pending must survive a reload: it is derived from the queued request")
	}
	// `prereview explain` stamps created_at; a walkthrough written after the
	// request ends the wait.
	writeExplanations(t, c, Explanation{ID: "e1", CreatedAt: time.Now().UTC().Add(time.Second).Format(time.RFC3339),
		Steps: []Step{{Title: "t", Anchors: []Anchor{{File: "a.go", Kind: AnchorKindFile}}}}})
	c.applyExplain(&got)
	if got.ExplainPending() {
		t.Error("an explanation arrived, yet the request still reads pending")
	}
}

// The built-in prompt renders in full on the request's card, so it stays short,
// and it keeps the instructions that stop the agent editing or suggesting.
func TestExplainPromptBuiltin(t *testing.T) {
	ps := LoadExplainPrompts("")
	if len(ps) != 1 {
		t.Fatalf("want one built-in walkthrough prompt, got %d", len(ps))
	}
	if n := len(strings.Split(ps[0].Body, "\n")); n > 16 {
		t.Errorf("the built-in prompt is %d lines; keep it under 16 and put detail in the skill", n)
	}
	for _, must := range []string{"prereview explain", "prereview suggest", "Cite\nthe lines"} {
		if !strings.Contains(ps[0].Body, must) {
			t.Errorf("the prompt must mention %q", must)
		}
	}
}

// TestJumpToAnchor: a step's link opens the file it cites, which needn't be the
// open one, and parks the cursor on the cited line of that file's diff.
func TestJumpToAnchor(t *testing.T) {
	c, state := explainFixture(t)
	state.ShowUnderstand = true
	got, err := c.JumpToAnchor(state, checksCtx("jumpToAnchor", map[string]interface{}{"file": "b.go", "side": "new", "line": "4"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.SelectedFile != "b.go" || got.ShowUnderstand {
		t.Errorf("selected %q, page open %v; want b.go with the page closed", got.SelectedFile, got.ShowUnderstand)
	}
	if got.CursorKey != "L0-4" || got.RevealFile != "b.go" {
		t.Errorf("cursor %q reveal %q, want L0-4 on b.go (an added line has old 0)", got.CursorKey, got.RevealFile)
	}
	// A whole-file anchor just opens the file.
	got, err = c.JumpToAnchor(state, checksCtx("jumpToAnchor", map[string]interface{}{"file": "c.go", "side": "new", "line": "0"}))
	if err != nil || got.SelectedFile != "c.go" || got.CursorKey != "" {
		t.Errorf("whole-file jump: err %v, file %q, cursor %q", err, got.SelectedFile, got.CursorKey)
	}
}

// TestUnderstandTabs: the page opens on the tab asked for; otherwise on the
// walkthrough, unless there is none and the file has a quiz.
func TestUnderstandTabs(t *testing.T) {
	c, state := explainFixture(t)
	if state.UnderstandOnQuiz() {
		t.Error("nothing on the page: the walkthrough tab (with its Explain button) comes first")
	}
	state.Quizzes = []Quiz{{ID: "z", File: "a.go", Questions: []Question{{ID: "q", Kind: QuestionKindFile}}}}
	if !state.UnderstandOnQuiz() {
		t.Error("only a quiz: the page should open on it")
	}
	state.Explanations = []Explanation{{ID: "e"}}
	if state.UnderstandOnQuiz() {
		t.Error("a walkthrough exists: it comes first")
	}
	open, err := c.ToggleUnderstand(state, checksCtx("toggleUnderstand", map[string]interface{}{"tab": "quiz"}))
	if err != nil || !open.ShowUnderstand || !open.UnderstandOnQuiz() {
		t.Fatalf("Overview opens the quiz tab: err %v, open %v, quiz %v", err, open.ShowUnderstand, open.UnderstandOnQuiz())
	}
	switched, _ := c.SetUnderstandTab(open, checksCtx("setUnderstandTab", map[string]interface{}{"tab": "walkthrough"}))
	if !switched.ShowUnderstand || switched.UnderstandOnQuiz() {
		t.Error("the Walkthrough tab must switch to the walkthrough")
	}
	if bogus, _ := c.SetUnderstandTab(open, checksCtx("setUnderstandTab", map[string]interface{}{"tab": "nope"})); bogus.UnderstandTab != "" {
		t.Errorf("an unknown tab must fall back to the default, got %q", bogus.UnderstandTab)
	}
	closed, _ := c.ToggleUnderstand(switched, checksCtx("toggleUnderstand", nil))
	if closed.ShowUnderstand {
		t.Error("z closes the page")
	}
}

// walkFixture: the explain fixture with a grounded three-step walkthrough on
// disk: b.go first, then a step citing nothing the review has, then a.go.
func walkFixture(t *testing.T) (*PrereviewController, PrereviewState) {
	t.Helper()
	c, state := explainFixture(t)
	writeExplanations(t, c, Explanation{ID: "w", Steps: []Step{
		{Title: "b first", Anchors: []Anchor{{File: "b.go", FromLine: 4, ToLine: 6}}},
		{Title: "nowhere", Anchors: []Anchor{{File: "z.go", FromLine: 1}}},
		{Title: "then a", Anchors: []Anchor{{File: "a.go", FromLine: 3}, {File: "c.go", Kind: AnchorKindFile}}},
	}})
	c.applyExplain(&state)
	return c, state
}

// TestWalkthroughSteps: Start lands on the first stop's file and line; › walks
// every grounded anchor in order, stopping (without jumping) on a step that
// cites nothing; the bar hides the "N open" stepper; a replaced walkthrough
// ends the walk.
func TestWalkthroughSteps(t *testing.T) {
	c, state := walkFixture(t)
	// b, nowhere, a, c, then the derived "Not explained" step: a.go's deletion
	// (old lines 3-4) is cited by no step.
	if got := len(walkStopsOf(state.walkSteps())); got != 5 {
		t.Fatalf("stops = %d, want 5 (one per grounded anchor, one for the step citing nothing, one not explained)", got)
	}
	state.ShowUnderstand = true
	s, err := c.StartWalkthrough(state, checksCtx("startWalkthrough", nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.ShowUnderstand || s.SelectedFile != "b.go" || s.CursorKey != "L0-4" {
		t.Fatalf("start: page %v, file %q, cursor %q; want b.go L0-4 with the page closed", s.ShowUnderstand, s.SelectedFile, s.CursorKey)
	}
	if bar := s.WalkBar(); bar == nil || bar.Num != 1 || bar.Total != 4 || bar.HasPrev || !bar.HasNext {
		t.Fatalf("bar at start = %+v", bar)
	}
	if s.ShowNav() {
		t.Error("the step bar takes the stepper's slot: ShowNav must be false while walking")
	}
	next := func(s PrereviewState) PrereviewState {
		t.Helper()
		out, err := c.WalkNext(s, checksCtx("walkNext", nil))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	s = next(s)
	if bar := s.WalkBar(); bar == nil || bar.Num != 2 || bar.Anchor != "" || s.SelectedFile != "b.go" {
		t.Fatalf("step 2 cites nothing: bar %+v, file %q (must stay put)", bar, s.SelectedFile)
	}
	s = next(s)
	if s.SelectedFile != "a.go" || s.CursorKey != "L5-3" { // new line 3 was old line 5
		t.Fatalf("step 3: file %q cursor %q, want a.go L5-3", s.SelectedFile, s.CursorKey)
	}
	s = next(s)
	if s.SelectedFile != "c.go" || s.CursorKey != "" {
		t.Fatalf("whole-file stop c.go: file %q cursor %q", s.SelectedFile, s.CursorKey)
	}
	s = next(s)
	if bar := s.WalkBar(); bar == nil || bar.Title != "Not explained" || bar.Anchor != "a.go L3-L4 (old)" || bar.HasNext || s.CursorKey != "L3-0" {
		t.Fatalf("last stop is the change no step cites: bar %+v, cursor %q", s.WalkBar(), s.CursorKey)
	}
	if again := next(s); again.WalkStep != s.WalkStep || again.WalkAnchor != s.WalkAnchor {
		t.Error("› on the last stop must stay there")
	}
	// From a step: Walk from here.
	from, _ := c.StartWalkthrough(state, checksCtx("startWalkthrough", map[string]interface{}{"step": "3"}))
	if from.SelectedFile != "a.go" || from.WalkBar().Num != 3 {
		t.Errorf("walk from step 3: file %q bar %+v", from.SelectedFile, from.WalkBar())
	}
	// A new walkthrough (another id) ends the walk.
	s.Explanations = append(s.Explanations, Explanation{ID: "other", Steps: s.Explanations[0].Steps})
	if s.WalkBar() != nil {
		t.Error("a replaced walkthrough must end the walk")
	}
	ended, _ := c.EndWalkthrough(from, checksCtx("endWalkthrough", nil))
	if ended.WalkBar() != nil {
		t.Error("× must put the bar away")
	}
}

// TestStepMarkLines: the open file's step starts are labelled on their rows, and
// those rows survive folding.
func TestStepMarkLines(t *testing.T) {
	c, state := explainFixture(t)
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	writeAt(t, c.RepoPath, "a.go", strings.Join(lines, "\n")+"\n")
	writeExplanations(t, c, Explanation{ID: "m", Steps: []Step{
		{Title: "far context", Anchors: []Anchor{{File: "a.go", FromLine: 30}}},
	}})
	files, err := gitdiff.ListFiles(c.RepoPath, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	state.Files = files
	c.applyExplain(&state)
	d, err := gitdiff.LoadDiff(c.RepoPath, "HEAD", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	state.CurrentDiff = d
	marks := state.StepMarkLines()
	var key string
	for k, v := range marks {
		if v == "Step 1: far context" {
			key = k
		}
	}
	if key == "" {
		t.Fatalf("no marker for the step: %v", marks)
	}
	for _, l := range state.VisibleLines() {
		if lineCursorKey(l) == key {
			return
		}
	}
	t.Errorf("row %s carries a step marker but was folded away", key)
}

// TestDrawerWalkOrder: in walkthrough order a file sorts by the first step that
// cites it, a directory by the earliest step under it, and uncited files last.
func TestDrawerWalkOrder(t *testing.T) {
	s := PrereviewState{
		Files: []gitdiff.FileEntry{
			{Path: "a.go", Status: "M"}, {Path: "docs/z.md", Status: "M"}, {Path: "m.go", Status: "M"}, {Path: "web/b.js", Status: "M"},
		},
		Explanations: []Explanation{{ID: "e", Steps: []Step{
			{Title: "1", Anchors: []Anchor{{File: "m.go"}}},
			{Title: "2", Anchors: []Anchor{{File: "web/b.js"}, {File: "a.go"}}},
		}}},
	}
	names := func() []string {
		var out []string
		for _, n := range s.FileTree() {
			out = append(out, n.Name)
		}
		return out
	}
	if got := strings.Join(names(), ","); got != "docs,web,a.go,m.go" {
		t.Errorf("A–Z order = %s", got)
	}
	s.DrawerWalkOrder = true
	if got := strings.Join(names(), ","); got != "m.go,web,a.go,docs" {
		t.Errorf("walkthrough order = %s, want m.go,web,a.go,docs", got)
	}
}

// The walk's cursor is the stop itself, not an index: when the shown stop's
// anchor stops resolving (after an edit), the walk stays on its step, at the
// step's first remaining stop, instead of drifting onto another step.
func TestWalkCursorSurvivesShiftingStops(t *testing.T) {
	c, state := walkFixture(t)
	s, err := c.StartWalkthrough(state, checksCtx("startWalkthrough", map[string]interface{}{"step": "3"}))
	if err != nil {
		t.Fatal(err)
	}
	s, _ = c.WalkNext(s, checksCtx("walkNext", nil)) // step 3, its second anchor (c.go)
	if s.WalkStep != 2 || s.WalkAnchor != 1 {
		t.Fatalf("cursor = step %d anchor %d, want 2/1", s.WalkStep, s.WalkAnchor)
	}
	s.Explanations[0].Steps[2].Anchors[1].Status = anchorUngrounded
	if bar := s.WalkBar(); bar == nil || bar.Num != 3 || bar.Anchor != "a.go L3" {
		t.Errorf("after c.go's anchor went ungrounded the bar must stay on step 3 at a.go, got %+v", bar)
	}
}

// TestNotExplained: the hunks no grounded anchor covers on their side. A partly
// covered hunk counts as explained; a deletion cited on the new side does not; an
// opaque file needs a whole-file anchor; an ungrounded anchor explains nothing;
// citing a text file whole doesn't explain its hunks.
func TestNotExplained(t *testing.T) {
	idx := &gitdiff.ChangedIndex{Files: map[string]*gitdiff.FileChanges{
		"a.go":     {Hunks: []gitdiff.Hunk{{OldStart: 3, OldCount: 0, NewStart: 3, NewCount: 3}, {OldStart: 9, OldCount: 1, NewStart: 10, NewCount: 1}}, NewLen: 20, OldLen: 18},
		"b.go":     {Hunks: []gitdiff.Hunk{{OldStart: 7, OldCount: 2, NewStart: 6, NewCount: 0}}, NewLen: 10, OldLen: 12},
		"bin.png":  {Opaque: true},
		"logo.png": {Opaque: true},
		"c.go":     {Hunks: []gitdiff.Hunk{{NewStart: 1, NewCount: 2}}, NewLen: 2},
		"d.go":     {Hunks: []gitdiff.Hunk{{NewStart: 5, NewCount: 1}}, NewLen: 9},
	}}
	ok := func(a Anchor) Anchor { normalizeAnchor(&a); a.Status = anchorOK; return a }
	steps := []Step{
		{Anchors: []Anchor{ok(Anchor{File: "a.go", FromLine: 4})}},              // part of a.go's first hunk
		{Anchors: []Anchor{ok(Anchor{File: "b.go", FromLine: 7})}},              // new side: the deletion is old 7-8
		{Anchors: []Anchor{ok(Anchor{File: "logo.png", Kind: AnchorKindFile})}}, // explains the opaque file
		{Anchors: []Anchor{{File: "c.go", FromLine: 1, ToLine: 2, Status: anchorUngrounded}}},
		{Anchors: []Anchor{ok(Anchor{File: "d.go", Kind: AnchorKindFile})}}, // whole text file: not its hunks
	}
	var got []string
	for _, a := range notExplained(idx, steps) {
		got = append(got, a.Label())
	}
	want := []string{"a.go L10", "b.go L7-L8 (old)", "bin.png", "c.go L1-L2", "d.go L5"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("not explained = %v\nwant %v", got, want)
	}
}

// The derived step joins the walk last, and never writes into the agent's steps.
func TestWalkStepsAppendsNotExplained(t *testing.T) {
	steps := make([]Step, 1, 4) // spare capacity: an unclipped append would write into it
	steps[0] = Step{ID: "s1", Title: "one"}
	s := PrereviewState{Explanations: []Explanation{{ID: "e", Steps: steps}},
		NotExplained: []Anchor{{File: "x.go", Kind: AnchorKindLine, Side: "new", FromLine: 1, ToLine: 1, Status: anchorOK}}}
	ws := s.walkSteps()
	if len(ws) != 2 || !ws[1].Derived || ws[0].Derived || ws[1].Anchors[0].File != "x.go" {
		t.Fatalf("walkSteps = %+v", ws)
	}
	ws[1].Title = "changed"
	if again := s.walkSteps(); again[1].Title != "Not explained" || len(s.Explanations[0].Steps) != 1 {
		t.Error("the derived step leaked into the explanation's own steps")
	}
}
