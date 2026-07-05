package review

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/livetemplate/prereview/csv"
)

// gitRepoWith writes files into a fresh git repo (committed) and returns its dir.
func gitRepoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "seed")
	return dir
}

// seededSuggestion returns a suggestion anchored to originalText the way
// applySuggestions derives it at load, so relocateSuggestion can drift it.
func seededSuggestion(id, file string, fromLine int, originalText string) Suggestion {
	return Suggestion{
		ID: id, File: file, FromLine: fromLine, ToLine: fromLine, Side: "new",
		OriginalText: originalText, ProposedText: "REPLACED",
		Anchor: CommentAnchor{Text: normJoinText(originalText)}, AnchorStatus: anchorOK,
	}
}

// TestSuggestionDrift_121_DropsFromSnapshotWhenTargetEditedAway reproduces #121:
// a decided suggestion whose target line is edited away on disk must re-anchor to
// `outdated` and DROP from the emitted snapshot (self-pruning). This is the
// guarantee the continuous-emission engine depends on. Asserts the SNAPSHOT
// path (what the agent receives), not the live per-tab render.
func TestSuggestionDrift_121_DropsFromSnapshotWhenTargetEditedAway(t *testing.T) {
	const orig = "\treturn \"The quick brown fox\""
	repo := gitRepoWith(t, map[string]string{
		"app.go": "package app\n\nfunc Greet() string {\n" + orig + "\n}\n",
	})
	csvPath := filepath.Join(repo, ".prereview", "comments.csv")
	if err := os.MkdirAll(filepath.Dir(csvPath), 0o755); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	c := &PrereviewController{
		RepoPath: repo, Base: "HEAD", CSVPath: csvPath,
		DonePath: filepath.Join(repo, ".prereview", "DONE"),
		CSVWriter: csv.NewWriter(csvPath), SkillMode: true, StreamMode: true,
		Emitter: NewEventStream(buf, ""),
	}

	sg := seededSuggestion("s1", "app.go", 4, orig)
	st := PrereviewState{
		Suggestions: []Suggestion{sg},
		Decisions:   []SuggestionDecision{{SuggestionID: "s1", Verdict: verdictAccept, Fingerprint: suggestionFingerprint(sg)}},
	}

	// Round 1: target text is present → the decided suggestion is in the snapshot.
	if _, err := c.HandOff(st, regionCtx("handOff", nil)); err != nil {
		t.Fatalf("HandOff 1: %v", err)
	}
	evs := decodeEvents(t, buf.Bytes())
	if n := len(evs[0].DecisionList()); n != 1 {
		t.Fatalf("round 1: want the decided suggestion in the snapshot, got %d", n)
	}

	// The agent applies the accept: the original text is gone from the file.
	if err := os.WriteFile(filepath.Join(repo, "app.go"),
		[]byte("package app\n\nfunc Greet() string {\n\treturn \"A slow red fox\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Round 2: the suggestion must re-anchor outdated and DROP from the snapshot.
	buf.Reset()
	if _, err := c.HandOff(st, regionCtx("handOff", nil)); err != nil {
		t.Fatalf("HandOff 2: %v", err)
	}
	evs = decodeEvents(t, buf.Bytes())
	if n := len(evs[0].DecisionList()); n != 0 {
		t.Errorf("#121: an applied/edited-away suggestion must drop from the snapshot, got %d", n)
	}
}
