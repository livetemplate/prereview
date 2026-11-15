package checks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// TypeJudge is a judge: a Markdown rubric the coding agent answers on request
// (prereview never calls an LLM). It lives in the same library as checkers, with
// the same list, on/off switch and categories; its result comes from the agent's
// latest verdict instead of a run.
const TypeJudge = "judge"

// Statuses only a judge has: no verdict yet, or a verdict for an older version of
// the file or the rubric.
const (
	StatusNotRun = "notrun"
	StatusStale  = "stale"
)

// judgeFile is a judge's frontmatter.
type judgeFile struct {
	Kind        string   `yaml:"kind"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Category    string   `yaml:"category"`
	Files       []string `yaml:"files"`
}

// IsJudge reports a judge rule.
func (r *Rule) IsJudge() bool { return r.Type == TypeJudge }

// ParseJudge loads a judge from a .md file: YAML frontmatter between "---" lines
// (kind: judge, name, description, category, files) and the rubric as the body —
// one yes/no question plus what passing looks like. A malformed file still yields
// a rule, with Err set, so the Checks page can show it as errored.
func ParseJudge(id, source, filePath string, data []byte) *Rule {
	r := &Rule{ID: id, Name: id, Source: source, Path: filePath, Type: TypeJudge,
		Level: LevelSuggestion, Category: DefaultCategory(source)}
	front, body, ok := SplitFrontmatter(data)
	if !ok {
		r.Err = "a judge needs YAML frontmatter between --- lines (kind: judge, name, …) before its rubric"
		return r
	}
	var f judgeFile
	if err := yaml.Unmarshal(front, &f); err != nil {
		r.Err = "invalid frontmatter: " + err.Error()
		return r
	}
	if f.Kind != TypeJudge {
		r.Err = fmt.Sprintf("a .md check must set kind: judge (got %q)", f.Kind)
		return r
	}
	if f.Name != "" {
		r.Name = f.Name
	}
	r.Description = f.Description
	if c := strings.TrimSpace(f.Category); c != "" {
		r.Category = c
	}
	for _, g := range f.Files {
		if _, err := path.Match(g, ""); err != nil {
			r.Err = fmt.Sprintf("bad files glob %q: %v", g, err)
			return r
		}
	}
	r.Files = f.Files
	r.Rubric = strings.TrimSpace(string(body))
	if r.Rubric == "" {
		r.Err = "the rubric (the text after the frontmatter) is empty"
		return r
	}
	if r.Description == "" {
		r.Description = rubricTitle(r.Rubric)
	}
	r.RubricHash = shortHash([]byte(r.Rubric))
	return r
}

// SplitFrontmatter splits "---\n<yaml>\n---\n<body>".
func SplitFrontmatter(data []byte) (front, body []byte, ok bool) {
	s := bytes.TrimLeft(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), " \t\r\n")
	if !bytes.HasPrefix(s, []byte("---")) {
		return nil, nil, false
	}
	rest := s[3:]
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return nil, nil, false
	}
	front = rest[:end]
	body = rest[end+4:]
	if i := bytes.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = nil
	}
	return front, body, true
}

// ContentHash identifies a file's text as the review shows it (the diff's
// new-side lines). A verdict recorded for another hash is stale. The CLI and the
// server hash the same split (ReadLines / the diff), so they agree.
func ContentHash(lines []string) string {
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func shortHash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:16]
}

// rubricTitle is a rubric's first line, without Markdown heading/list markers.
func rubricTitle(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.TrimLeft(s, "#*- "))
}

// JudgePrompt is the request the reviewer's "Run judges" queues to the agent: a
// file-level comment listing each judge's rubric and the exact command that
// records its verdict. store is the review's STORE path (the agent passes it as
// --out); contentHash is the file's ContentHash as the review shows it, carried
// in each command with the rubric's hash so the verdict records exactly what was
// judged (the CLI never has to resolve or re-read the file).
func JudgePrompt(file, store, contentHash string, judges []*Rule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Judge `%s` against %d rubric(s). For each one, read the whole file, decide pass or fail, and record the verdict with the exact command below — one call per judge. Do **not** edit the file and do **not** propose suggestions for this request; then mark this comment done.\n\n", file, len(judges))
	b.WriteString("A verdict is binary: pass only if the file clearly meets the rubric. The critique says why in one or two sentences; when it is about a specific passage, add `--quote` with that exact text from the file so the reviewer can jump to it.\n")
	for _, j := range judges {
		fmt.Fprintf(&b, "\n### %s\n\n%s\n\n```\nprereview judge --out %s --file %s --judge %s --content-hash %s --rubric-hash %s --verdict pass|fail --critique \"…\" [--quote \"exact text\"] [--model <your model id>]\n```\n",
			j.Name, j.Rubric, shellQuote(store), shellQuote(file), j.ID, contentHash, j.RubricHash)
	}
	return b.String()
}

// shellQuote single-quotes s when it holds anything a shell would split on.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ValidateDrafted checks a checker (rule YAML) or judge (rubric Markdown) that an
// agent drafted for a learning, before prereview writes it to your checks
// folder: it must parse, and a checker may not be a command check — that runs a
// program on your files, so command checks stay hand-written. judge selects the
// rubric form.
func ValidateDrafted(data []byte, judge bool) error {
	if judge {
		if j := ParseJudge("drafted", SourceUser, "", data); j.Err != "" {
			return fmt.Errorf("judge: %s", j.Err)
		}
		return nil
	}
	var head struct {
		Extends string `yaml:"extends"`
	}
	_ = yaml.Unmarshal(data, &head) // a malformed file is reported by ParseRule below
	r := ParseRule("drafted", SourceUser, "", data)
	switch {
	case head.Extends == TypeCommand || r.Type == TypeCommand:
		return fmt.Errorf("checker: a drafted checker can't be a command check (it would run a program); use existence, substitution, occurrence or vocab")
	case r.Err != "":
		return fmt.Errorf("checker: %s", r.Err)
	}
	return nil
}
