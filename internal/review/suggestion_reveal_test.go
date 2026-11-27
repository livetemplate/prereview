package review

import (
	"encoding/json"
	"os"
	"testing"
)

// writeSugs writes JSONL suggestion lines to this controller's suggestions file.
func writeSugs(t *testing.T, c *PrereviewController, lines ...string) {
	t.Helper()
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(c.suggestionsPath(), []byte(body), 0o644); err != nil {
		t.Fatalf("write suggestions: %v", err)
	}
}

// TestLLMStatusChanged_RevealsOnNewSuggestion (#116): a brand-new suggestion
// arriving while the inline boxes are toggled off must reveal them — a hidden
// toggle can't be allowed to silently swallow fresh proposals.
func TestLLMStatusChanged_RevealsOnNewSuggestion(t *testing.T) {
	c, _ := newStatusController(t)
	// Prior render had one suggestion loaded; the reviewer then toggled hide on.
	prior := PrereviewState{
		AnnFilters:  map[string]AnnFilter{"x.go": AnnFilter{Off: []string{navSuggestion}}},
		Suggestions: []Suggestion{{ID: "a", File: "x.go"}},
	}
	// The agent appends a NEW suggestion "b" and pings status.
	writeSugs(t, c,
		`{"id":"a","file":"x.go","from_line":1,"original":"o","proposed":"p"}`,
		`{"id":"b","file":"x.go","from_line":2,"original":"o2","proposed":"p2"}`,
	)
	st, _ := c.LLMStatusChanged(prior, nil)
	if !st.annFilter("x.go").kindOn(navSuggestion) {
		t.Error("a new suggestion must auto-reveal the hidden inline boxes (#116)")
	}
}

// TestLLMStatusChanged_StaysHiddenOnUnrelatedTick is the negative that actually
// guards the feature: LLMStatusChanged also fans out on plain status and
// processed-marker changes. A tick that adds NO new suggestion must leave the
// reviewer's hide toggle exactly as they set it.
func TestLLMStatusChanged_StaysHiddenOnUnrelatedTick(t *testing.T) {
	c, sp := newStatusController(t)
	prior := PrereviewState{
		AnnFilters:  map[string]AnnFilter{"x.go": AnnFilter{Off: []string{navSuggestion}}},
		Suggestions: []Suggestion{{ID: "a", File: "x.go"}},
	}
	// Same suggestion set on disk; only the status file changed (working→done).
	writeSugs(t, c, `{"id":"a","file":"x.go","from_line":1,"original":"o","proposed":"p"}`)
	if err := os.WriteFile(sp, []byte(`{"state":"done"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := c.LLMStatusChanged(prior, nil)
	if st.annFilter("x.go").kindOn(navSuggestion) {
		t.Error("an unrelated status/processed tick must NOT flip the hide toggle off")
	}
}

// TestLLMStatusChanged_StaysHiddenOnRevision: a revision re-appends the SAME id
// with new content — that's not a new proposal, so it must not force-reveal
// (reveal-on-content-change is a hide-feature concern, deliberately not #116).
func TestLLMStatusChanged_StaysHiddenOnRevision(t *testing.T) {
	c, _ := newStatusController(t)
	prior := PrereviewState{
		AnnFilters:  map[string]AnnFilter{"x.go": AnnFilter{Off: []string{navSuggestion}}},
		Suggestions: []Suggestion{{ID: "a", File: "x.go"}},
	}
	// Same id "a", different proposed text (a revision).
	writeSugs(t, c, `{"id":"a","file":"x.go","from_line":1,"original":"o","proposed":"REVISED"}`)
	st, _ := c.LLMStatusChanged(prior, nil)
	if st.annFilter("x.go").kindOn(navSuggestion) {
		t.Error("a same-id revision must not force-reveal (new-ID only, #116)")
	}
}

// A brand-new question set turns Questions back on in its file's filter, like a
// new suggestion (#116); an unrelated tick leaves the reviewer's setting alone.
func TestLLMStatusChanged_RevealsQuestionsOnNewSet(t *testing.T) {
	c, _ := newStatusController(t)
	set := validSet()
	// A fresh map per call: the handler edits the state's map in place.
	off := func() map[string]AnnFilter { return map[string]AnnFilter{set.File: {Off: []string{navQuestion}}} }

	st, _ := c.LLMStatusChanged(PrereviewState{AnnFilters: off()}, nil)
	if st.annFilter(set.File).kindOn(navQuestion) {
		t.Fatal("no question set arrived, so the filter must stay as set")
	}

	line, _ := json.Marshal(set)
	if err := os.WriteFile(QuestionsPath(c.CSVPath), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ = c.LLMStatusChanged(PrereviewState{AnnFilters: off()}, nil)
	if !st.annFilter(set.File).kindOn(navQuestion) {
		t.Error("a new question set must turn Questions back on in its file's filter")
	}

	st, _ = c.LLMStatusChanged(PrereviewState{AnnFilters: off(), QuestionSets: st.QuestionSets}, nil)
	if st.annFilter(set.File).kindOn(navQuestion) {
		t.Error("the same set again is not new: the filter must stay off")
	}
}
