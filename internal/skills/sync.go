package skills

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
// so the two copies are one skill there).
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
	LibDir    string // the per-user library (skill sources, defs, ownership)
	ChecksDir string // your checks folder (derived checkers and judges)
	Home      string // the agent home (library.AgentHome)
}

// SourcePath is a generated skill's source in the library.
func (e Env) SourcePath(id string) string {
	return filepath.Join(e.LibDir, "skills", id, "SKILL.md")
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
	Path   string
	State  string
}

// Report is what a sync found and did.
type Report struct {
	Skills  []SkillReport
	Changed []string // paths written or removed
}

// defsFile holds the scope skills you created and the scope proposals you
// dismissed (whole-file, written under the library lock).
const defsFile = "defs.json"

type defs struct {
	Skills    []Def    `json:"skills"`
	Dismissed []string `json:"dismissed,omitempty"`
}

func readDefs(lib string) defs {
	var d defs
	if b, err := os.ReadFile(filepath.Join(lib, "skills", defsFile)); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	return d
}

// Defs is the scope skills you created and the scopes you dismissed.
func Defs(lib string) ([]Def, []string) {
	d := readDefs(lib)
	return d.Skills, d.Dismissed
}

// CreateSkill adds a skill for scope (the Skills tab's "Create skill").
func CreateSkill(lib, scope, name string) error {
	return editDefs(lib, func(d *defs) {
		id := SlugID(scope)
		if !slices.ContainsFunc(d.Skills, func(s Def) bool { return s.Scope == scope || s.ID == id }) {
			d.Skills = append(d.Skills, Def{ID: id, Name: name, Scope: scope})
		}
	})
}

// DismissScope stops proposing a skill for scope ("Not now").
func DismissScope(lib, scope string) error {
	return editDefs(lib, func(d *defs) {
		if !slices.Contains(d.Dismissed, scope) {
			d.Dismissed = append(d.Dismissed, scope)
		}
	})
}

func editDefs(lib string, fn func(*defs)) error {
	unlock, err := library.Lock(lib)
	if err != nil {
		return err
	}
	defer unlock()
	d := readDefs(lib)
	fn(&d)
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(lib, "skills"), 0o755); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(lib, "skills", defsFile), append(b, '\n'), 0o644)
}

// Sync makes disk match the accepted learnings: each generated skill's source in
// the library, its install in every agent that's set up, and the checkers and
// judges its learnings produce in your checks folder. It writes only what
// differs and removes what's no longer wanted — only files prereview wrote and
// nobody changed. Idempotent: calling it again with no change does nothing, so
// every server can run it after any library change.
func Sync(env Env, ls []library.Learning) (Report, error) {
	var rep Report
	ds, _ := Defs(env.LibDir)
	gen := Generate(ls, ds)
	err := Own(env.LibDir, func(o *Owner) error {
		want := map[string]bool{}
		note := func(path string, changed bool) {
			want[path] = true
			if changed {
				rep.Changed = append(rep.Changed, path)
			}
		}
		for _, s := range gen {
			sr := SkillReport{Skill: s, Source: env.SourcePath(s.ID)}
			src := []byte(s.SkillMD())
			switch changed, err := o.Write("", sr.Source, src); {
			case errors.Is(err, ErrEdited):
				sr.Edited = true
				src, _ = os.ReadFile(sr.Source) // installs follow your edit
				want[sr.Source] = true
			case err != nil:
				return err
			default:
				note(sr.Source, changed)
			}
			for _, t := range Targets {
				p := t.Path(env.Home, s.ID)
				body := src
				if t.Cursor {
					body = []byte(cursorFrom(s, src))
				}
				ir := InstallReport{Target: t, Path: p, State: Installed}
				changed, err := o.Write(filepath.Join(env.Home, t.Root), p, body)
				switch {
				case errors.Is(err, ErrNoRoot):
					ir.State = NotSetUp
				case errors.Is(err, ErrEdited):
					ir.State, want[p] = EditedThere, true
				case errors.Is(err, ErrForeign):
					ir.State = Foreign
				case err != nil:
					return err
				default:
					note(p, changed)
				}
				sr.Installs = append(sr.Installs, ir)
			}
			for _, c := range s.Checks {
				if env.ChecksDir == "" {
					break
				}
				p := filepath.Join(env.ChecksDir, c.File())
				changed, err := o.Write("", p, []byte(c.Body))
				if err != nil && !errors.Is(err, ErrEdited) && !errors.Is(err, ErrForeign) {
					return err
				}
				note(p, changed)
			}
			rep.Skills = append(rep.Skills, sr)
		}
		for _, p := range o.Paths() { // what a learning no longer asks for
			if want[p] {
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

// Regenerate discards your edit to a generated skill's source and writes it
// from the learnings again (the next Sync installs it).
func Regenerate(env Env, ls []library.Learning, id string) error {
	ds, _ := Defs(env.LibDir)
	for _, s := range Generate(ls, ds) {
		if s.ID == id {
			return Own(env.LibDir, func(o *Owner) error { return o.Reclaim(env.SourcePath(id), []byte(s.SkillMD())) })
		}
	}
	return errors.New("no generated skill " + id)
}

// cursorFrom renders the Cursor rule from the skill's source (generated or your
// edit): its body after the SKILL.md frontmatter, under Cursor's frontmatter.
func cursorFrom(s Skill, src []byte) string {
	body := string(src)
	if rest, ok := strings.CutPrefix(body, "---\n"); ok {
		if i := strings.Index(rest, "\n---\n"); i >= 0 {
			body = strings.TrimLeft(rest[i+len("\n---\n"):], "\n")
		}
	}
	r := s.CursorRule()
	return r[:strings.Index(r, "\n---\n")+len("\n---\n\n")] + body
}
