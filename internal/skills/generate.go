package skills

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
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
	Lines           []Line
	Checks          []Check
	Globs           []string
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
	var accepted []library.Learning
	for _, l := range ls {
		if l.Status == library.StatusAccepted {
			accepted = append(accepted, l)
		}
	}
	sort.SliceStable(accepted, func(i, j int) bool {
		if accepted[i].CreatedAt != accepted[j].CreatedAt {
			return accepted[i].CreatedAt < accepted[j].CreatedAt
		}
		return accepted[i].ID < accepted[j].ID
	})
	for _, l := range accepted {
		s := byScope[l.Scope]
		if s == nil {
			s = writing
		}
		if producesInstruction(l) {
			s.Lines = append(s.Lines, Line{LearningID: l.ID, Text: oneLine(l.Text)})
		}
		for _, g := range l.Globs {
			if !slices.Contains(s.Globs, g) {
				s.Globs = append(s.Globs, g)
			}
		}
		if c, ok := drafted(l, false); ok {
			s.Checks = append(s.Checks, c)
		}
		if c, ok := drafted(l, true); ok {
			s.Checks = append(s.Checks, c)
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

// Proposals are scopes worth a skill of their own: ProposeAt or more accepted
// learnings, no created skill, not dismissed. Sorted by scope.
func Proposals(ls []library.Learning, defs []Def, dismissed []string) map[string]int {
	has := map[string]bool{library.DefaultScope: true}
	for _, d := range defs {
		has[d.Scope] = true
	}
	for _, s := range dismissed {
		has[s] = true
	}
	n := map[string]int{}
	for _, l := range ls {
		if l.Status == library.StatusAccepted && !has[l.Scope] {
			n[l.Scope]++
		}
	}
	for s, c := range n {
		if c < ProposeAt {
			delete(n, s)
		}
	}
	return n
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
	if strings.TrimSpace(body) == "" || checks.ValidateDrafted([]byte(body), judge) != nil {
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
func SlugID(scope string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(scope), "-"), "-")
	if s == "" {
		s = "skill"
	}
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	return IDPrefix + s
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Header marks every generated file.
const Header = "<!-- Generated by prereview from your accepted learnings. Edit the learnings in prereview (Library → Learnings), not this file: an edit here stops prereview updating it. -->"

// Description is the skill's frontmatter description: when an agent should use it.
func (s Skill) Description() string {
	if s.Extends == "" {
		return "Your writing preferences, learned from your own review comments. Use when writing or editing prose: docs, plans, READMEs, release notes, messages."
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
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s", s.ID, quote(s.Description()), s.Body())
}

// CursorRule is the skill as a Cursor rule (.mdc): applied when relevant, or to
// the learnings' globs.
func (s Skill) CursorRule() string {
	var fm strings.Builder
	fmt.Fprintf(&fm, "---\ndescription: %s\n", quote(s.Description()))
	if len(s.Globs) > 0 {
		fmt.Fprintf(&fm, "globs: %s\n", strings.Join(s.Globs, ","))
	}
	fm.WriteString("alwaysApply: false\n---\n\n")
	return fm.String() + s.Body()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
