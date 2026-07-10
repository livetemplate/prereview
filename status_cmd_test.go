package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/livetemplate/prereview/internal/review"
)

func readStatus(t *testing.T, dir string) review.LLMStatus {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".prereview", review.LLMStatusFileName))
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	var s review.LLMStatus
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse status: %v", err)
	}
	return s
}

func TestRunStatus(t *testing.T) {
	dir := t.TempDir()

	// working + multi-word message (joined).
	if err := runStatus([]string{"--out", dir, "working", "Applying", "your", "review"}); err != nil {
		t.Fatalf("runStatus working: %v", err)
	}
	if s := readStatus(t, dir); s.State != "working" || s.Message != "Applying your review" {
		t.Errorf("got %+v, want working / \"Applying your review\"", s)
	}

	// done with no message (message omitted from JSON).
	if err := runStatus([]string{"--out", dir, "done"}); err != nil {
		t.Fatalf("runStatus done: %v", err)
	}
	if s := readStatus(t, dir); s.State != "done" || s.Message != "" {
		t.Errorf("got %+v, want done / empty message", s)
	}

	// an unknown state is rejected.
	if err := runStatus([]string{"--out", dir, "bogus"}); err == nil {
		t.Error("expected error for unknown state")
	}
	// no state is a usage error.
	if err := runStatus([]string{"--out", dir}); err == nil {
		t.Error("expected error when no state given")
	}
}
