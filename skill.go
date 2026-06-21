package main

import (
	"os"
	"path/filepath"
)

// installSkill writes the embedded Claude Code skill files into
// <home>/.claude/skills/prereview/ and returns the SKILL.md path. It is the
// Claude-specific entry point used by --update's skill sync; the general
// multi-client installer is installClient (see clients.go). Overwrites
// existing files so re-running upgrades the skill. The filename is the
// case-sensitive uppercase SKILL.md on purpose — a lowercase skill.md is
// silently ignored by Claude Code, the exact trap this command exists to
// prevent users from hitting.
func installSkill(home string) (string, error) {
	// The "claude" target writes SKILL.md first, then reference.md.
	paths, err := installClient(home, "claude")
	if err != nil {
		return "", err
	}
	return paths[0], nil
}

// syncInstalledSkill refreshes the on-disk skill when it is already
// installed but its bytes differ from this binary's embedded copy. This is
// how `prereview --update` "also updates the skill": the running binary is
// the source of truth for its own embedded skill text, so a normal startup
// after any binary upgrade — self-update re-exec, Homebrew, Scoop, or
// `go install` — brings the skill back in sync. (Trying to write the new
// text from the old binary during `--update` itself can't work: the old
// binary still holds the old text.)
//
// It never *creates* the skill: a user who hasn't run --install-skill stays
// opted out. Returns true only when it rewrote the files.
func syncInstalledSkill(home string) (changed bool, err error) {
	dir := filepath.Join(home, ".claude", "skills", "prereview")
	skillPath := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		return false, nil // not installed → respect the opt-out
	}
	if skillFileCurrent(skillPath, skillMD) &&
		skillFileCurrent(filepath.Join(dir, "reference.md"), skillReferenceMD) {
		return false, nil // already in sync (the steady-state no-op)
	}
	if _, err := installSkill(home); err != nil {
		return false, err
	}
	return true, nil
}

// skillFileCurrent reports whether the file at path already holds want. A
// read error counts as "not current" so a deleted or unreadable companion
// file (e.g. reference.md) triggers a rewrite that restores it.
func skillFileCurrent(path, want string) bool {
	got, err := os.ReadFile(path)
	return err == nil && string(got) == want
}
