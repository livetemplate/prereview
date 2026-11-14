// Package checks runs deterministic prose checks over a file's text: a subset of
// Vale's rule format (existence, substitution, occurrence, plus a vocab list),
// user-supplied commands that print JSON findings, and the vale binary itself when
// a repo carries a .vale.ini. It is pure of prereview's review package: callers
// pass a path and the file's lines and get Results back.
package checks

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Rule types (the `extends:` field). The first three and the vocab list follow
// Vale's semantics; command is prereview's own.
const (
	TypeExistence    = "existence"
	TypeSubstitution = "substitution"
	TypeOccurrence   = "occurrence"
	TypeVocab        = "vocab"
	TypeCommand      = "command"
)

// Levels, as Vale names them. A rule with no level is a warning.
const (
	LevelError      = "error"
	LevelWarning    = "warning"
	LevelSuggestion = "suggestion"
)

// Where a rule came from. Only builtin and user rules may run a command: opening
// a review must never execute code the reviewed repo supplied.
const (
	SourceBuiltin = "builtin"
	SourceUser    = "user"
	SourceRepo    = "repo"
	SourceVale    = "vale"
)

// DefaultFiles are the globs a rule applies to when it sets no `files:`. Prose
// rules on code files produce noise, so code is opt-in.
var DefaultFiles = []string{"*.md", "*.markdown", "*.txt", "*.rst", "*.adoc"}

// DefaultTimeout bounds a command check.
const DefaultTimeout = 5 * time.Second

// ruleFile is the YAML shape: Vale's fields plus prereview's extensions
// (name, description, files, command, timeout, accept/reject for vocab).
type ruleFile struct {
	Extends     string            `yaml:"extends"`
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Message     string            `yaml:"message"`
	Level       string            `yaml:"level"`
	Tokens      []string          `yaml:"tokens"`
	Swap        map[string]string `yaml:"swap"`
	Max         int               `yaml:"max"`
	Token       string            `yaml:"token"`
	IgnoreCase  bool              `yaml:"ignorecase"`
	NonWord     bool              `yaml:"nonword"`
	Scope       string            `yaml:"scope"`
	Accept      []string          `yaml:"accept"`
	Reject      []string          `yaml:"reject"`
	Files       []string          `yaml:"files"`
	Command     []string          `yaml:"command"`
	Timeout     string            `yaml:"timeout"`
}

// Rule is one loaded check. A rule that failed to parse or compile still exists,
// with Err set, so the panel can show it as errored instead of silently dropping it.
type Rule struct {
	ID          string // file name without extension; a user file replaces a built-in with the same ID
	Name        string
	Description string
	Type        string
	Message     string
	Level       string
	Scope       string // occurrence: "sentence" (default) or "line"
	Max         int
	Files       []string
	Command     []string
	Timeout     time.Duration
	Source      string
	Path        string // file on disk; "" for a built-in
	Err         string

	patterns []pattern // compiled matchers (existence, substitution, vocab)
	token    *regexp.Regexp
}

// pattern is one compiled matcher. For a substitution or a vocab accept term,
// want is the preferred text, and a match equal to it is not a finding.
type pattern struct {
	re   *regexp.Regexp
	want string
	// caseOnly: a vocab accept term flags only a match whose case differs.
	caseOnly bool
}

// Async reports whether the rule runs outside the render path: a valid command
// rule (it spawns a process). An errored rule never runs, so it isn't async.
func (r *Rule) Async() bool { return r.Type == TypeCommand && r.Err == "" }

// errCommandSource is the one message for the trust rule on command checks.
const errCommandSource = "command checks may only live in your own checks folder, not in the reviewed repo"

// ruleID is a rule file's ID: its name without the YAML extension.
func ruleID(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(name, ".yml"), ".yaml")
}

// Applies reports whether the rule checks the file at rel (a slash path). A glob
// without a slash matches the base name; one with a slash matches the whole path.
func (r *Rule) Applies(rel string) bool {
	globs := r.Files
	if len(globs) == 0 {
		globs = DefaultFiles
	}
	base := path.Base(rel)
	for _, g := range globs {
		target := base
		if strings.Contains(g, "/") {
			target = rel
		}
		if ok, _ := path.Match(g, target); ok {
			return true
		}
	}
	return false
}

// ParseRule parses one rule file. It never returns nil: a bad file yields a Rule
// with Err set (and the ID, so it still lists).
func ParseRule(id, source, filePath string, data []byte) *Rule {
	r := &Rule{ID: id, Name: id, Source: source, Path: filePath, Level: LevelWarning}
	var f ruleFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		r.Err = "invalid YAML: " + err.Error()
		return r
	}
	if err := r.build(f); err != nil {
		r.Err = err.Error()
	}
	return r
}

func (r *Rule) build(f ruleFile) error {
	if f.Name != "" {
		r.Name = f.Name
	}
	r.Description = f.Description
	r.Type = f.Extends
	r.Message = f.Message
	r.Files = f.Files
	switch f.Level {
	case "":
	case LevelError, LevelWarning, LevelSuggestion:
		r.Level = f.Level
	default:
		return fmt.Errorf("unknown level %q (want error, warning or suggestion)", f.Level)
	}
	for _, g := range f.Files {
		if _, err := path.Match(g, ""); err != nil {
			return fmt.Errorf("bad files glob %q: %v", g, err)
		}
	}
	flags := ""
	if f.IgnoreCase {
		flags = "(?i)"
	}
	switch f.Extends {
	case TypeExistence:
		if len(f.Tokens) == 0 {
			return fmt.Errorf("existence needs tokens")
		}
		re, err := compileTokens(flags, f.Tokens, f.NonWord)
		if err != nil {
			return err
		}
		r.patterns = []pattern{{re: re}}
	case TypeSubstitution:
		if len(f.Swap) == 0 {
			return fmt.Errorf("substitution needs swap")
		}
		keys := make([]string, 0, len(f.Swap))
		for k := range f.Swap {
			keys = append(keys, k)
		}
		sort.Strings(keys) // map order is random; findings must be stable
		for _, k := range keys {
			re, err := compileTokens(flags, []string{k}, f.NonWord)
			if err != nil {
				return err
			}
			r.patterns = append(r.patterns, pattern{re: re, want: f.Swap[k]})
		}
	case TypeOccurrence:
		if f.Max <= 0 || f.Token == "" {
			return fmt.Errorf("occurrence needs max > 0 and token")
		}
		re, err := regexp.Compile(flags + f.Token)
		if err != nil {
			return fmt.Errorf("bad token: %v", err)
		}
		r.token, r.Max = re, f.Max
		switch f.Scope {
		case "", "sentence":
			r.Scope = "sentence"
		case "line":
			r.Scope = "line"
		default:
			return fmt.Errorf("unknown scope %q (want sentence or line)", f.Scope)
		}
	case TypeVocab:
		if len(f.Accept) == 0 && len(f.Reject) == 0 {
			return fmt.Errorf("vocab needs accept or reject")
		}
		for _, term := range f.Accept {
			re, err := compileTokens("(?i)", []string{term}, false)
			if err != nil {
				return err
			}
			r.patterns = append(r.patterns, pattern{re: re, want: term, caseOnly: true})
		}
		if len(f.Reject) > 0 {
			re, err := compileTokens("(?i)", f.Reject, false)
			if err != nil {
				return err
			}
			r.patterns = append(r.patterns, pattern{re: re})
		}
	case TypeCommand:
		if r.Source != SourceBuiltin && r.Source != SourceUser {
			return errors.New(errCommandSource)
		}
		if len(f.Command) == 0 {
			return fmt.Errorf("command needs an argv list")
		}
		r.Command = f.Command
		r.Timeout = DefaultTimeout
		if f.Timeout != "" {
			d, err := time.ParseDuration(f.Timeout)
			if err != nil || d <= 0 {
				return fmt.Errorf("bad timeout %q (want a duration like 5s)", f.Timeout)
			}
			r.Timeout = d
		}
	case "":
		return fmt.Errorf("missing extends (want existence, substitution, occurrence, vocab or command)")
	default:
		return fmt.Errorf("unsupported extends %q (want existence, substitution, occurrence, vocab or command)", f.Extends)
	}
	return nil
}

// compileTokens joins tokens into one alternation, word-bounded unless nonword. A
// literal space in a token matches any run of whitespace, so a phrase still
// matches across a hard-wrapped line.
func compileTokens(flags string, tokens []string, nonword bool) (*regexp.Regexp, error) {
	alts := make([]string, len(tokens))
	for i, t := range tokens {
		alts[i] = strings.ReplaceAll(t, " ", `\s+`)
	}
	expr := "(?:" + strings.Join(alts, "|") + ")"
	if !nonword {
		expr = `\b` + expr + `\b`
	}
	re, err := regexp.Compile(flags + expr)
	if err != nil {
		return nil, fmt.Errorf("bad token: %v", err)
	}
	return re, nil
}

// formatMessage fills a Vale-style message's %s placeholders in order. It does
// not use fmt, so a stray verb in a user's message can't garble it.
func formatMessage(msg string, args ...string) string {
	var b strings.Builder
	for {
		i := strings.Index(msg, "%s")
		if i < 0 || len(args) == 0 {
			b.WriteString(msg)
			return b.String()
		}
		b.WriteString(msg[:i])
		b.WriteString(args[0])
		args, msg = args[1:], msg[i+2:]
	}
}
