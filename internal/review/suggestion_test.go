package review

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

// writeJSONL writes lines joined by "\n" to a temp suggestions file and returns
// its path — the agent-append format loadSuggestions consumes.
func writeSuggestionsFile(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, SuggestionFileName)
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write suggestions file: %v", err)
	}
	return path
}

func TestLoadSuggestions_Missing(t *testing.T) {
	// A missing file is the common case (no agent has suggested anything) and must
	// yield nil, never an error.
	if got := loadSuggestions(filepath.Join(t.TempDir(), "nope.jsonl")); got != nil {
		t.Fatalf("missing file: want nil, got %v", got)
	}
}

func TestLoadSuggestions_Tolerant(t *testing.T) {
	// Torn / blank / id-less / file-less lines are skipped; valid ones survive, and
	// side defaults to "new".
	path := writeSuggestionsFile(t,
		`{"id":"s1","file":"a.md","from_line":2,"to_line":2,"original":"x","proposed":"y"}`,
		``,                          // blank
		`{"id":"","file":"a.md"}`,   // no id
		`{"id":"s2","from_line":1}`, // no file
		`{not json`,                 // torn
		`{"id":"s3","file":"b.md","from_line":5,"to_line":6,"side":"old","original":"p","proposed":"q"}`,
	)
	got := loadSuggestions(path)
	if len(got) != 2 {
		t.Fatalf("want 2 valid suggestions, got %d: %+v", len(got), got)
	}
	if got[0].ID != "s1" || got[0].Side != "new" {
		t.Errorf("s1: want id=s1 side=new, got id=%s side=%s", got[0].ID, got[0].Side)
	}
	if got[1].ID != "s3" || got[1].Side != "old" {
		t.Errorf("s3: want id=s3 side=old, got id=%s side=%s", got[1].ID, got[1].Side)
	}
}

func TestLoadSuggestions_DedupLastWins(t *testing.T) {
	// Re-appending the same id revises that suggestion; first-seen order is kept so
	// the UI doesn't reshuffle.
	path := writeSuggestionsFile(t,
		`{"id":"s1","file":"a.md","from_line":1,"to_line":1,"proposed":"first"}`,
		`{"id":"s2","file":"a.md","from_line":2,"to_line":2,"proposed":"other"}`,
		`{"id":"s1","file":"a.md","from_line":1,"to_line":1,"proposed":"revised"}`,
	)
	got := loadSuggestions(path)
	if len(got) != 2 {
		t.Fatalf("want 2 deduped, got %d", len(got))
	}
	if got[0].ID != "s1" || got[0].ProposedText != "revised" {
		t.Errorf("dedup: want s1=revised in first slot, got %s=%q", got[0].ID, got[0].ProposedText)
	}
	if got[1].ID != "s2" {
		t.Errorf("order: want s2 second, got %s", got[1].ID)
	}
}

// ctrlWithSuggestions builds a controller whose store dir holds the given
// suggestions file lines, plus an empty comments CSV path in the same dir.
func ctrlWithSuggestions(t *testing.T, lines ...string) *PrereviewController {
	t.Helper()
	path := writeSuggestionsFile(t, lines...)
	csvPath := filepath.Join(filepath.Dir(path), "comments.csv")
	return &PrereviewController{CSVPath: csvPath}
}

func TestApplySuggestions_AnchorFromOriginal(t *testing.T) {
	// applySuggestions derives the drift anchor from the ORIGINAL text and takes
	// the span from its line count (so a wrong to_line hint is corrected).
	c := ctrlWithSuggestions(t,
		`{"id":"s1","file":"a.md","from_line":3,"to_line":99,"original":"alpha\nbeta","proposed":"z"}`,
	)
	var st PrereviewState
	c.applySuggestions(&st)
	if len(st.Suggestions) != 1 {
		t.Fatalf("want 1 suggestion, got %d", len(st.Suggestions))
	}
	s := st.Suggestions[0]
	if s.Anchor.Text != "alpha\nbeta" {
		t.Errorf("anchor text: want %q, got %q", "alpha\nbeta", s.Anchor.Text)
	}
	if s.FromLine != 3 || s.ToLine != 4 { // span=1 from the 2-line original
		t.Errorf("span from original: want [3,4], got [%d,%d]", s.FromLine, s.ToLine)
	}
}

func TestRelocateSuggestionsSelected_Drift(t *testing.T) {
	// A suggestion whose original text is present → ok; moved down → moved;
	// gone → outdated. Mirrors comment drift, sharing the same engine.
	c := ctrlWithSuggestions(t,
		`{"id":"ok","file":"a.md","from_line":2,"to_line":2,"original":"beta","proposed":"B"}`,
		`{"id":"moved","file":"a.md","from_line":1,"to_line":1,"original":"gamma","proposed":"G"}`,
		`{"id":"gone","file":"a.md","from_line":1,"to_line":1,"original":"vanished","proposed":"V"}`,
	)
	st := PrereviewState{SelectedFile: "a.md"}
	c.applySuggestions(&st)
	// Live file: "alpha, beta, delta, gamma" — beta still at 2, gamma moved to 4,
	// "vanished" absent.
	st.CurrentDiff = fd("alpha", "beta", "delta", "gamma")
	c.relocateSuggestionsSelected(&st)

	byID := map[string]Suggestion{}
	for _, s := range st.Suggestions {
		byID[s.ID] = s
	}
	if s := byID["ok"]; s.AnchorStatus != anchorOK || s.FromLine != 2 {
		t.Errorf("ok: want ok@2, got %s@%d", s.AnchorStatus, s.FromLine)
	}
	if s := byID["moved"]; s.AnchorStatus != anchorMoved || s.FromLine != 4 {
		t.Errorf("moved: want moved@4, got %s@%d", s.AnchorStatus, s.FromLine)
	}
	if s := byID["gone"]; !s.AnchorOutdated() {
		t.Errorf("gone: want outdated, got %s", s.AnchorStatus)
	}
}

func TestSuggestionViews_GroupingAndToggle(t *testing.T) {
	st := PrereviewState{
		SelectedFile: "a.md",
		Suggestions: []Suggestion{
			{ID: "s1", File: "a.md", FromLine: 2, ToLine: 2},
			{ID: "s2", File: "a.md", FromLine: 5, ToLine: 6},
			{ID: "other", File: "b.md", FromLine: 1, ToLine: 1}, // different file
		},
	}
	if got := st.SuggestionCount(); got != 2 {
		t.Errorf("SuggestionCount: want 2 (a.md only), got %d", got)
	}
	by := st.SuggestionsByEndLine()
	if len(by[2]) != 1 || len(by[6]) != 1 {
		t.Errorf("SuggestionsByEndLine: want 1 at L2 and 1 at L6, got %+v", by)
	}
	if len(st.FileSuggestions()) != 2 {
		t.Errorf("FileSuggestions: want 2, got %d", len(st.FileSuggestions()))
	}
	// HideSuggestions blanks every rendering surface but not the count (total
	// available drives the toggle label).
	st.HideSuggestions = true
	if st.SuggestionsByEndLine() != nil || st.FileSuggestions() != nil {
		t.Errorf("HideSuggestions should blank the render surfaces")
	}
	if st.SuggestionCount() != 2 {
		t.Errorf("SuggestionCount should ignore HideSuggestions, got %d", st.SuggestionCount())
	}
}

// fdWithChange builds a diff whose line 1 is an add (so CollapseToHunks folds)
// and lines 2..n are unchanged ctx — enough to exercise fold-vs-full-file.
func fdWithChange(n int) *gitdiff.FileDiff {
	d := &gitdiff.FileDiff{}
	d.Lines = append(d.Lines, gitdiff.DiffLine{OldNum: 0, NewNum: 1, Kind: "add", Content: "changed"})
	for i := 2; i <= n; i++ {
		d.Lines = append(d.Lines, gitdiff.DiffLine{OldNum: i, NewNum: i, Kind: "ctx", Content: "line"})
	}
	return d
}

func hasLine(lines []gitdiff.DiffLine, newNum int) bool {
	for _, l := range lines {
		if l.NewNum == newNum && l.Kind != "fold" {
			return true
		}
	}
	return false
}

func TestVisibleLines_SuggestionRevealsFoldedLine(t *testing.T) {
	// A suggestion on an unchanged line far from the change would be folded away in
	// diff view (hiding the box). With a visible suggestion on the file, the whole
	// file renders so the target line — and its box — is present. Toggling
	// suggestions off restores folding.
	st := PrereviewState{SelectedFile: "a.md", CurrentDiff: fdWithChange(12)}

	// Baseline: no suggestions → line 8 is folded (far from the line-1 change).
	if hasLine(st.VisibleLines(), 8) {
		t.Fatal("precondition: line 8 should be folded without a suggestion")
	}

	st.Suggestions = []Suggestion{{ID: "s1", File: "a.md", FromLine: 8, ToLine: 8}}
	if !hasLine(st.VisibleLines(), 8) {
		t.Error("a visible suggestion on line 8 must force the full file so the box renders")
	}

	// Hidden suggestions must NOT force full file (nothing to reveal).
	st.HideSuggestions = true
	if hasLine(st.VisibleLines(), 8) {
		t.Error("with suggestions hidden, line 8 should fold again")
	}
}

func TestSuggestionLineSpan(t *testing.T) {
	if s := (Suggestion{FromLine: 42, ToLine: 42}); s.LineSpan() != "L42" {
		t.Errorf("single-line span: want L42, got %s", s.LineSpan())
	}
	if s := (Suggestion{FromLine: 42, ToLine: 48}); s.LineSpan() != "L42-L48" {
		t.Errorf("range span: want L42-L48, got %s", s.LineSpan())
	}
}
