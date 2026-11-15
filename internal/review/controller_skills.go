package review

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"github.com/livetemplate/livetemplate"

	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/skills"
)

// Generated skills (checks M4): accepted learnings become prereview-owned
// skills, installed where each agent loads skills, and the checkers and judges
// they carry land in your checks folder. skills.Sync is idempotent, so every
// server runs it after any library change (its own, or another's seen by the
// watcher).

// skillEnv is where generated skills are read and written; ok is false with no
// library.
func (c *PrereviewController) skillEnv() (skills.Env, bool) {
	if c.LibraryDir == "" {
		return skills.Env{}, false
	}
	home, err := library.AgentHome()
	if err != nil {
		return skills.Env{}, false
	}
	return skills.Env{LibDir: c.LibraryDir, ChecksDir: c.ChecksDir, Home: home}, true
}

// syncSkills brings the generated skills and derived checks in line with the
// accepted learnings. When it changed anything it records a library version
// (changelog names the learning, when this server's action caused it) and
// reloads the checks, so a new checker runs in every open tab.
func (c *PrereviewController) syncSkills(changelog string) {
	env, ok := c.skillEnv()
	if !ok {
		return
	}
	c.skillMu.Lock()
	defer c.skillMu.Unlock()
	rep, err := skills.Sync(env, c.learnings())
	if err != nil {
		slog.Warn("skills sync", "err", err)
		return
	}
	c.skillReport = rep
	if len(rep.Changed) == 0 {
		return
	}
	checksChanged := c.reloadChecksIfChanged()
	c.checkpointLibrary(VersionTriggerLearning, changelog)
	if checksChanged {
		go c.broadcast("ChecksUpdated")
	}
}

// lastSkillReport is the latest sync's report, for the Skills tab.
func (c *PrereviewController) lastSkillReport() skills.Report {
	c.skillMu.Lock()
	defer c.skillMu.Unlock()
	return c.skillReport
}

// libTabSkills is the Library's Skills tab.
const libTabSkills = "skills"

// SkillsTab is the Library's Skills tab: your generated skills, the scopes
// worth a skill of their own, and (M4b) the skills you already have.
type SkillsTab struct {
	Cards     []SkillCard
	Proposals []SkillProposal
	Imported  []ImportedCard
	Found     []FoundSkill
	Empty     string
}

// ImportedCard is a skill you imported (read by reference, never written).
type ImportedCard struct {
	Name, PathRel, Path string
	EditorURL           template.URL
	Changed             bool // its SKILL.md differs from when you imported it
	Overlay             int  // accepted learnings attached to it (scope = its name)
	Checks              []string // checks read from its evals/ (names)
	Skipped             []string // its graders that grade an agent run, with why
}

// FoundSkill is a skill on this machine you could import.
type FoundSkill struct {
	Name, Hash, Where string
	Copies            int
	HasEvals          bool
}

// SkillCard is one generated skill.
type SkillCard struct {
	ID, Name, Scope   string
	Source, SourceRel string // its SKILL.md in the library, and as shown (~/…)
	EditorURL         template.URL
	Lines             int
	Checkers, Judges  int
	Edited            bool // you edited SKILL.md: prereview stopped regenerating it
	Installs          []SkillInstall
	History           *HistoryView
}

// SkillInstall is one agent's copy of a skill.
type SkillInstall struct {
	Label, State string
	OK           bool
}

// SkillProposal is a scope with enough accepted learnings for its own skill.
type SkillProposal struct {
	Scope string
	Count int
}

// applySkillsTab builds the Skills tab while it's open (nil otherwise).
func (c *PrereviewController) applySkillsTab(state *PrereviewState) {
	state.SkillsTab = nil
	if !state.ShowLibrary || state.libraryTab() != libTabSkills || c.LibraryDir == "" {
		return
	}
	tab := &SkillsTab{}
	home, _ := library.AgentHome()
	for _, s := range c.lastSkillReport().Skills {
		card := SkillCard{ID: s.ID, Name: s.Name, Scope: s.Scope, Source: s.Source, SourceRel: tildePath(home, s.Source),
			EditorURL: editorURL(state.Editor, state.EditorTemplate, state.EditorSSHHost, s.Source, 1),
			Lines:     len(s.Lines), Edited: s.Edited}
		for _, ch := range s.Checks {
			if ch.Judge {
				card.Judges++
			} else {
				card.Checkers++
			}
		}
		for _, in := range s.Installs {
			card.Installs = append(card.Installs, SkillInstall{Label: in.Target.Label, State: in.State, OK: in.State == skills.Installed})
		}
		if key := c.libraryKey(s.Source); key != "" {
			if h := c.ruleHistory(key); len(h) > 0 {
				card.History = &HistoryView{ID: s.ID, Action: "restoreSkillVersion", Versions: h}
			}
		}
		tab.Cards = append(tab.Cards, card)
	}
	env, _ := c.skillEnv()
	imports := skills.Imports(c.LibraryDir)
	imported := map[string]bool{}
	for _, imp := range imports {
		imported[imp.Name] = true
		card := ImportedCard{Name: imp.Name, Path: imp.Path, PathRel: tildePath(home, imp.Path),
			EditorURL: editorURL(state.Editor, state.EditorTemplate, state.EditorSSHHost, imp.Path, 1),
			Changed:   imp.Changed(), Overlay: len(skills.Overlay(c.learnings(), imp.Name))}
		ics, skipped := skills.ImportedChecks(env, imp)
		for _, ic := range ics {
			card.Checks = append(card.Checks, ic.Name)
		}
		for _, sk := range skipped {
			card.Skipped = append(card.Skipped, filepath.Base(filepath.Dir(filepath.Dir(sk.Source)))+"/"+filepath.Base(sk.Source)+": "+sk.Reason)
		}
		tab.Imported = append(tab.Imported, card)
	}
	for _, f := range skills.Discover(env) {
		if !imported[f.Name] {
			tab.Found = append(tab.Found, FoundSkill{Name: f.Name, Hash: f.Hash, Where: tildePath(home, f.Paths[0]), Copies: len(f.Paths), HasEvals: f.HasEvals})
		}
	}
	defs, dismissed := skills.Defs(c.LibraryDir)
	for name := range imported {
		dismissed = append(dismissed, name) // an imported skill's scope is its overlay, not a proposal
	}
	for scope, n := range skills.Proposals(c.learnings(), defs, dismissed) {
		tab.Proposals = append(tab.Proposals, SkillProposal{Scope: scope, Count: n})
	}
	slices.SortFunc(tab.Proposals, func(a, b SkillProposal) int { return strings.Compare(a.Scope, b.Scope) })
	if len(tab.Cards)+len(tab.Proposals)+len(tab.Imported)+len(tab.Found) == 0 {
		tab.Empty = "No skills yet. Accept a learning and it becomes a line in your Writing skill, installed for every agent set up on this machine."
	}
	state.SkillsTab = tab
}

// tildePath shows p under home as ~/….
func tildePath(home, p string) string {
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + filepath.ToSlash(rel)
	}
	return p
}

// CreateSkill gives a proposed scope its own skill.
func (c *PrereviewController) CreateSkill(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	scope := ctx.GetString("scope")
	if err := skills.CreateSkill(c.LibraryDir, scope, scope); err != nil {
		return state, err
	}
	return c.libraryWritten(state, "Created the “"+scope+"” skill"), nil
}

// DismissSkillScope stops offering a skill for a scope ("Not now").
func (c *PrereviewController) DismissSkillScope(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if err := skills.DismissScope(c.LibraryDir, ctx.GetString("scope")); err != nil {
		return state, err
	}
	return c.libraryWritten(state, ""), nil
}

// RegenerateSkill discards your edit to a generated skill and writes it from
// the learnings again (installed by the sync that follows).
func (c *PrereviewController) RegenerateSkill(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	env, ok := c.skillEnv()
	if !ok {
		return state, errors.New("no library folder")
	}
	if err := skills.Regenerate(env, c.learnings(), ctx.GetString("id")); err != nil {
		return state, err
	}
	return c.libraryWritten(state, "Regenerated from your learnings"), nil
}

// RestoreSkillVersion puts an earlier version of a generated skill back. That is
// an edit like any other: prereview stops regenerating the skill (Regenerate
// returns to the learnings) and its installs follow the restored text.
func (c *PrereviewController) RestoreSkillVersion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	env, ok := c.skillEnv()
	vs := c.libVersionStore()
	if !ok || vs == nil {
		return state, errors.New("no library versions")
	}
	path := env.SourcePath(ctx.GetString("id"))
	data, err := vs.Restore(c.libraryKey(path), ctx.GetInt("seq"))
	if err != nil {
		return state, fmt.Errorf("restore: %w", err)
	}
	if err := writeFileAtomicVersions(path, data); err != nil {
		return state, fmt.Errorf("restore: %w", err)
	}
	state.Flash = "Restored an earlier version of the skill"
	return c.libraryWritten(state, "Restored an earlier version"), nil
}

// ImportSkill imports a skill found on this machine, by reference, with the
// checks its evals/ carries.
func (c *PrereviewController) ImportSkill(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	env, ok := c.skillEnv()
	if !ok {
		return state, errors.New("no library folder")
	}
	h := ctx.GetString("hash")
	for _, f := range skills.Discover(env) {
		if f.Hash == h {
			if err := skills.ImportSkill(c.LibraryDir, f); err != nil {
				return state, err
			}
			state.Flash = "Imported " + f.Name + " (prereview never edits its folder)"
			return c.libraryWritten(state, "Imported the “"+f.Name+"” skill"), nil
		}
	}
	return state, errors.New("that skill is no longer there")
}

// ForgetImport drops an imported skill from the library; its folder is untouched.
func (c *PrereviewController) ForgetImport(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	name := ctx.GetString("name")
	if err := skills.ForgetImport(c.LibraryDir, name); err != nil {
		return state, err
	}
	return c.libraryWritten(state, "Stopped tracking the “"+name+"” skill"), nil
}
