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
