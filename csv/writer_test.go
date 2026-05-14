package csv

import (
	stdcsv "encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	r := stdcsv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	return rows
}

func TestWriter_HeaderAndRows(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(filepath.Join(dir, "comments.csv"))

	created := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	rows := []Row{
		{ID: "01A", File: "main.go", FromLine: 10, ToLine: 12, Side: "new", Body: "extract this", CreatedAt: created},
		{ID: "01B", File: "x.go", FromLine: 1, ToLine: 1, Side: "old", Body: "remove?", CreatedAt: created},
	}
	if err := w.Write(rows); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := readCSV(t, w.Path())
	want := [][]string{
		{"id", "file", "from_line", "to_line", "side", "body", "created_at", "resolved"},
		{"01A", "main.go", "10", "12", "new", "extract this", "2026-05-14T10:00:00Z", "false"},
		{"01B", "x.go", "1", "1", "old", "remove?", "2026-05-14T10:00:00Z", "false"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("row %d: got %d cols, want %d: %v", i, len(got[i]), len(want[i]), got[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Errorf("[%d][%d] = %q, want %q", i, j, got[i][j], want[i][j])
			}
		}
	}
}

func TestWriter_MultiLineBodyRFC4180(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(filepath.Join(dir, "comments.csv"))

	body := "first line\nsecond line\nthird, with comma\nfourth \"quoted\""
	if err := w.Write([]Row{{
		ID: "X", File: "f.go", FromLine: 1, ToLine: 1, Side: "new",
		Body: body, CreatedAt: time.Unix(0, 0).UTC(),
	}}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Re-parse and confirm the round-trip is identical — RFC-4180 quoting
	// must preserve newlines and embedded commas/quotes byte-for-byte.
	got := readCSV(t, w.Path())
	if len(got) != 2 {
		t.Fatalf("expected header + 1 row, got %d rows: %v", len(got), got)
	}
	if got[1][5] != body {
		t.Errorf("body round-trip mismatch:\ngot:  %q\nwant: %q", got[1][5], body)
	}
}

func TestWriter_RewriteReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "comments.csv")
	w := NewWriter(path)

	if err := w.Write([]Row{{ID: "A", File: "f", FromLine: 1, ToLine: 1, Side: "new", Body: "first", CreatedAt: time.Unix(0, 0)}}); err != nil {
		t.Fatalf("write 1: %v", err)
	}

	// Second write replaces — no stale "A" row.
	if err := w.Write([]Row{{ID: "B", File: "g", FromLine: 2, ToLine: 2, Side: "new", Body: "second", CreatedAt: time.Unix(0, 0)}}); err != nil {
		t.Fatalf("write 2: %v", err)
	}

	got := readCSV(t, path)
	if len(got) != 2 {
		t.Fatalf("expected header + 1 row, got %d: %v", len(got), got)
	}
	if got[1][0] != "B" {
		t.Errorf("expected ID B after rewrite, got %q", got[1][0])
	}
}

func TestWriter_EmptyRowsWritesHeaderOnly(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(filepath.Join(dir, "comments.csv"))
	if err := w.Write(nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readCSV(t, w.Path())
	if len(got) != 1 {
		t.Fatalf("expected header-only, got %d rows: %v", len(got), got)
	}
}

func TestWriter_AtomicWrite_NoTmpLeftBehind(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(filepath.Join(dir, "comments.csv"))
	if err := w.Write([]Row{{ID: "A", File: "f", FromLine: 1, ToLine: 1, Side: "new", Body: "ok", CreatedAt: time.Unix(0, 0)}}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// No .tmp file should survive a successful write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") || strings.Contains(e.Name(), ".prereview-csv-") {
			t.Errorf("tmp file survived write: %s", e.Name())
		}
	}
}

// TestWriter_ConcurrentWrites is a stress test for the mutex. We don't try
// to assert specific final content (whichever writer goes last wins); we
// only assert the file is always valid CSV with exactly one row of content,
// and that no .tmp file remains after the dust settles.
func TestWriter_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(filepath.Join(dir, "comments.csv"))
	const N = 32

	var wg sync.WaitGroup
	for i := range N {
		wg.Go(func() {
			_ = w.Write([]Row{{
				ID:        "id-" + string(rune('A'+(i%26))),
				File:      "f.go",
				FromLine:  i + 1,
				ToLine:    i + 1,
				Side:      "new",
				Body:      "concurrent",
				CreatedAt: time.Unix(0, 0),
			}})
		})
	}
	wg.Wait()

	got := readCSV(t, w.Path())
	if len(got) != 2 {
		t.Fatalf("expected header + 1 row, got %d: %v", len(got), got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".prereview-csv-") {
			t.Errorf("tmp file survived concurrent writes: %s", e.Name())
		}
	}
}
