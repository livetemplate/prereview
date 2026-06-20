package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// installSkill writes the embedded skill files into
// <home>/.claude/skills/prereview/ and returns the SKILL.md path.
// Overwrites existing files so re-running upgrades the skill. The
// filename is the case-sensitive uppercase SKILL.md on purpose — a
// lowercase skill.md is silently ignored by Claude Code, the exact
// trap this command exists to prevent users from hitting.
func installSkill(home string) (string, error) {
	dir := filepath.Join(home, ".claude", "skills", "prereview")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	skillPath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(skillMD), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", skillPath, err)
	}
	refPath := filepath.Join(dir, "reference.md")
	if err := os.WriteFile(refPath, []byte(skillReferenceMD), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", refPath, err)
	}
	return skillPath, nil
}
