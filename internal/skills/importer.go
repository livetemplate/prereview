package skills

import (
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
// path and content hash, re-read on every look — and never writes into their
// folders. Learnings whose scope is an imported skill's name form its overlay:
// they stay out of the generated skills, and Export patch turns them into a diff
// for you to apply to the skill yourself.

// Found is a skill on this machine that prereview could import: one per
// distinct SKILL.md content (the same skill installed in several agents' folders
// is one skill).
type Found struct {
	Name     string
	Hash     string
	Paths    []string // every SKILL.md with this content, sorted
	HasEvals bool     // a sibling evals/ folder
}

// Discover lists the skills under every agent's skills root, deduped by content.
// prereview's own skill and the ones it generated are skipped by name
// (prereview, learned-*): every file it installs lives in a learned-* folder.
func Discover(env Env) []Found {
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
			if err != nil {
				continue
			}
			h := hash(b)
			f := byHash[h]
			if f == nil {
				f = &Found{Name: skillName(b, d.Name()), Hash: h}
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

// skillName is a SKILL.md's frontmatter name, or its folder's.
func skillName(b []byte, dir string) string {
	if fm, _, ok := checks.SplitFrontmatter(b); ok {
		var h struct{ Name string }
		if yaml.Unmarshal(fm, &h) == nil && h.Name != "" {
			return h.Name
		}
	}
	return dir
}

// Import is a skill you imported, as recorded.
type Import struct {
	Name string `json:"name"`
	Path string `json:"path"` // the SKILL.md read (the first of Found.Paths)
	Hash string `json:"hash"` // its content when imported
	At   string `json:"at"`
}

// Changed reports whether the skill's SKILL.md differs from when you imported it
// (or is gone): its overlay's place in the text may have moved.
func (i Import) Changed() bool {
	b, err := os.ReadFile(i.Path)
	return err != nil || hash(b) != i.Hash
}

const importsFile = "imported.json"

// Imports lists the skills you imported.
func Imports(lib string) []Import { return readJSON[[]Import](lib, importsFile) }

// ImportSkill records f as imported (by reference; its folder is never written).
func ImportSkill(lib string, f Found) error {
	return editJSON(lib, importsFile, func(is *[]Import) (bool, error) {
		*is = slices.DeleteFunc(*is, func(i Import) bool { return i.Name == f.Name })
		*is = append(*is, Import{Name: f.Name, Path: f.Paths[0], Hash: f.Hash, At: library.Now()})
		slices.SortFunc(*is, func(a, b Import) int { return strings.Compare(a.Name, b.Name) })
		return true, nil
	})
}

// ForgetImport drops an imported skill from the library (its folder untouched).
func ForgetImport(lib, name string) error {
	return editJSON(lib, importsFile, func(is *[]Import) (bool, error) {
		n := len(*is)
		*is = slices.DeleteFunc(*is, func(i Import) bool { return i.Name == name })
		return len(*is) != n, nil
	})
}

// Overlay is the accepted learnings attached to an imported skill (their scope
// is its name), oldest first.
func Overlay(ls []library.Learning, name string) []Line {
	var lines []Line
	for _, l := range accepted(ls) {
		if l.Scope == name {
			lines = append(lines, Line{LearningID: l.ID, Text: oneLine(l.Text)})
		}
	}
	return lines
}

// accepted is the accepted learnings, oldest first (ties by id): the order every
// generated line follows.
func accepted(ls []library.Learning) []library.Learning {
	var out []library.Learning
	for _, l := range ls {
		if l.Status == library.StatusAccepted {
			out = append(out, l)
		}
	}
	slices.SortStableFunc(out, func(a, b library.Learning) int {
		if c := strings.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
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
func withoutOverlays(ls []library.Learning, imports []Import) []library.Learning {
	if len(imports) == 0 {
		return ls
	}
	names := map[string]bool{}
	for _, i := range imports {
		names[i.Name] = true
	}
	return slices.DeleteFunc(slices.Clone(ls), func(l library.Learning) bool { return names[l.Scope] })
}

// SkippedGrader is a grader that doesn't translate to a document check.
type SkippedGrader struct {
	Label, Reason string // Label: "<case>/<grader>.md"
}

// ImportedChecks reads an imported skill's plugin-eval graders
// (evals/<case>/graders/*.md, the `claude plugin eval` layout) and maps the
// ones that can check a document: an `llm` grader becomes a judge (its body is
// the rubric), a `regex` grader with `match: not_contains` becomes an existence
// checker (the pattern must not appear). Every other grader grades an agent run
// (tool calls, files created, a baseline) and is skipped with the reason.
func ImportedChecks(imp Import) ([]Check, []SkippedGrader) {
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(imp.Path), "evals", "*", "graders", "*.md"))
	sort.Strings(files)
	var out []Check
	var skipped []SkippedGrader
	for _, f := range files {
		caseName := filepath.Base(filepath.Dir(filepath.Dir(f)))
		gname := strings.TrimSuffix(filepath.Base(f), ".md")
		where := caseName + "/" + filepath.Base(f)
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		fm, body, _ := checks.SplitFrontmatter(b)
		var g struct{ Type, Pattern, Flags, Match string }
		if yaml.Unmarshal(fm, &g) != nil {
			skipped = append(skipped, SkippedGrader{where, "its frontmatter doesn't parse"})
			continue
		}
		label := imp.Name + ": " + gname
		c := Check{ID: SlugID("imported " + imp.Name + " " + caseName + " " + gname), Name: label}
		switch {
		case g.Type == "llm" && strings.TrimSpace(string(body)) != "":
			c.Judge = true
			c.Body = fmt.Sprintf("---\nkind: judge\nname: %s\ncategory: %s\n---\n%s\n", YAMLQuote(label), YAMLQuote(imp.Name), strings.TrimSpace(string(body)))
		case g.Type == "regex" && g.Match == "not_contains" && g.Pattern != "":
			tok := g.Pattern
			if strings.Contains(g.Flags, "i") {
				tok = "(?i)" + tok
			}
			c.Body = fmt.Sprintf("extends: existence\nname: %s\ncategory: %s\nmessage: %s\ntokens:\n  - %s\n", YAMLQuote(label), YAMLQuote(imp.Name), YAMLQuote("'%s' is something "+imp.Name+" says to avoid."), YAMLQuote(tok))
		case g.Type == "regex":
			skipped = append(skipped, SkippedGrader{where, "a regex that must appear (or appear N times) grades an agent's reply, not a document"})
			continue
		default:
			skipped = append(skipped, SkippedGrader{where, "a " + g.Type + " grader grades an agent run, not a document"})
			continue
		}
		if err := checks.ValidateDrafted([]byte(c.Body), c.Judge); err != nil {
			skipped = append(skipped, SkippedGrader{where, err.Error()})
			continue
		}
		out = append(out, c)
	}
	return out, skipped
}
