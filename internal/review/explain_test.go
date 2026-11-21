package review

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
	c := &PrereviewController{RepoPath: dir, Base: "HEAD", CSVPath: filepath.Join(dir, ".prereview", "comments.csv")}
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
