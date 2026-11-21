package review

import (
	"embed"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// explain.go wires the #115 walkthrough: the reviewer asks the agent to explain
// the whole change, the agent writes the steps in the order to read them, each
// citing the lines it explains, and prereview checks those lines and walks the
// reviewer through them.
//
// The files follow the quiz's ownership rule (see quiz.go, thread.go): never one
// file with two writers.
//
//   - .prereview/explain.jsonl       is AGENT-owned and append-only, written by
//     `prereview explain`. Durable across launches; deduped by id, last write
//     wins, so the agent revises an explanation by re-appending its id.
//   - .prereview/first-seen.json     is SERVER-owned: the diff fingerprint each
//     explanation was first seen against (firstseen.go), which is how the page
//     knows the files moved on after it was written. The agent can't record this
//     itself: the command doesn't know the review's base.
//
// As with the quiz, the guarantees are enforced downstream of the (replaceable)
// prompt: ValidateExplanation in the command checks the structure, and the
// server, which holds the diff, checks that every anchor resolves.
const ExplainFileName = "explain.jsonl"

// Anchor kinds and states. An anchor uses the comment vocabulary: a line range
// on one side, or a whole file (for a binary, or a step about a file as a whole).
const (
	AnchorKindLine = "line"
	AnchorKindFile = "file"
)

// Explanation is one walkthrough of the change.
type Explanation struct {
	ID        string `json:"id"`
	Summary   string `json:"summary,omitempty"`
	Steps     []Step `json:"steps"`
	CreatedAt string `json:"created_at,omitempty"`
	// Stale: the reviewed files changed after this explanation was first seen,
	// so it was written against an older tree. Derived, never written by the agent.
	Stale bool `json:"-"`
}

// Step is one stop on the walkthrough: what to look at and why.
type Step struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Body    string   `json:"body,omitempty"`
	Anchors []Anchor `json:"anchors"`
	// Derived marks a step prereview made (the "Not explained" step), never the
	// agent's: code that treats steps as agent text skips it.
	Derived bool `json:"-"`
}

// Anchor is a place in the change a step cites.
type Anchor struct {
	File     string `json:"file"`
	Kind     string `json:"kind,omitempty"` // line (default) | file
	Side     string `json:"side,omitempty"` // new (default) | old
	FromLine int    `json:"from_line,omitempty"`
	ToLine   int    `json:"to_line,omitempty"`
	// Status is derived by the server's grounding, never part of the payload.
	Status string `json:"-"`
}

// Ungrounded reports that the anchor cites a line (or file) the review doesn't
// have.
func (a Anchor) Ungrounded() bool { return a.Status == anchorUngrounded }

// Label names the anchor the way the rest of the UI names a span: the file, then
// L42 or L42-L48 (as Comment.LineSpan), marking the old side.
func (a Anchor) Label() string {
	if a.Kind == AnchorKindFile {
		return a.File
	}
	l := fmt.Sprintf("%s L%d", a.File, a.FromLine)
	if a.ToLine > a.FromLine {
		l += fmt.Sprintf("-L%d", a.ToLine)
	}
	if a.Side == "old" {
		l += " (old)"
	}
	return l
}

// Explanation first-seen keys are namespaced in first-seen.json.
func (e Explanation) seenKey() string { return "explain/" + e.ID }

// ExplainPath returns <csv dir>/explain.jsonl.
func ExplainPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), ExplainFileName)
}

// NormalizeExplanation fills the defaults the agent may omit, so a payload
// round-trips through the loader unchanged. The command applies it before
// validating.
func NormalizeExplanation(e Explanation) Explanation {
	if e.ID == "" {
		e.ID = newCommentID()
	}
	for i := range e.Steps {
		s := &e.Steps[i]
		if s.ID == "" {
			s.ID = fmt.Sprintf("s%d", i+1)
		}
		for j := range s.Anchors {
			normalizeAnchor(&s.Anchors[j])
		}
	}
	return e
}

func normalizeAnchor(a *Anchor) {
	if a.Kind == "" {
		a.Kind = AnchorKindLine
	}
	if a.Side == "" {
		a.Side = "new"
	}
	// Only a missing end is filled; a reversed range is the agent's mistake and
	// fails validation rather than being quietly narrowed to one line.
	if a.Kind == AnchorKindLine && a.ToLine == 0 {
		a.ToLine = a.FromLine
	}
}

// ValidateExplanation checks the structure the server and the template rely on.
// It never looks at the diff; grounding is the server's half. Errors name the
// step, so the agent can find what to fix.
func ValidateExplanation(e Explanation) error {
	if len(e.Steps) == 0 {
		return fmt.Errorf("explanation has no steps")
	}
	seen := make(map[string]bool, len(e.Steps))
	for i, s := range e.Steps {
		where := fmt.Sprintf("step %d (%s)", i+1, s.ID)
		if s.ID == notExplainedStepID {
			return fmt.Errorf("%s: id %q is reserved for prereview's own step", where, s.ID)
		}
		if seen[s.ID] {
			return fmt.Errorf("%s: duplicate id", where)
		}
		seen[s.ID] = true
		if s.Title == "" {
			return fmt.Errorf("%s: missing \"title\"", where)
		}
		// A step with nothing to point at makes no claim the server can check,
		// and gives the walkthrough nowhere to go.
		if len(s.Anchors) == 0 {
			return fmt.Errorf("%s: needs at least one anchor (the lines it explains; kind %q for a whole file)", where, AnchorKindFile)
		}
		for j, a := range s.Anchors {
			if err := validateAnchor(a); err != nil {
				return fmt.Errorf("%s, anchor %d: %w", where, j+1, err)
			}
		}
	}
	return nil
}

func validateAnchor(a Anchor) error {
	if a.File == "" {
		return fmt.Errorf("missing \"file\"")
	}
	// Repo-relative and inside the repo. The server checks the file is in the
	// review; this stops an absolute or ../ path before it is ever written.
	if !filepath.IsLocal(a.File) {
		return fmt.Errorf("\"file\" %q must be a path relative to the repo, inside it", a.File)
	}
	if a.Side != "new" && a.Side != "old" {
		return fmt.Errorf("unknown \"side\" %q (want new or old)", a.Side)
	}
	switch a.Kind {
	case AnchorKindFile:
	case AnchorKindLine:
		if a.FromLine < 1 {
			return fmt.Errorf("a line anchor needs \"from_line\" >= 1 (use kind %q for the whole file)", AnchorKindFile)
		}
		if a.ToLine < a.FromLine {
			return fmt.Errorf("\"to_line\" (%d) precedes \"from_line\" (%d)", a.ToLine, a.FromLine)
		}
	default:
		return fmt.Errorf("unknown \"kind\" %q (want %s or %s)", a.Kind, AnchorKindLine, AnchorKindFile)
	}
	return nil
}

// LoadExplanations reads the agent's explanations for the store whose CSV lives
// at csvPath.
func LoadExplanations(csvPath string) []Explanation {
	return loadExplanations(ExplainPath(csvPath))
}

// loadExplanations reads the append-only file, deduped by id. Unlike quizzes,
// order is by LAST write: the reviewer reads the newest explanation, and a
// revised one (an old id re-appended) is the newest. Tolerant like loadQuizzes: a
// torn or unparseable line is skipped, and the defaults are filled at this read
// boundary too, for a line written without the command.
func loadExplanations(path string) []Explanation {
	var all []Explanation
	last := map[string]int{} // id -> index of its last write in all
	readJSONL(path, func(line []byte) {
		var e Explanation
		if json.Unmarshal(line, &e) != nil || e.ID == "" || len(e.Steps) == 0 {
			return
		}
		last[e.ID] = len(all)
		all = append(all, NormalizeExplanation(e))
	})
	var out []Explanation
	for i, e := range all {
		if last[e.ID] == i {
			out = append(out, e)
		}
	}
	return out
}

//go:embed builtin_explain/*.md
var builtinExplainFS embed.FS

// LoadExplainPrompts returns the built-in walkthrough prompts overlaid with the
// user's (<library>/explain/*.md), with the shared prompt-library rules. As with
// quiz prompts, a custom one is safe: the structure and the grounding are
// enforced whatever prompt produced the walkthrough.
func LoadExplainPrompts(userDir string) []Prompt {
	return loadPromptLibrary(builtinExplainFS, "builtin_explain", userDir)
}
