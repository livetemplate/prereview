package skills

import (
	"fmt"
	"slices"
	"strings"

	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
)

// IDPrefix starts every generated skill's ID ("learned-writing"), so one never
// collides with a skill you made by hand.
const IDPrefix = "learned-"

// WritingID is the default skill: every accepted learning lands in it unless its
// scope has a skill of its own.
const WritingID = IDPrefix + "writing"

// Skill is one generated skill: the learnings it carries and the checks they
// produce, ready to render.
type Skill struct {
	ID, Name, Scope string
	Extends         string // a narrower skill extends Writing instead of copying it
	Use             string // an agent-text skill's "use when" (library.AgentText.Use)
	Lines           []Line
	Checks          []Check
}

// Line is one instruction, traced back to its learning.
type Line struct {
	LearningID, Text string
}

// Check is a checker or judge a learning produces, written to your checks folder.
type Check struct {
	LearningID string
	ID         string // learned-<learning id>: the rule's file name and ID
	Name       string
	Judge      bool
	Body       string
}

// File is the checks-folder file name.
func (c Check) File() string {
	if c.Judge {
		return c.ID + ".md"
	}
	return c.ID + ".yml"
}

// Def is a skill you created for a scope (the Skills tab's "Create skill").
type Def struct {
	ID, Name, Scope string
}

// ProposeAt is how many accepted learnings must share a scope with no skill of
// its own before the Skills tab offers one.
const ProposeAt = 5

// Generate builds the generated skills from the accepted learnings: one per
// created scope skill, plus Writing for the rest (only when it has anything).
// Deterministic: the same learnings give the same skills, line for line.
func Generate(ls []library.Learning, defs []Def) []Skill {
	byScope := map[string]*Skill{}
	writing := &Skill{ID: WritingID, Name: "Writing", Scope: library.DefaultScope}
	var out []*Skill
	for _, d := range defs {
		s := &Skill{ID: d.ID, Name: d.Name, Scope: d.Scope, Extends: WritingID}
		byScope[d.Scope] = s
		out = append(out, s)
	}
	// The agent's own text gets a skill per kind from its first lesson, so a
	// lesson about replies never applies to every doc through Writing.
	var agentText []*Skill
	for _, a := range library.AgentTexts {
		if byScope[a.Scope] != nil {
			continue // you created a skill for it yourself
		}
		s := &Skill{ID: SlugID(a.Scope), Name: a.Scope, Scope: a.Scope, Extends: WritingID, Use: a.Use}
		byScope[a.Scope] = s
		agentText = append(agentText, s)
	}
	for _, l := range accepted(ls) {
		s := byScope[l.Scope]
		if s == nil {
			s = writing
		}
		if producesInstruction(l) {
			text := oneLine(l.Text)
			if len(l.Globs) > 0 { // where it applies stays on its own line, not the whole skill
				text += " (in " + strings.Join(l.Globs, ", ") + ")"
			}
			s.Lines = append(s.Lines, Line{LearningID: l.ID, Text: text})
		}
		if c, ok := drafted(l, false); ok {
			s.Checks = append(s.Checks, c)
		}
		if c, ok := drafted(l, true); ok {
			s.Checks = append(s.Checks, c)
		}
	}
	for _, s := range agentText {
		if len(s.Lines)+len(s.Checks) > 0 {
			out = append(out, s)
		}
	}
	if len(writing.Lines)+len(writing.Checks) > 0 {
		out = append([]*Skill{writing}, out...)
	}
	res := make([]Skill, 0, len(out))
	for _, s := range out {
		res = append(res, *s)
	}
	return res
}

// WriteWith is, for each kind of agent text, the generated skills to load
// before writing it: Writing, then the kind's own skill. A kind with neither is
// left out, and nil when no kind has any (the snapshot omits the field).
func WriteWith(gen []Skill) map[string][]string {
	var out map[string][]string
	for _, a := range library.AgentTexts {
		var ids []string
		for _, s := range gen {
			if s.ID == WritingID || s.Scope == a.Scope {
				ids = append(ids, s.ID)
			}
		}
		if len(ids) > 0 {
			if out == nil {
				out = map[string][]string{}
			}
			out[a.Kind] = ids
		}
	}
	return out
}

// Current is the skills the library's learnings make now (what Sync writes).
func Current(lib string, ls []library.Learning) []Skill { return generate(lib, ls, Imports(lib)) }

// Proposal is a scope with enough accepted learnings for a skill of its own.
type Proposal struct {
	Scope string
	Count int
}

// Proposals are the scopes worth a skill of their own: ProposeAt or more
// accepted learnings, no created skill, not dismissed, not an imported skill's
// overlay. Sorted by scope.
func Proposals(lib string, ls []library.Learning) []Proposal {
	d := readJSON[defs](lib, defsFile)
	has := map[string]bool{library.DefaultScope: true}
	for _, a := range library.AgentTexts { // these get a skill from the first lesson
		has[a.Scope] = true
	}
	for _, s := range d.Skills {
		has[s.Scope] = true
	}
	for _, s := range d.Dismissed {
		has[s] = true
	}
	for _, imp := range Imports(lib) {
		has[imp.Name] = true
	}
	n := map[string]int{}
	for _, l := range ls {
		if l.Status == library.StatusAccepted && !has[l.Scope] {
			n[l.Scope]++
		}
	}
	var out []Proposal
	for scope, c := range n {
		if c >= ProposeAt {
			out = append(out, Proposal{scope, c})
		}
	}
	slices.SortFunc(out, func(a, b Proposal) int { return strings.Compare(a.Scope, b.Scope) })
	return out
}

// producesInstruction: a learning becomes a skill line unless it only asked for
// checks.
func producesInstruction(l library.Learning) bool {
	return len(l.Produces) == 0 || slices.Contains(l.Produces, "instruction")
}

// drafted is the learning's checker or judge, when it carries a valid one
// (re-validated here: an imported learning never went through learn propose).
func drafted(l library.Learning, judge bool) (Check, bool) {
	body := l.Checker
	if judge {
		body = l.Judge
	}
	if strings.TrimSpace(body) == "" || checks.ValidateDrafted([]byte(body), judge) != nil { // gates every way a learning arrives
		return Check{}, false
	}
	c := Check{LearningID: l.ID, ID: IDPrefix + l.ID, Judge: judge, Body: body}
	if judge {
		c.Name = checks.ParseJudge(c.ID, checks.SourceUser, "", []byte(body)).Name
	} else {
		c.Name = checks.ParseRule(c.ID, checks.SourceUser, "", []byte(body)).Name
	}
	return c, true
}

// SlugID is a scope's skill ID: "Technical docs" → "learned-technical-docs".
func SlugID(scope string) string { return IDPrefix + library.Slug(scope, "skill") }

// Header marks every generated file.
const Header = "<!-- Generated by prereview from your accepted learnings. Edit the learnings in prereview (Library → Learnings), not this file: an edit here stops prereview updating it. -->"

// Description is the skill's frontmatter description: when an agent should use it.
func (s Skill) Description() string {
	if s.Extends == "" {
		return "Your writing preferences, learned from your own review comments. Use when writing or editing prose: docs, plans, READMEs, release notes, messages, and the replies, suggestions and quiz questions you send through prereview."
	}
	if s.Use != "" {
		return fmt.Sprintf("Your preferences for %s, learned from your own review comments. Use when writing %s; it extends %s.", strings.ToLower(s.Name), s.Use, s.Extends)
	}
	return fmt.Sprintf("Your preferences for %s, learned from your own review comments. Use when writing or editing %s; it extends %s.", s.Name, strings.ToLower(s.Name), s.Extends)
}

// Body is the skill's Markdown after the frontmatter.
func (s Skill) Body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n# %s\n\n", Header, s.Name)
	if s.Extends != "" {
		fmt.Fprintf(&b, "These extend the `%s` skill: apply both.\n\n", s.Extends)
	}
	if len(s.Lines) > 0 {
		b.WriteString("Apply these preferences, each learned from the reviewer's own comments:\n\n")
		for _, l := range s.Lines {
			fmt.Fprintf(&b, "- %s <!-- learning %s -->\n", l.Text, l.LearningID)
		}
	}
	if len(s.Checks) > 0 {
		b.WriteString("\n## Checks\n\nBefore you finish, run `prereview check <files you changed>` and fix what it reports. It includes these checks from your learnings:\n\n")
		for _, c := range s.Checks {
			kind := "checker"
			if c.Judge {
				kind = "judge (ask the reviewer to run it)"
			}
			fmt.Fprintf(&b, "- %s — %s, `%s` <!-- learning %s -->\n", c.Name, kind, c.ID, c.LearningID)
		}
	}
	return b.String()
}

// SkillMD is the skill's SKILL.md: the Agent Skills format Claude, Codex,
// Gemini and opencode all read.
func (s Skill) SkillMD() string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s", s.ID, YAMLQuote(s.Description()), s.Body())
}

// CursorRule is the skill as a Cursor rule (.mdc), applied when relevant.
func (s Skill) CursorRule() string { return s.cursorFrontmatter() + s.Body() }

func (s Skill) cursorFrontmatter() string {
	return fmt.Sprintf("---\ndescription: %s\nalwaysApply: false\n---\n\n", YAMLQuote(s.Description()))
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// YAMLQuote renders s as a double-quoted YAML string (frontmatter values).
func YAMLQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
