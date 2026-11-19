package skills

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
)

// Target is a place an agent loads skills from, under the agent home.
type Target struct {
	ID, Label string
	Root      string // relative to the agent home
	Cursor    bool   // a Cursor rule (.mdc) rather than a SKILL.md folder
}

// Targets are where generated skills are installed, when the agent is set up
// (its skills root exists). ~/.agents/skills is read by Codex, Gemini CLI and
// opencode; opencode also reads ~/.claude/skills (a skill's ID is its folder,
// so the two copies are one skill there). These are skill folders, unlike most
// of clientTargets()' rows (slash commands, a script), so they're their own list.
var Targets = []Target{
	{ID: "claude", Label: "Claude Code", Root: ".claude/skills"},
	{ID: "agents", Label: "Codex · Gemini · opencode", Root: ".agents/skills"},
	{ID: "codex", Label: "Codex (older)", Root: ".codex/skills"},
	{ID: "cursor", Label: "Cursor", Root: ".cursor/rules", Cursor: true},
}

// Path is where skill id is installed for this target.
func (t Target) Path(home, id string) string {
	if t.Cursor {
		return filepath.Join(home, t.Root, id+".mdc")
	}
	return filepath.Join(home, t.Root, id, "SKILL.md")
}

// Env is where a sync reads and writes.
type Env struct {
	LibDir string // the per-user library (skill sources, defs, ownership)
	Home   string // the agent home (AgentHome)
	// NoPointers: you turned off "Tell agents to use these skills", so no
	// memory block or hook is written (and any there are removed).
	NoPointers bool
}

// SourcePath is a generated skill's source in the library.
func (e Env) SourcePath(id string) string { return filepath.Join(e.skillsDir(), id, "SKILL.md") }

// LearnedDir is the folder of checks your learnings (and imported skills)
// produce — loaded as checks.SourceLearned, so they can never run a command.
func (e Env) LearnedDir() string { return LearnedChecksDir(e.LibDir) }

// LearnedChecksDir is a library's learned-checks folder.
func LearnedChecksDir(lib string) string {
	if lib == "" {
		return ""
	}
	return filepath.Join(lib, "skills", "checks")
}

func (e Env) skillsDir() string { return filepath.Join(e.LibDir, "skills") }

// generated reports a path only a sync writes: a skill source or learned check
// in the library, or a learned-* install in an agent's folder. The sweep never
// removes anything else the ownership record may hold.
func (e Env) generated(p string) bool {
	if within(e.skillsDir(), p) {
		return true
	}
	for _, t := range Targets {
		root := filepath.Join(e.Home, t.Root)
		if !within(root, p) {
			continue
		}
		first, _, _ := strings.Cut(filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator))), "/")
		if strings.HasPrefix(first, IDPrefix) { // learned-writing/SKILL.md or learned-writing.mdc
			return true
		}
	}
	return false
}

// Install states.
const (
	Installed   = "installed"
	NotSetUp    = "not set up here"
	EditedThere = "edited there — not updated"
	Foreign     = "another skill has that name there"
)

// SkillReport is one generated skill after a sync.
type SkillReport struct {
	Skill
	Source   string
	Edited   bool // you edited the source: prereview stopped regenerating it
	Installs []InstallReport
}

// InstallReport is where a skill is (or isn't) installed.
type InstallReport struct {
	Target Target
	State  string
}

// ImportReport is an imported skill after a sync: the checks its evals gave,
// the graders that didn't translate, and whether it changed since you imported it.
type ImportReport struct {
	Import
	Checks  []Check
	Skipped []SkippedGrader
	Changed bool
}

// Report is what a sync found and did.
type Report struct {
	Skills  []SkillReport
	Imports []ImportReport
	// Pointers are the memory blocks and the hook that tell agents to use the
	// skills (memory.go, claudehook.go).
	Pointers []PointerReport
	Changed  []string // paths written or removed
}

// defsFile holds the scope skills you created and the scope proposals you
// dismissed.
const defsFile = "defs.json"

type defs struct {
	Skills    []Def    `json:"skills"`
	Dismissed []string `json:"dismissed,omitempty"`
}

// CreateSkill adds a skill for scope (the Skills tab's "Create skill").
func CreateSkill(lib, scope, name string) error {
	return editJSON(lib, defsFile, func(d *defs) (bool, error) {
		id := SlugID(scope)
		if slices.ContainsFunc(d.Skills, func(s Def) bool { return s.Scope == scope || s.ID == id }) {
			return false, nil
		}
		d.Skills = append(d.Skills, Def{ID: id, Name: name, Scope: scope})
		return true, nil
	})
}

// DismissScope stops proposing a skill for scope ("Not now").
func DismissScope(lib, scope string) error {
	return editJSON(lib, defsFile, func(d *defs) (bool, error) {
		if slices.Contains(d.Dismissed, scope) {
			return false, nil
		}
		d.Dismissed = append(d.Dismissed, scope)
		return true, nil
	})
}

// generate is the skills the library's learnings make (overlays excluded).
func generate(lib string, ls []library.Learning, imports []Import) []Skill {
	return Generate(withoutOverlays(ls, imports), readJSON[defs](lib, defsFile).Skills)
}

// Sync makes disk match the accepted learnings: each generated skill's source in
// the library, its install in every agent that's set up, and the checkers and
// judges its learnings (and imported skills' evals) produce in the learned-checks
// folder. It writes only what differs and removes what's no longer wanted — only
// files it generates, that nobody changed. Idempotent: calling it again with no
// change does nothing, so every server can run it after any library change.
func Sync(env Env, ls []library.Learning) (Report, error) {
	var rep Report
	imports := Imports(env.LibDir)
	gen := generate(env.LibDir, ls, imports)
	err := Own(env.LibDir, func(o *Owner) error {
		want := map[string]bool{}
		// keep records a write's outcome: kept (and changed?) unless it's fatal.
		keep := func(p string, changed bool, err error) error {
			switch {
			case err == nil, errors.Is(err, ErrEdited), errors.Is(err, ErrForeign):
				want[p] = true
				if changed {
					rep.Changed = append(rep.Changed, p)
				}
			case errors.Is(err, ErrNoRoot):
			default:
				return err
			}
			return nil
		}
		var checksToWrite []Check
		for _, s := range gen {
			sr := SkillReport{Skill: s, Source: env.SourcePath(s.ID)}
			src := []byte(s.SkillMD())
			changed, err := o.WriteIn(env.skillsDir(), sr.Source, src)
			if errors.Is(err, ErrEdited) {
				sr.Edited = true
				src, _ = os.ReadFile(sr.Source) // installs follow your edit
			}
			if err := keep(sr.Source, changed, err); err != nil {
				return err
			}
			for _, t := range Targets {
				body := src
				if t.Cursor {
					body = []byte(cursorFrom(s, src))
				}
				p := t.Path(env.Home, s.ID)
				changed, err := o.Write(filepath.Join(env.Home, t.Root), p, body)
				if err := keep(p, changed, err); err != nil {
					return err
				}
				sr.Installs = append(sr.Installs, InstallReport{Target: t, State: installState(err)})
			}
			checksToWrite = append(checksToWrite, s.Checks...)
			rep.Skills = append(rep.Skills, sr)
		}
		for _, imp := range imports {
			cs, skipped := ImportedChecks(imp)
			rep.Imports = append(rep.Imports, ImportReport{Import: imp, Checks: cs, Skipped: skipped, Changed: imp.Changed()})
			checksToWrite = append(checksToWrite, cs...)
		}
		for _, c := range checksToWrite {
			p := filepath.Join(env.LearnedDir(), c.File())
			changed, err := o.WriteIn(env.LearnedDir(), p, []byte(c.Body))
			if err := keep(p, changed, err); err != nil {
				return err
			}
		}
		block := ""
		if !env.NoPointers && len(gen) > 0 {
			block = MemoryBlock(gen)
		}
		mems, changed, err := syncMemories(o, env.Home, block)
		if err != nil {
			return err
		}
		rep.Pointers, rep.Changed = append(rep.Pointers, mems...), append(rep.Changed, changed...)
		hook, changed, err := syncHook(o, env.Home, block != "")
		if err != nil {
			return err
		}
		if hook != nil {
			rep.Pointers = append(rep.Pointers, *hook)
		}
		rep.Changed = append(rep.Changed, changed...)
		for p := range o.hashes { // what nothing asks for any more
			if want[p] || !env.generated(p) {
				continue
			}
			if removed, err := o.Remove(p); err == nil && removed {
				rep.Changed = append(rep.Changed, p)
			}
		}
		return nil
	})
	return rep, err
}

func installState(err error) string {
	switch {
	case errors.Is(err, ErrNoRoot):
		return NotSetUp
	case errors.Is(err, ErrEdited):
		return EditedThere
	case errors.Is(err, ErrForeign):
		return Foreign
	}
	return Installed
}

// Regenerate discards your edit to a generated skill's source and writes it
// from the learnings again (the next Sync installs it).
func Regenerate(env Env, ls []library.Learning, id string) error {
	for _, s := range generate(env.LibDir, ls, Imports(env.LibDir)) {
		if s.ID == id {
			return Own(env.LibDir, func(o *Owner) error { return o.Reclaim(env.SourcePath(id), []byte(s.SkillMD())) })
		}
	}
	return errors.New("no generated skill " + id)
}

// cursorFrom renders the Cursor rule from the skill's source (generated or your
// edit): its body after the SKILL.md frontmatter, under Cursor's frontmatter.
func cursorFrom(s Skill, src []byte) string {
	if _, body, ok := checks.SplitFrontmatter(src); ok {
		src = body
	}
	return s.cursorFrontmatter() + strings.TrimLeft(string(src), "\n")
}
