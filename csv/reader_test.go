package csv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRead_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "comments.csv")
	w := NewWriter(path)

	created := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	originals := []Row{
		{ID: "A", File: "main.go", FromLine: 10, ToLine: 12, Side: "new", Body: "extract this\nto a helper", CreatedAt: created},
		{ID: "B", File: "x.go", FromLine: 1, ToLine: 1, Side: "old", Body: "remove?", CreatedAt: created.Add(time.Minute)},
	}
	if err := w.Write(originals); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(originals) {
		t.Fatalf("got %d rows, want %d", len(got), len(originals))
	}
	for i, want := range originals {
		if got[i].ID != want.ID || got[i].File != want.File ||
			got[i].FromLine != want.FromLine || got[i].ToLine != want.ToLine ||
			got[i].Side != want.Side || got[i].Body != want.Body ||
			!got[i].CreatedAt.Equal(want.CreatedAt) {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want)
		}
	}
}

func TestRead_NonexistentFile(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "missing.csv"))
	if err != nil {
		t.Errorf("expected nil error for missing file, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil rows, got %v", got)
	}
}

func TestRead_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.csv")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Errorf("expected nil error for empty file, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 rows, got %d", len(got))
	}
}

func TestRead_HeaderOnly(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(filepath.Join(dir, "comments.csv"))
	if err := w.Write(nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Read(w.Path())
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 rows for header-only file, got %d", len(got))
	}
}

func TestRead_SkipsMalformedRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "comments.csv")
	content := strings.Join([]string{
		"id,file,from_line,to_line,side,body,created_at",
		"good,a.go,1,1,new,first,2026-05-14T10:00:00Z",
		"bad,a.go,not-a-number,1,new,second,2026-05-14T10:00:00Z", // bad from_line
		"good2,b.go,2,2,new,third,2026-05-14T10:00:00Z",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows (malformed skipped), got %d: %+v", len(got), got)
	}
	if got[0].ID != "good" || got[1].ID != "good2" {
		t.Errorf("got IDs %s, %s — want 'good', 'good2'", got[0].ID, got[1].ID)
	}
}

// TestRead_BackCompatColumnCounts pins that 7/8/9/10-column rows all
// load with correct defaults so older CSVs round-trip after the anchor
// migration (no false "outdated" on pre-migration data — empty status).
func TestRead_BackCompatColumnCounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "comments.csv")
	content := strings.Join([]string{
		"id,file,from_line,to_line,side,body,created_at,resolved,anchor,anchor_status,kind",
		`c7,a.go,1,1,new,seven,2026-05-14T10:00:00Z`,                                    // 7 cols
		`c8,a.go,2,2,new,eight,2026-05-14T10:00:00Z,true`,                               // 8 cols
		`c9,a.go,3,3,new,nine,2026-05-14T10:00:00Z,false,"{""text"":""x""}"`,            // 9 cols
		`c10,a.go,4,4,new,ten,2026-05-14T10:00:00Z,true,"{""text"":""y""}",moved`,       // 10 cols
		`c11,a.go,0,0,,eleven,2026-05-14T10:00:00Z,false,,,file`,                        // 11 cols (file-level)
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(got), got)
	}
	// 7-col: resolved false, no anchor, no status, no kind.
	if got[0].Resolved || got[0].Anchor != "" || got[0].AnchorStatus != "" || got[0].Kind != "" {
		t.Errorf("7-col defaults wrong: %+v", got[0])
	}
	// 8-col: resolved parsed, still no anchor/status/kind.
	if !got[1].Resolved || got[1].Anchor != "" || got[1].AnchorStatus != "" || got[1].Kind != "" {
		t.Errorf("8-col defaults wrong: %+v", got[1])
	}
	// 9-col: anchor present, status/kind still empty.
	if got[2].Anchor != `{"text":"x"}` || got[2].AnchorStatus != "" || got[2].Kind != "" {
		t.Errorf("9-col anchor/status/kind wrong: %+v", got[2])
	}
	// 10-col: full anchor + status, no kind (legacy line-only rows).
	if !got[3].Resolved || got[3].Anchor != `{"text":"y"}` || got[3].AnchorStatus != "moved" || got[3].Kind != "" {
		t.Errorf("10-col full row wrong: %+v", got[3])
	}
	// 11-col: file-level — kind="file", line/side/anchor empty.
	if got[4].Kind != "file" || got[4].FromLine != 0 || got[4].ToLine != 0 || got[4].Side != "" || got[4].Anchor != "" {
		t.Errorf("11-col file-level row wrong: %+v", got[4])
	}
}

// TestRead_AnchorRoundTrip pins that an anchor JSON blob containing the
// CSV-hostile characters (newline, comma, double-quote) survives a
// Writer→Read round-trip byte-for-byte alongside the status column.
func TestRead_AnchorRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(filepath.Join(dir, "comments.csv"))
	anchor := `{"text":"line one\nline, two \"q\"","hash":"abc"}`
	in := []Row{{
		ID: "A", File: "d.md", FromLine: 5, ToLine: 7, Side: "new",
		Body: "b", CreatedAt: time.Unix(0, 0).UTC(),
		Anchor: anchor, AnchorStatus: "outdated",
	}}
	if err := w.Write(in); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Read(w.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].Anchor != anchor {
		t.Errorf("anchor round-trip mismatch:\ngot:  %q\nwant: %q", got[0].Anchor, anchor)
	}
	if got[0].AnchorStatus != "outdated" {
		t.Errorf("anchor_status = %q, want outdated", got[0].AnchorStatus)
	}
}
