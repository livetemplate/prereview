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
	ID, Label string
	Dir       string // relative to the agent home; never created
	File      string // relative to the agent home
	// Existing: only add the block to a file that's already there. opencode
	// reads ~/.claude/CLAUDE.md only while its own AGENTS.md is absent, so
	// creating one would hide your CLAUDE.md from it.
	Existing bool
}

// Memories are the memory files prereview adds its block to.
var Memories = []Memory{
	{ID: "claude", Label: "Claude Code", Dir: ".claude", File: ".claude/CLAUDE.md"},
	{ID: "codex", Label: "Codex", Dir: ".codex", File: ".codex/AGENTS.md"},
	{ID: "gemini", Label: "Gemini CLI", Dir: ".gemini", File: ".gemini/GEMINI.md"},
	{ID: "opencode", Label: "opencode", Dir: ".config/opencode", File: ".config/opencode/AGENTS.md", Existing: true},
}

// The block's markers. Everything outside them is yours and never touched.
const (
	blockStart = "<!-- prereview:learned-skills -->"
	blockEnd   = "<!-- /prereview:learned-skills -->"
	blockKey   = "#learned-skills" // the ownership-record key suffix
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
// is "". It reports whether the file changed. A block edited since prereview
// wrote it is left (ErrEdited); a block with no record (an earlier install, a
// lost record) carries prereview's own markers, so it is taken over.
func (o *Owner) writeBlock(path, block string, mustExist bool) (bool, error) {
	key := path + blockKey
	cur, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if block == "" || mustExist {
			o.record(key, "")
			return false, nil
		}
		cur = nil
	case err != nil:
		return false, err
	}
	start, end, found := findBlock(cur)
	if found {
		if want, ok := o.hashes[key]; ok && want != hash(cur[start:end]) {
			return false, ErrEdited
		}
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
		o.record(key, "")
		return false, nil
	default:
		next = append([]byte{}, cur...)
		if len(next) > 0 {
			next = append(bytes.TrimRight(next, "\n"), '\n', '\n')
		}
		next = append(next, block...)
	}
	o.record(key, block)
	if bytes.Equal(next, cur) {
		return false, nil
	}
	if len(bytes.TrimSpace(next)) == 0 { // nothing but the block was there
		return true, os.Remove(path)
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	return true, library.WriteFileAtomic(path, next, mode)
}

// syncMemories writes block (or removes it, when "") in every agent's memory
// file whose folder exists.
func syncMemories(o *Owner, home, block string) ([]PointerReport, []string, error) {
	var reps []PointerReport
	var changed []string
	for _, m := range Memories {
		path := filepath.Join(home, filepath.FromSlash(m.File))
		r := PointerReport{Label: m.Label, Path: path}
		if fi, err := os.Stat(filepath.Join(home, filepath.FromSlash(m.Dir))); err != nil || !fi.IsDir() {
			r.State = NotSetUp
			if block == "" {
				continue
			}
			reps = append(reps, r)
			continue
		}
		ch, err := o.writeBlock(path, block, m.Existing)
		switch {
		case errors.Is(err, ErrEdited):
			r.State = EditedThere
		case err != nil:
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		case block == "":
			r.State = Off
		case m.Existing && !fileExists(path):
			r.State = NotSetUp
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

// record sets the ownership hash for a region key ("" forgets it), marking the
// record dirty only when that changes it.
func (o *Owner) record(key, data string) {
	old, had := o.hashes[key]
	switch {
	case data == "" && had:
		delete(o.hashes, key)
	case data == "" || (had && old == hash([]byte(data))):
		return
	default:
		o.hashes[key] = hash([]byte(data))
	}
	o.dirty = true
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
