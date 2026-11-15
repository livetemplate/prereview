package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
)

// Imported skills (checks M4b) are yours: prereview reads them by reference —
// path, content hash, name and description, re-read on every look — and never
// writes into their folders. Learnings whose scope is an imported skill's name
// form its overlay: they stay out of the generated skills, and Export patch
// turns them into a diff for you to apply to the skill yourself.

// Found is a skill on this machine that prereview could import: one per
// distinct SKILL.md content (the same skill installed in several agents' folders
// is one skill).
type Found struct {
	Name, Description string
	Hash              string
	Paths             []string // every SKILL.md with this content, sorted
	HasEvals          bool     // a sibling evals/ folder
}

// Discover lists the skills under every agent's skills root, deduped by content.
// prereview's own skill and the skills it generated are skipped: by their name
// (prereview, learned-*) and by ownership (a file prereview wrote).
func Discover(env Env) []Found {
	var owned map[string]bool
	_ = Own(env.LibDir, func(o *Owner) error {
		owned = map[string]bool{}
		for _, p := range o.Paths() {
			owned[p] = true
		}
		return nil
	})
	byHash := map[string]*Found{}
	for _, t := range Targets {
		if t.Cursor {
			continue // Cursor rules aren't skills
		}
		dirs, _ := os.ReadDir(filepath.Join(env.Home, t.Root))
		for _, d := range dirs {
			if !d.IsDir() || d.Name() == "prereview" || strings.HasPrefix(d.Name(), IDPrefix) {
				continue
			}
			p := filepath.Join(env.Home, t.Root, d.Name(), "SKILL.md")
			b, err := os.ReadFile(p)
			if err != nil || owned[p] {
				continue
			}
			h := hash(b)
			f := byHash[h]
			if f == nil {
				name, desc := frontmatter(b)
				if name == "" {
					name = d.Name()
				}
				f = &Found{Name: name, Description: desc, Hash: h}
				byHash[h] = f
			}
			f.Paths = append(f.Paths, p)
			if fi, err := os.Stat(filepath.Join(filepath.Dir(p), "evals")); err == nil && fi.IsDir() {
				f.HasEvals = true
			}
		}
	}
	out := make([]Found, 0, len(byHash))
	for _, f := range byHash {
		sort.Strings(f.Paths)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Import is a skill you imported, as recorded.
type Import struct {
	Name        string `json:"name"`
	Path        string `json:"path"` // the SKILL.md read (the first of Found.Paths)
	Hash        string `json:"hash"` // its content when imported
	Description string `json:"description,omitempty"`
	At          string `json:"at"`
}

// Changed reports whether the skill's SKILL.md differs from when you imported it
// (or is gone): its overlay's place in the text may have moved.
func (i Import) Changed() bool {
	b, err := os.ReadFile(i.Path)
	return err != nil || hash(b) != i.Hash
}

const importsFile = "imported.json"

// Imports lists the skills you imported.
func Imports(lib string) []Import {
	var out []Import
	if b, err := os.ReadFile(filepath.Join(lib, "skills", importsFile)); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// ImportSkill records f as imported (by reference; its folder is never written).
func ImportSkill(lib string, f Found) error {
	return editImports(lib, func(is []Import) []Import {
		is = slices.DeleteFunc(is, func(i Import) bool { return i.Name == f.Name })
		return append(is, Import{Name: f.Name, Path: f.Paths[0], Hash: f.Hash, Description: f.Description, At: library.Now()})
	})
}

// ForgetImport drops an imported skill from the library (its folder untouched).
func ForgetImport(lib, name string) error {
	return editImports(lib, func(is []Import) []Import {
		return slices.DeleteFunc(is, func(i Import) bool { return i.Name == name })
	})
}

func editImports(lib string, fn func([]Import) []Import) error {
	unlock, err := library.Lock(lib)
	if err != nil {
		return err
	}
	defer unlock()
	is := fn(Imports(lib))
	sort.Slice(is, func(a, b int) bool { return is[a].Name < is[b].Name })
	b, err := json.MarshalIndent(is, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(lib, "skills"), 0o755); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(lib, "skills", importsFile), append(b, '\n'), 0o644)
}

// Overlay is the accepted learnings attached to an imported skill (their scope
// is its name), oldest first.
func Overlay(ls []library.Learning, name string) []Line {
	var out []library.Learning
	for _, l := range ls {
		if l.Status == library.StatusAccepted && l.Scope == name {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	lines := make([]Line, 0, len(out))
	for _, l := range out {
		lines = append(lines, Line{LearningID: l.ID, Text: oneLine(l.Text)})
	}
	return lines
}

// OverlayPatch is a unified diff that appends a "Learned preferences" section
// with the overlay to the skill's SKILL.md, for you to apply from the skill's
// folder (`git apply` or `patch -p1`). "" when there's nothing to add.
func OverlayPatch(imp Import, lines []Line) (string, error) {
	if len(lines) == 0 {
		return "", nil
	}
	b, err := os.ReadFile(imp.Path)
	if err != nil {
		return "", err
	}
	text := string(b)
	noEOL := text != "" && !strings.HasSuffix(text, "\n")
	old := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	add := []string{"", "## Learned preferences", "", "Learned from your own review comments in prereview:", ""}
	for _, l := range lines {
		add = append(add, "- "+l.Text)
	}
	// Context: up to three lines before the end; without a final newline the last
	// line itself changes (it gains one), so it's removed and re-added.
	keep := old[max(0, len(old)-3):]
	var last []string
	if noEOL {
		keep, last = keep[:len(keep)-1], keep[len(keep)-1:]
	}
	start := len(old) - len(keep) - len(last) + 1
	var p strings.Builder
	p.WriteString("--- a/SKILL.md\n+++ b/SKILL.md\n")
	fmt.Fprintf(&p, "@@ -%d,%d +%d,%d @@\n", start, len(keep)+len(last), start, len(keep)+len(last)+len(add))
	for _, l := range keep {
		p.WriteString(" " + l + "\n")
	}
	for _, l := range last {
		p.WriteString("-" + l + "\n\\ No newline at end of file\n+" + l + "\n")
	}
	for _, l := range add {
		p.WriteString("+" + l + "\n")
	}
	return p.String(), nil
}

// withoutOverlays drops the learnings attached to an imported skill: they belong
// to its overlay, not to a generated skill.
func withoutOverlays(lib string, ls []library.Learning) []library.Learning {
	names := map[string]bool{}
	for _, i := range Imports(lib) {
		names[i.Name] = true
	}
	if len(names) == 0 {
		return ls
	}
	return slices.DeleteFunc(slices.Clone(ls), func(l library.Learning) bool { return names[l.Scope] })
}

// frontmatter reads a SKILL.md's name and description.
func frontmatter(b []byte) (name, desc string) {
	rest, ok := strings.CutPrefix(string(b), "---\n")
	if !ok {
		return "", ""
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", ""
	}
	for _, l := range strings.Split(rest[:end], "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			name = v
		case "description":
			desc = v
		}
	}
	return name, desc
}

// ImportedCheck is a document check read from an imported skill's evals.
type ImportedCheck struct {
	Check
	Source string // the grader file it came from
}

// SkippedGrader is a grader that doesn't translate to a document check.
type SkippedGrader struct {
	Source, Reason string
}

// ImportedChecks reads an imported skill's plugin-eval graders
// (evals/<case>/graders/*.md, the `claude plugin eval` layout) and maps the
// ones that can check a document: an `llm` grader becomes a judge (its body is
// the rubric), a `regex` grader with `match: not_contains` becomes an existence
// checker (the pattern must not appear). Every other grader grades an agent run
// (tool calls, files created, a baseline) and is skipped with the reason.
func ImportedChecks(env Env, imp Import) ([]ImportedCheck, []SkippedGrader) {
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(imp.Path), "evals", "*", "graders", "*.md"))
	sort.Strings(files)
	var out []ImportedCheck
	var skipped []SkippedGrader
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		fm, body := splitFrontmatter(b)
		var g struct {
			Type, Pattern, Flags, Match string
		}
		if yaml.Unmarshal([]byte(fm), &g) != nil {
			skipped = append(skipped, SkippedGrader{f, "its frontmatter doesn't parse"})
			continue
		}
		caseName := filepath.Base(filepath.Dir(filepath.Dir(f)))
		gname := strings.TrimSuffix(filepath.Base(f), ".md")
		id := SlugID("imported " + imp.Name + " " + caseName + " " + gname)
		label := imp.Name + ": " + gname
		var c Check
		switch {
		case g.Type == "llm" && strings.TrimSpace(body) != "":
			c = Check{ID: id, Name: label, Judge: true,
				Body: fmt.Sprintf("---\nkind: judge\nname: %s\ncategory: %s\n---\n%s\n", quote(label), quote(imp.Name), strings.TrimSpace(body))}
		case g.Type == "regex" && g.Match == "not_contains" && g.Pattern != "":
			tok := g.Pattern
			if strings.Contains(g.Flags, "i") {
				tok = "(?i)" + tok
			}
			c = Check{ID: id, Name: label,
				Body: fmt.Sprintf("extends: existence\nname: %s\ncategory: %s\nmessage: %s\ntokens:\n  - %s\n", quote(label), quote(imp.Name), quote("'%s' is something "+imp.Name+" says to avoid."), quote(tok))}
		case g.Type == "regex":
			skipped = append(skipped, SkippedGrader{f, "a regex that must appear (or appear N times) grades an agent's reply, not a document"})
			continue
		default:
			skipped = append(skipped, SkippedGrader{f, "a " + g.Type + " grader grades an agent run, not a document"})
			continue
		}
		if err := checksValidate(c); err != nil {
			skipped = append(skipped, SkippedGrader{f, err.Error()})
			continue
		}
		out = append(out, ImportedCheck{Check: c, Source: f})
	}
	return out, skipped
}

// splitFrontmatter is a Markdown file's YAML frontmatter and body.
func splitFrontmatter(b []byte) (fm, body string) {
	rest, ok := strings.CutPrefix(string(b), "---\n")
	if !ok {
		return "", string(b)
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", string(b)
	}
	return rest[:end], strings.TrimPrefix(strings.TrimPrefix(rest[end+len("\n---"):], "\n"), "\n")
}

// checksValidate re-checks a mapped check the way a drafted one is.
func checksValidate(c Check) error { return checks.ValidateDrafted([]byte(c.Body), c.Judge) }
