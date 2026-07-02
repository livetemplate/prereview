package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/review"
)

// TestRunProcessed_AppendsMarks verifies the subcommand writes one JSONL line per
// id into <out>/.prereview/processed.jsonl and APPENDS on subsequent calls (never
// rewrites — the append-only contract that keeps it off the server's CSV rail).
func TestRunProcessed_AppendsMarks(t *testing.T) {
	dir := t.TempDir()
	if err := runProcessed([]string{"--out", dir, "id1", "id2"}); err != nil {
		t.Fatalf("runProcessed: %v", err)
	}
	path := filepath.Join(dir, ".prereview", review.ProcessedFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read markers: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), data)
	}
	if !strings.Contains(lines[0], `"id":"id1"`) || !strings.Contains(lines[1], `"id":"id2"`) {
		t.Errorf("markers missing ids: %q", data)
	}

	// Append-only: a second call adds a line, never rewrites.
	if err := runProcessed([]string{"--out", dir, "id3"}); err != nil {
		t.Fatalf("runProcessed 2: %v", err)
	}
	data2, _ := os.ReadFile(path)
	if n := len(strings.Split(strings.TrimSpace(string(data2)), "\n")); n != 3 {
		t.Fatalf("want 3 lines after append, got %d: %q", n, data2)
	}
}

// TestRunProcessed_NoIDs is the error path: a mark with no id is a usage error.
func TestRunProcessed_NoIDs(t *testing.T) {
	if err := runProcessed([]string{"--out", t.TempDir()}); err == nil {
		t.Error("expected error when no ids given")
	}
}
