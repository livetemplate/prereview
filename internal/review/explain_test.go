package review

import (
	"encoding/json"
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
