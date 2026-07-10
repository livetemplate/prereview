package review

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/livetemplate/prereview/csv"
)

// TestLoadComments_ActionableVsAll pins that LoadComments filters exactly like
// the snapshot (actionable = unresolved, non-outdated, non-draft) and
// that --all returns every row.
func TestLoadComments_ActionableVsAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CommentsFileName)
	w := csv.NewWriter(path)
	rows := []csv.Row{
		{ID: "c1", File: "a.go", FromLine: 1, ToLine: 1, Side: "new", Body: "open", CreatedAt: time.Unix(0, 0)},
		{ID: "c2", File: "a.go", FromLine: 2, ToLine: 2, Side: "new", Body: "resolved", CreatedAt: time.Unix(0, 0), Resolved: true},
		{ID: "c3", File: "a.go", FromLine: 3, ToLine: 3, Side: "new", Body: "outdated", CreatedAt: time.Unix(0, 0), AnchorStatus: "outdated"},
		{ID: "c4", File: "a.go", FromLine: 4, ToLine: 4, Side: "new", Body: "draft", CreatedAt: time.Unix(0, 0), Draft: true},
	}
	if err := w.Write(rows); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	act, err := LoadComments(path, false)
	if err != nil {
		t.Fatalf("LoadComments(actionable): %v", err)
	}
	if len(act) != 1 || act[0].ID != "c1" {
		t.Fatalf("actionable = %v, want [c1]", ids(act))
	}

	all, err := LoadComments(path, true)
	if err != nil {
		t.Fatalf("LoadComments(all): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("all = %v, want 4 comments", ids(all))
	}

	// The JSON shape must match the stream handoff contract (snake_case keys).
	b, err := json.Marshal(act[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"id"`, `"file"`, `"from_line"`, `"side"`, `"body"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("StreamComment JSON missing %s: %s", key, b)
		}
	}
}

// TestLoadComments_MissingFileIsEmptyNotNil: a missing CSV yields a non-nil
// empty slice so `prereview comments --json` prints `[]`, never `null`.
func TestLoadComments_MissingFileIsEmptyNotNil(t *testing.T) {
	got, err := LoadComments(filepath.Join(t.TempDir(), "nope.csv"), false)
	if err != nil {
		t.Fatalf("LoadComments(missing): %v", err)
	}
	if got == nil {
		t.Fatal("LoadComments on a missing file returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", ids(got))
	}
}

func ids(cs []StreamComment) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}
