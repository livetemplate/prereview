package skills

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/prereview/internal/library"
)

// An agent loads a skill only when it judges the task matches the skill's
// description, so new docs get written without the learned ones. Two pointers
// make it use them: a block in each agent's always-loaded memory file (this
// file), and a Claude Code hook that checks prose after every edit
// (claudehook.go). Sync writes both while a generated skill exists, and
// removes them when none does or you turn them off.

// Memory is an agent's global memory file: read at the start of every session.
type Memory struct {
	Label string
	File  string // relative to the agent home
	// SetUp is the path (relative to the agent home) whose existence means the
	// agent is set up here: its folder, which prereview never creates. For
	// opencode it is the file itself: opencode reads ~/.claude/CLAUDE.md only
	// while its own AGENTS.md is absent, so creating one would hide your
	// CLAUDE.md from it.
	SetUp string
}

// Memories are the memory files prereview adds its block to.
var Memories = []Memory{
	{Label: "Claude Code", File: ".claude/CLAUDE.md", SetUp: ".claude"},
	{Label: "Codex", File: ".codex/AGENTS.md", SetUp: ".codex"},
	{Label: "Gemini CLI", File: ".gemini/GEMINI.md", SetUp: ".gemini"},
	{Label: "opencode", File: ".config/opencode/AGENTS.md", SetUp: ".config/opencode/AGENTS.md"},
}

// The block's markers. Everything outside them is yours and never touched.
const (
	blockStart = "<!-- prereview:learned-skills -->"
	blockEnd   = "<!-- /prereview:learned-skills -->"
)

// MemoryBlock is the block that tells an agent to use the generated skills.
func MemoryBlock(gen []Skill) string {
	var b strings.Builder
	b.WriteString(blockStart + "\n")
	b.WriteString("The user's writing preferences, learned by prereview from their review comments, are skills. Before you write or edit prose (docs, plans, READMEs, release notes, commit and PR messages), load the matching one and follow it:\n\n")
	for _, s := range gen {
		if s.Extends == "" {
			fmt.Fprintf(&b, "- `%s`: any prose\n", s.ID)
		} else {
			fmt.Fprintf(&b, "- `%s`: %s (apply `%s` too)\n", s.ID, strings.ToLower(s.Name), s.Extends)
		}
	}
	b.WriteString("\nBefore you finish, run `prereview check <files you changed>` and fix what it reports. prereview keeps this block up to date: change the learnings in prereview (Library → Learnings), not this text.\n")
	b.WriteString(blockEnd + "\n")
	return b.String()
}

// Pointer states, beside the install states.
const (
	Added = "added"
	Off   = "off"
)

// PointerReport is where one pointer (a memory block or the hook) stands.
type PointerReport struct {
	Label, Path, State string
}

// findBlock locates the block in data: [start, end) spans the markers and the
// newline after the end marker; ok is false when there's no complete block.
func findBlock(data []byte) (start, end int, ok bool) {
	start = bytes.Index(data, []byte(blockStart))
	if start < 0 {
		return 0, 0, false
	}
	rel := bytes.Index(data[start:], []byte(blockEnd))
	if rel < 0 {
		return 0, 0, false
	}
	end = start + rel + len(blockEnd)
	if end < len(data) && data[end] == '\n' {
		end++
	}
	return start, end, true
}

// writeBlock puts block in the memory file at path, or removes it when block
// is "", and reports whether the file changed. A block edited since prereview
// wrote it is left (ErrEdited).
func (o *Owner) writeBlock(path, block string) (bool, error) {
	reg := o.Region(path, "learned-skills")
	cur, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	start, end, found := findBlock(cur)
	if found && reg.Edited(cur[start:end]) {
		return false, ErrEdited
	}
	var next []byte
	switch {
	case found && block == "":
		// Take the blank line writeBlock put before the block with it.
		pre := bytes.TrimRight(cur[:start], "\n")
		if len(pre) > 0 {
			pre = append(pre, '\n')
		}
		next = append(pre, cur[end:]...)
	case found:
		next = append(append(append([]byte{}, cur[:start]...), block...), cur[end:]...)
	case block == "":
		reg.Set("")
		return false, nil
	case len(cur) > 0:
		next = append(append(bytes.TrimRight(cur, "\n"), '\n', '\n'), block...)
	default:
		next = []byte(block)
	}
	reg.Set(block)
	switch {
	case bytes.Equal(next, cur):
		return false, nil
	case len(bytes.TrimSpace(next)) == 0: // nothing but the block was there
		return true, os.Remove(path)
	}
	return true, library.WriteFileAtomic(path, next, 0o644)
}

// syncMemories writes block (or removes it, when "") in every agent's memory
// file, where the agent is set up.
func syncMemories(o *Owner, home, block string) ([]PointerReport, []string, error) {
	var reps []PointerReport
	var changed []string
	for _, m := range Memories {
		path := filepath.Join(home, filepath.FromSlash(m.File))
		r := PointerReport{Label: m.Label + " memory", Path: path}
		if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(m.SetUp))); err != nil {
			if block != "" {
				r.State = NotSetUp
				reps = append(reps, r)
			}
			continue
		}
		ch, err := o.writeBlock(path, block)
		switch {
		case errors.Is(err, ErrEdited):
			r.State = EditedThere
		case err != nil:
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		case block == "":
			r.State = Off
		default:
			r.State = Added
		}
		if ch {
			changed = append(changed, path)
		}
		reps = append(reps, r)
	}
	return reps, changed, nil
}
