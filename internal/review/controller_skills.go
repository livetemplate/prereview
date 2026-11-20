package review

import (
	"cmp"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"slices"

	"github.com/livetemplate/livetemplate"

	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/skills"
)

// Generated skills (checks M4): accepted learnings become prereview-owned
// skills, installed where each agent loads skills, and the checkers and judges
// they carry land in the library's learned-checks folder. skills.Sync is
// idempotent, so every server runs it after any library change (its own, or
// another's seen by the watcher).

// skillEnv is where generated skills are read and written; ok is false with no
// library or no agent home (both injected at launch).
func (c *PrereviewController) skillEnv() (skills.Env, bool) {
	if c.LibraryDir == "" || c.AgentHome == "" {
		return skills.Env{}, false
	}
	return skills.Env{LibDir: c.LibraryDir, Home: c.AgentHome}, true
}

// learnedDir is the learned-checks folder the checks runner loads ("" with no library).
func (c *PrereviewController) learnedDir() string { return skills.LearnedChecksDir(c.LibraryDir) }

// syncSkills brings the generated skills and learned checks in line with the
// accepted learnings, records a library version when anything moved (changelog
// names the learning, when this server's action caused it; Checkpoint itself is
// the no-op check, so a restored skill source is versioned too) and reloads the
// checks, so a new checker runs in every open tab. syncMu serialises this
// server's syncs; the report is swapped under reportMu, so a render never waits
// on another server's lock.
func (c *PrereviewController) syncSkills(changelog string) {
	env, ok := c.skillEnv()
	if !ok {
		return
	}
	// The switch is a per-user pref, read at each sync so every server follows
	// the one you flipped.
	env.NoPointers = loadUIPrefs(c.UIPrefsPath).AgentPointersOff
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	rep, err := skills.Sync(env, c.learnings())
	if err != nil {
		slog.Warn("skills sync", "err", err)
		return
	}
	c.reportMu.Lock()
	c.skillReport = rep
	c.reportMu.Unlock()
	checksChanged := c.reloadChecksIfChanged()
	c.checkpointLibrary(VersionTriggerLearning, changelog)
	if checksChanged {
		go c.broadcast("ChecksUpdated")
	}
}

// lastSkillReport is the latest sync's report, for the Skills tab.
func (c *PrereviewController) lastSkillReport() skills.Report {
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	return c.skillReport
}

// WriteWith is, per kind of agent text, the generated skills to load before
// writing it. Every snapshot carries it, so a skill the background learner adds
// mid-review reaches the agent in the next one; it's re-read only when the
// learnings or skill records change. A review with no library of its own
// (--external) reads SkillsLibDir.
func (c *PrereviewController) WriteWith() map[string][]string {
	lib := cmp.Or(c.LibraryDir, c.SkillsLibDir)
	if lib == "" {
		return nil
	}
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	if st := skills.InputsStamp(lib); st != c.writeWithStamp {
		c.writeWithStamp, c.writeWith = st, skills.WriteWithFor(lib)
	}
	return c.writeWith
}

// libTabSkills is the Library's Skills tab.
const libTabSkills = "skills"

// SkillsTab is the Library's Skills tab: your generated skills, the scopes
// worth a skill of their own, and (M4b) the skills you already have.
type SkillsTab struct {
	// Pointers tell agents to use the skills: the memory blocks and the Claude
	// Code hook, with the switch that turns them off.
	Pointers    []SkillInstall
	PointersOff bool
	Cards       []SkillCard
	Proposals   []skills.Proposal
	Imported    []ImportedCard
	Found       []FoundSkill
	Empty       string
}

// ImportedCard is a skill you imported (read by reference, never written).
type ImportedCard struct {
	Name, PathRel, Path string
	EditorURL           template.URL
	Changed             bool     // its SKILL.md differs from when you imported it
	Overlay             int      // accepted learnings attached to it (scope = its name)
	Checks              int      // checks read from its evals/
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
	Versions          int          // its versions in the library
	History           *HistoryView // only while expanded
}

// SkillInstall is one agent's copy of a skill.
type SkillInstall struct {
	Label, State string
	Where        string // the file, for a pointer (~/…)
	OK           bool
}

// applySkillsTab builds the Skills tab while it's open (nil otherwise), from the
// latest sync's report; only the expanded skill carries its history.
func (c *PrereviewController) applySkillsTab(state *PrereviewState) {
	state.SkillsTab = nil
	env, ok := c.skillEnv()
	if !state.ShowLibrary || state.libraryTab() != libTabSkills || !ok {
		return
	}
	rep := c.lastSkillReport()
	tab := &SkillsTab{PointersOff: state.AgentPointersOff}
	for _, p := range rep.Pointers {
		tab.Pointers = append(tab.Pointers, SkillInstall{Label: p.Label, State: p.State, Where: library.HomeLabel(env.Home, p.Path), OK: p.State == skills.Added})
	}
	for _, s := range rep.Skills {
		card := SkillCard{ID: s.ID, Name: s.Name, Scope: s.Scope, Source: s.Source, SourceRel: library.HomeLabel(env.Home, s.Source),
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
			h := c.ruleHistory(key)
			card.Versions = len(h)
			if state.ExpandedSkill == s.ID && len(h) > 0 {
				card.History = &HistoryView{ID: s.ID, Action: "restoreSkillVersion", Versions: h}
			}
		}
		tab.Cards = append(tab.Cards, card)
	}
	ls := c.learnings()
	for _, ir := range rep.Imports {
		card := ImportedCard{Name: ir.Name, Path: ir.Path, PathRel: library.HomeLabel(env.Home, ir.Path),
			EditorURL: editorURL(state.Editor, state.EditorTemplate, state.EditorSSHHost, ir.Path, 1),
			Changed:   ir.Changed, Overlay: len(skills.Overlay(ls, ir.Name)), Checks: len(ir.Checks)}
		for _, sk := range ir.Skipped {
			card.Skipped = append(card.Skipped, sk.Label+": "+sk.Reason)
		}
		tab.Imported = append(tab.Imported, card)
	}
	for _, f := range skills.Discover(env) {
		if !slices.ContainsFunc(rep.Imports, func(ir skills.ImportReport) bool { return ir.Name == f.Name }) {
			tab.Found = append(tab.Found, FoundSkill{Name: f.Name, Hash: f.Hash, Where: library.HomeLabel(env.Home, f.Paths[0]), Copies: len(f.Paths), HasEvals: f.HasEvals})
		}
	}
	tab.Proposals = skills.Proposals(c.LibraryDir, ls)
	if len(tab.Cards)+len(tab.Proposals)+len(tab.Imported)+len(tab.Found) == 0 {
		tab.Empty = "No skills yet. Accept a learning and it becomes a line in your Writing skill, installed for every agent set up on this machine."
	}
	state.SkillsTab = tab
}

// ToggleAgentPointers turns "Tell agents to use these skills" on or off, and
// syncs at once so the memory blocks and the hook follow the switch.
func (c *PrereviewController) ToggleAgentPointers(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.AgentPointersOff = !state.AgentPointersOff
	c.savePrefs(state)
	c.syncSkills("")
	c.applySkillsTab(&state)
	return state, nil
}

// ExpandSkill shows (again hides) one skill's version history.
func (c *PrereviewController) ExpandSkill(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if state.ExpandedSkill == id {
		id = ""
	}
	state.ExpandedSkill = id
	c.applySkillsTab(&state)
	return state, nil
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
