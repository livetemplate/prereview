package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/review"
)

func TestParseSuggestionsInput_Forms(t *testing.T) {
	obj := `{"id":"s1","file":"a.md","from_line":2,"to_line":3,"original":"o","proposed":"p"}`
	obj2 := `{"id":"s2","file":"b.md","from_line":5,"original":"q","proposed":"r"}`

	cases := map[string]string{
		"single object": obj,
		"json array":    "[" + obj + "," + obj2 + "]",
		"jsonl":         obj + "\n" + obj2,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parseSuggestionsInput([]byte(payload))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(got) == 0 {
				t.Fatal("want >=1 suggestion")
			}
			if got[0].ID != "s1" || got[0].File != "a.md" || got[0].Side != "new" {
				t.Errorf("first: got %+v", got[0])
			}
		})
	}
}

func TestParseSuggestionsInput_Defaults(t *testing.T) {
	// Missing to_line collapses to from_line; missing id is generated; missing side
	// defaults to "new".
	got, err := parseSuggestionsInput([]byte(`{"file":"a.md","from_line":7,"proposed":"x"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := got[0]
	if s.ToLine != 7 {
		t.Errorf("to_line default: want 7, got %d", s.ToLine)
	}
	if s.ID == "" {
		t.Error("id should be generated when omitted")
	}
	if s.Side != "new" {
		t.Errorf("side default: want new, got %s", s.Side)
	}
}

func TestParseSuggestionsInput_Validation(t *testing.T) {
	bad := []string{
		``,                               // empty
		`{"from_line":1,"proposed":"x"}`, // missing file
		`{"file":"a.md","from_line":0}`,  // from_line < 1
	}
	for _, payload := range bad {
		if _, err := parseSuggestionsInput([]byte(payload)); err == nil {
			t.Errorf("want error for %q", payload)
		}
	}
}

func TestRunSuggest_AppendsJSONL(t *testing.T) {
	// End-to-end for the subcommand: a payload file is normalized and appended to
	// <out>/.prereview/suggestions.jsonl, and loadSuggestions reads it back.
	dir := t.TempDir()
	payload := filepath.Join(dir, "payload.json")
	if err := os.WriteFile(payload, []byte(`[
	  {"id":"s1","file":"a.md","from_line":1,"to_line":1,"original":"o1","proposed":"p1"},
	  {"file":"b.md","from_line":2,"original":"o2","proposed":"p2"}
	]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runSuggest([]string{"--out", dir, "--file", payload}); err != nil {
		t.Fatalf("runSuggest: %v", err)
	}
	got := loadSuggestionsFromDir(t, dir)
	if len(got) != 2 {
		t.Fatalf("want 2 appended, got %d", len(got))
	}
	// A second run appends (append-only) — the same ids revise, new ids add.
	if err := runSuggest([]string{"--out", dir, "--file", payload}); err != nil {
		t.Fatalf("runSuggest 2: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".prereview", review.SuggestionFileName))
	if n := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; n != 4 {
		t.Errorf("append-only: want 4 lines after two runs, got %d", n)
	}
}

// loadSuggestionsFromDir reads the suggestions written under dir/.prereview via
// the exported path helper, mirroring how the server loads them.
func loadSuggestionsFromDir(t *testing.T, dir string) []review.Suggestion {
	t.Helper()
	// The loader is unexported (internal/review); re-parse the raw file the same
	// tolerant way the CLI wrote it so the test stays in the main package.
	raw, err := os.ReadFile(filepath.Join(dir, ".prereview", review.SuggestionFileName))
	if err != nil {
		t.Fatalf("read suggestions: %v", err)
	}
	out, err := parseSuggestionsInput(raw)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	return out
}
