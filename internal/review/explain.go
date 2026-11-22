package review

import (
	"embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
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
	// Concerns group the steps (A′): each is declared once and steps name it.
	Concerns []Concern `json:"concerns,omitempty"`
	// Diagrams are mermaid flows of the change, each citing the lines it shows.
	Diagrams []Diagram `json:"diagrams,omitempty"`
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
	// Concern is the id of the Concern this step belongs to, if any.
	Concern string `json:"concern,omitempty"`
	// Risks flag what deserves a careful read. They never reorder anything.
	Risks []Risk `json:"risks,omitempty"`
	// Impact points at code outside the diff the change affects (callers,
	// tests). Context only: never a walk stop, never coverage.
	Impact []ImpactAnchor `json:"impact,omitempty"`
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

// Concern is one thing the change does, which a run of steps explains. A
// mechanical concern (a rename, a moved block, generated code) is one whose
// lines can fold out of the way.
type Concern struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Mechanical bool   `json:"mechanical,omitempty"`
}

// Risk kinds a step can flag.
const (
	RiskAuth          = "auth"
	RiskConcurrency   = "concurrency"
	RiskDataMigration = "data-migration"
	RiskErrorHandling = "error-handling"
	RiskSecurity      = "security"
	RiskPerformance   = "performance"
	RiskPublicAPI     = "public-api"
)

var riskKinds = []string{
	RiskAuth, RiskConcurrency, RiskDataMigration, RiskErrorHandling, RiskSecurity, RiskPerformance, RiskPublicAPI,
}

// Risk flags a step for a careful read.
type Risk struct {
	Kind string `json:"kind"`
	Note string `json:"note,omitempty"`
}

// ImpactAnchor is a line range in the working tree, usually outside the diff,
// that the change affects. Contains, when set, must occur in the cited lines, so
// a stale or invented line number fails grounding.
type ImpactAnchor struct {
	File     string `json:"file"`
	FromLine int    `json:"from_line"`
	ToLine   int    `json:"to_line,omitempty"`
	Contains string `json:"contains,omitempty"`
	Note     string `json:"note,omitempty"`
	// Status is derived by the server's grounding, never part of the payload.
	Status string `json:"-"`
}

// Ungrounded reports that the impact anchor doesn't resolve in the working tree.
func (a ImpactAnchor) Ungrounded() bool { return a.Status == anchorUngrounded }

// anchor is the line anchor an impact anchor cites, so labels, defaults and
// path checks are the step anchors' own.
func (a ImpactAnchor) anchor() Anchor {
	return Anchor{File: a.File, Kind: AnchorKindLine, Side: "new", FromLine: a.FromLine, ToLine: a.ToLine}
}

// Label names the impact anchor as Anchor.Label does.
func (a ImpactAnchor) Label() string { return a.anchor().Label() }

// Diagram is a mermaid flow of the change. Its source is not checked; its
// anchors are grounded and listed under it, and never count as coverage.
type Diagram struct {
	ID      string   `json:"id"`
	Title   string   `json:"title,omitempty"`
	Mermaid string   `json:"mermaid"`
	Anchors []Anchor `json:"anchors,omitempty"`
}

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
		normalizeAnchors(s.Anchors)
		for j := range s.Impact {
			n := s.Impact[j].anchor()
			normalizeAnchor(&n)
			s.Impact[j].ToLine = n.ToLine
		}
	}
	for i := range e.Diagrams {
		d := &e.Diagrams[i]
		if d.ID == "" {
			d.ID = fmt.Sprintf("d%d", i+1)
		}
		normalizeAnchors(d.Anchors)
	}
	return e
}

func normalizeAnchors(as []Anchor) {
	for i := range as {
		normalizeAnchor(&as[i])
	}
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
	mechanical := make(map[string]bool, len(e.Concerns)) // concern id -> mechanical
	for i, c := range e.Concerns {
		where := fmt.Sprintf("concern %d (%s)", i+1, c.ID)
		switch {
		case c.ID == "":
			return fmt.Errorf("%s: missing \"id\"", where)
		case c.Title == "":
			return fmt.Errorf("%s: missing \"title\"", where)
		}
		if _, dup := mechanical[c.ID]; dup {
			return fmt.Errorf("%s: duplicate id", where)
		}
		mechanical[c.ID] = c.Mechanical
	}
	// A concern's steps must be contiguous, so a heading per concern never
	// reorders the walk. ended holds the concerns whose run is over.
	prev, ended := "", map[string]bool{}
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
		if err := validateAnchors(where+", anchor", s.Anchors); err != nil {
			return err
		}
		if s.Concern != prev {
			if s.Concern != "" && ended[s.Concern] {
				return fmt.Errorf("%s: concern %q's steps must be next to each other", where, s.Concern)
			}
			ended[prev] = true
			prev = s.Concern
		}
		if err := validateStepConcern(s, mechanical); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		for j, a := range s.Impact {
			if err := validateImpact(a); err != nil {
				return fmt.Errorf("%s, impact %d: %w", where, j+1, err)
			}
		}
	}
	diagrams := make(map[string]bool, len(e.Diagrams))
	for i, d := range e.Diagrams {
		where := fmt.Sprintf("diagram %d (%s)", i+1, d.ID)
		if diagrams[d.ID] {
			return fmt.Errorf("%s: duplicate id", where)
		}
		diagrams[d.ID] = true
		if strings.TrimSpace(d.Mermaid) == "" {
			return fmt.Errorf("%s: empty \"mermaid\" source", where)
		}
		if err := validateAnchors(where+", anchor", d.Anchors); err != nil {
			return err
		}
	}
	return nil
}

// validateAnchors checks each anchor, naming it as "<where> <n>".
func validateAnchors(where string, as []Anchor) error {
	for j, a := range as {
		if err := validateAnchor(a); err != nil {
			return fmt.Errorf("%s %d: %w", where, j+1, err)
		}
	}
	return nil
}

// validateStepConcern checks a step's concern and risks. A mechanical step can't
// carry a risk: its lines fold, and the fold would hide what the risk points at.
func validateStepConcern(s Step, mechanical map[string]bool) error {
	mech, named := mechanical[s.Concern]
	if s.Concern != "" && !named {
		return fmt.Errorf("unknown \"concern\" %q (declare it in \"concerns\")", s.Concern)
	}
	if mech && len(s.Risks) > 0 {
		return fmt.Errorf("a step in mechanical concern %q can't carry a risk (its lines fold)", s.Concern)
	}
	for j, r := range s.Risks {
		if !slices.Contains(riskKinds, r.Kind) {
			return fmt.Errorf("risk %d: unknown \"kind\" %q (want one of %s)", j+1, r.Kind, strings.Join(riskKinds, ", "))
		}
	}
	return nil
}

// validateImpact checks an impact anchor as the line anchor it is. Impact has no
// whole-file kind, so a missing line gets its own message.
func validateImpact(a ImpactAnchor) error {
	if a.FromLine < 1 {
		return fmt.Errorf("needs \"from_line\" >= 1")
	}
	return validateAnchor(a.anchor())
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
