package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallSkill(t *testing.T) {
	home := t.TempDir()

	path, err := installSkill(home)
	if err != nil {
		t.Fatalf("installSkill: %v", err)
	}

	wantPath := filepath.Join(home, ".claude", "skills", "prereview", "SKILL.md")
	if path != wantPath {
		t.Errorf("returned path = %q, want %q", path, wantPath)
	}

	// The filename must be exactly uppercase SKILL.md — a lowercase
	// skill.md is silently ignored by Claude Code (the trap this
	// command exists to prevent).
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("SKILL.md not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "prereview", "skill.md")); !os.IsNotExist(err) {
		t.Errorf("a lowercase skill.md must NOT be created (got err=%v)", err)
	}

	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if string(got) != skillMD {
		t.Errorf("SKILL.md content doesn't match the embedded skill")
	}

	ref, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "prereview", "reference.md"))
	if err != nil {
		t.Fatalf("read reference.md: %v", err)
	}
	if string(ref) != skillReferenceMD {
		t.Errorf("reference.md content doesn't match the embedded reference")
	}

	// Idempotent: re-running overwrites cleanly (skill upgrade path).
	if _, err := installSkill(home); err != nil {
		t.Fatalf("installSkill second run: %v", err)
	}
}
