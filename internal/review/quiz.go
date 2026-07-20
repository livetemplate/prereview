package review

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// quiz.go wires the #191 comprehension quiz: the reviewer asks for a quiz about
// the current diff, the agent authors it, and prereview renders it so answering
// becomes retrieval practice instead of a rubber-stamped `accept`.
//
// It is the same two-channel shape as suggestions, with the same ownership rule
// (see thread.go: never one file with two writers):
//
//   - .prereview/quiz.jsonl         is AGENT-owned and append-only (written by
//     `prereview quiz`, never rewritten by the server), so it cannot race
//     comments.csv. Durable across launches — openStore does NOT reset it.
//   - .prereview/quiz-answers.jsonl is SERVER-owned: the reviewer's answers,
//     rewritten atomically when they answer or retake.
//
// The two are merged at load, exactly like agent-replies.jsonl +
// reviewer-replies.jsonl.
//
// # Where the quality contract is enforced
//
// The prompt that produces a quiz is GUIDANCE and is user-replaceable (see
// LoadQuizPrompts), so no guarantee may depend on its text. The contract is
// enforced downstream instead, and binds every quiz whatever prompt produced it:
//
//	ValidateQuiz (here, called by the CLI verb) — structure: known probe, >= 2
//	    options, answer index in range, non-empty explanation, side set.
//	applyQuiz (the server, which holds the diff) — grounding: the cited line
//	    range actually resolves in the file under review.
//
// That split is what makes a user-authored prompt safe: a sloppy one yields a
// boring quiz, never a structurally invalid or hallucinated-anchor one.
const (
	// QuizFileName is the agent-written, append-only quiz file under .prereview/.
	// Durable across launches, deduped by quiz id (last write wins) so the agent
	// can revise a quiz by re-appending the same id.
	QuizFileName = "quiz.jsonl"
	// QuizAnswersFileName is the server-written record of the reviewer's answers.
	// Separate file because quiz.jsonl is the agent's; one writer per file.
	QuizAnswersFileName = "quiz-answers.jsonl"
)

// The probe taxonomy. The first three are adapted from the CodeReviewQA
// benchmark's reasoning steps (change type recognition / change localisation /
// solution identification); the last two are prereview's own.
//
// ProbeDecision is the one that targets rubber-stamping specifically: the other
// four ask whether the reviewer understood what IS in the diff, while `decision`
// asks whether they noticed something they might have OBJECTED to — an
// unrequested dependency, a changed default, a widened interface, a skipped edge
// case. It is diff-versus-intent rather than diff-internal, which is why only the
// agent that did the work can author it.
const (
	ProbeChangeType   = "change-type"
	ProbeLocalization = "localization"
	ProbeConsequence  = "consequence"
	ProbeRationale    = "rationale"
	ProbeDecision     = "decision"
)

// Question anchor kinds — the SAME vocabulary comments use (see csv/schema.go),
// so a quiz question is anchored exactly like every other annotation in
// prereview rather than inventing a parallel notion of "where this points".
//
// v1 renders `line` and `file`; `text`, `area` and `region` are accepted and
// stored so that adding their UI later is pure rendering work and never a
// migration of quizzes already on disk.
const (
	QuestionKindLine   = "line"   // a line range in the diff (the default)
	QuestionKindText   = "text"   // a character range within a line
	QuestionKindFile   = "file"   // the change as a whole, or something absent from it
	QuestionKindArea   = "area"   // a rectangle on an image
	QuestionKindRegion = "region" // a box on a live page (external mode)
)

var questionKinds = []string{
	QuestionKindLine, QuestionKindText, QuestionKindFile, QuestionKindArea, QuestionKindRegion,
}

// probeKinds is the accepted set, ordered for a stable error message.
var probeKinds = []string{
	ProbeChangeType, ProbeLocalization, ProbeConsequence, ProbeRationale, ProbeDecision,
}

// Question anchor states, DERIVED server-side by applyQuiz — never part of the
// agent's payload.
//
// quizAnchorAbsent and quizAnchorUngrounded must stay DISTINCT even though both
// render without a jump link: "absent" means the question is deliberately about
// something that isn't in the diff (a `decision` not to do a thing has no lines
// to point at), while "ungrounded" means the question CLAIMED an anchor that
// doesn't resolve — i.e. a hallucination. Collapsing them would let hallucinated
// anchors hide behind a legitimate-looking label, quietly defeating the check.
const (
	quizAnchorOK         = "ok"
	quizAnchorAbsent     = "absent"
	quizAnchorUngrounded = "ungrounded"
)

// Quiz is one generated comprehension quiz about one file's diff.
//
// The JSON tags are the agent-facing schema written to quiz.jsonl by
// `prereview quiz`. Fingerprint pins the quiz to the diff it was generated from
// so a later edit can mark it stale rather than silently quizzing the reviewer on
// code that has since changed.
type Quiz struct {
	ID          string     `json:"id"`
	File        string     `json:"file"`
	Prompt      string     `json:"prompt,omitempty"` // slug of the quiz prompt used
	Fingerprint string     `json:"fingerprint,omitempty"`
	Questions   []Question `json:"questions"`
	CreatedAt   string     `json:"created_at,omitempty"`
}

// Question is one multiple-choice item. Answer indexes into Options; Why is the
// explanation revealed after answering (immediate explanatory feedback is the
// headline recommendation in the retrieval-practice literature, so it is
// required, not optional).
//
// FromLine/ToLine/Side ground the question in the diff. Side is load-bearing and
// NOT redundant with the line numbers: a modified line exists on BOTH the old and
// new side, so a line number alone is ambiguous — the same reason Comment carries
// a side column. Without it the "jump to the cited code" link can land on the
// wrong row of a modified pair.
//
// FromLine == 0 means DELIBERATELY ANCHORLESS and is legal only for
// ProbeDecision, whose subject can be an omission ("chose not to add a test for
// the error path") with no lines to point at. Every other probe must anchor.
type Question struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"` // line (default) | text | file | area | region
	Probe   string   `json:"probe"`
	Prompt  string   `json:"prompt"`
	Options []string `json:"options"`
	Answer  int      `json:"answer"` // 0-based index into Options
	Why     string   `json:"why"`

	// Anchor, by kind — the same fields the CSV carries for comments.
	FromLine int    `json:"from_line"` // line/text
	ToLine   int    `json:"to_line"`
	FromCol  int    `json:"from_col"` // text
	ToCol    int    `json:"to_col"`
	Side     string `json:"side"`           // "new" (default) | "old"
	Area     *Area  `json:"area,omitempty"` // area: {x,y,w,h} 0..1 fractions
	URL      string `json:"url,omitempty"`  // region: the live page

	// AnchorStatus is DERIVED at load time by applyQuiz against the live diff —
	// kept off the JSON so the append-only file stays the agent's pure input.
	AnchorStatus string `json:"-"`
}

// LineAnchored reports that the question points at a line range — the kinds the
// grounding check applies to.
func (q Question) LineAnchored() bool {
	return q.Kind == QuestionKindLine || q.Kind == QuestionKindText
}

// Anchorless reports that the question carries no line anchor. It is now a
// property of the KIND (file/area/region) rather than a from_line==0 sentinel, so
// "this question is about the change as a whole, or about something absent from
// it" is stated in the same vocabulary comments use.
//
// Still distinct from Ungrounded: anchorless is a declared position, ungrounded
// is a claimed line that does not resolve.
func (q Question) Anchorless() bool { return !q.LineAnchored() }

// Ungrounded reports that the question claimed a line range that does not resolve
// in the current diff — a hallucinated anchor, rendered with a warning.
func (q Question) Ungrounded() bool { return q.AnchorStatus == quizAnchorUngrounded }

// QuizAnswer is one reviewer response, in the server-owned answers file. Choice
// indexes into the question's Options; -1 means cleared (a retake).
type QuizAnswer struct {
	QuizID     string `json:"quiz_id"`
	QuestionID string `json:"question_id"`
	Choice     int    `json:"choice"`
	At         int64  `json:"at"` // UnixNano, matching ThreadEntry
}

//go:embed builtin_quizzes/*.md
var builtinQuizzesFS embed.FS

// LoadQuizPrompts returns the embedded built-in quiz prompts overlaid with the
// user's library at userDir (~/.config/prereview/quizzes/*.md), sharing the
// suggestions library's loader and its rules: same "# Title" + body format, a
// user file OVERRIDES a built-in of the same slug, and anything unreadable is
// skipped rather than failing.
//
// User-authored quiz prompts are first-class on purpose. That is only safe
// because a prompt is guidance and never the guarantee — ValidateQuiz and the
// server's grounding check bind whatever prompt produced the quiz, so a custom
// prompt can make a quiz boring but not structurally invalid or hallucinated.
func LoadQuizPrompts(userDir string) []Prompt {
	return loadPromptLibrary(builtinQuizzesFS, "builtin_quizzes", userDir)
}

// QuizThreadID is a question's identity for THREADS. Question ids are only unique
// within their quiz ("q1" appears in every quiz), while a thread target id is
// global — so the two are joined. Colon-separated because it is typed by a human
// or an agent on the `prereview reply` command line, unlike the NUL-joined
// internal answer key.
func QuizThreadID(quizID, questionID string) string { return quizID + ":" + questionID }

// SplitQuizThreadID reverses QuizThreadID; ok is false when s is not a composite.
func SplitQuizThreadID(s string) (quizID, questionID string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// NewQuizID mints a stable, sortable id when the agent omits one. Exported
// because the CLI lives in the main package (mirrors NewSuggestionID).
func NewQuizID() string { return newCommentID() }

// QuizPath returns <csv dir>/quiz.jsonl. Centralised so the subcommand (main
// package), the watcher, and Mount all agree on one location.
func QuizPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), QuizFileName)
}

// QuizAnswersPath returns <csv dir>/quiz-answers.jsonl.
func QuizAnswersPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), QuizAnswersFileName)
}

// LoadQuizzes reads the agent-owned quizzes for the store whose CSV lives at
// csvPath.
func LoadQuizzes(csvPath string) []Quiz { return loadQuizzes(QuizPath(csvPath)) }

// loadQuizzes reads the append-only quiz file, deduped by ID (last write wins, so
// the agent can revise a quiz by re-appending the same id). Tolerant by design,
// exactly like loadSuggestions: a missing file yields nil and any torn or
// unparseable line is skipped rather than failing the load — the file is
// agent-appended and a review must never break on it. Order is stable (first-seen
// id order) so the UI doesn't reshuffle on each poll.
func loadQuizzes(path string) []Quiz {
	f, err := os.Open(path)
	if err != nil {
		return nil // missing (common) or unreadable → no quizzes
	}
	defer f.Close()
	order := make([]string, 0, 8)
	byID := make(map[string]Quiz)
	sc := bufio.NewScanner(f)
	// A quiz holds several questions with prose options and explanations, so give
	// the scanner room — a long one must never silently truncate.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var q Quiz
		if err := json.Unmarshal(line, &q); err != nil || q.ID == "" || q.File == "" {
			continue // torn/partial/blank/id-less line — skip, next may be fine
		}
		// Default Side at the READ boundary too, not just in NormalizeQuiz on the
		// write path (mirroring loadSuggestions). The file is agent-owned and
		// append-only, so a line written directly — bypassing `prereview quiz` —
		// would otherwise reach consumers with Side == "", and a question with no
		// side lands its jump link on the wrong row of a modified line.
		for i := range q.Questions {
			if q.Questions[i].Side == "" {
				q.Questions[i].Side = "new"
			}
		}
		if _, seen := byID[q.ID]; !seen {
			order = append(order, q.ID)
		}
		byID[q.ID] = q
	}
	out := make([]Quiz, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// LoadQuizAnswers reads the reviewer's answers for the store whose CSV lives at
// csvPath, keyed "<quizID>\x00<questionID>". A Choice of -1 (a cleared answer
// from a retake) is dropped at load, so callers see only live answers.
func LoadQuizAnswers(csvPath string) map[string]QuizAnswer {
	return loadQuizAnswers(QuizAnswersPath(csvPath))
}

// answerKey is the composite key for an answer: question ids are only unique
// within their quiz, so both parts are required. NUL-joined to avoid
// field-boundary collisions (same idiom as Suggestion.groupKey).
func answerKey(quizID, questionID string) string { return quizID + "\x00" + questionID }

func loadQuizAnswers(path string) map[string]QuizAnswer {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := make(map[string]QuizAnswer)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var a QuizAnswer
		if err := json.Unmarshal(line, &a); err != nil || a.QuizID == "" || a.QuestionID == "" {
			continue
		}
		k := answerKey(a.QuizID, a.QuestionID)
		if a.Choice < 0 {
			delete(out, k) // a retake cleared this one
			continue
		}
		out[k] = a
	}
	return out
}

// saveQuizAnswers rewrites the reviewer's answers atomically (temp + rename in
// the same dir), so the 750ms poller never sees a torn file. A full rewrite is
// safe here precisely because this file has ONE writer — the server. quiz.jsonl,
// which the agent appends to, is never rewritten.
func saveQuizAnswers(path string, answers map[string]QuizAnswer) error {
	// Sort by key so the file is stable across writes and diffable by hand.
	keys := make([]string, 0, len(answers))
	for k := range answers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, k := range keys {
		if err := enc.Encode(answers[k]); err != nil {
			return fmt.Errorf("encode answer %s: %w", k, err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".quiz-answers-*.tmp")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeds
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}

// ValidateQuiz enforces the STRUCTURAL half of the question contract. It lives
// here rather than in the CLI because it is the contract, not a CLI convenience:
// it must reject a bad quiz identically whether a built-in or a user-authored
// prompt produced it, and the review package is where that property is tested.
//
// It deliberately does NOT check that line ranges resolve — only the server holds
// the diff. That half is applyQuiz's job.
//
// Errors name the offending question so a failure is actionable rather than a
// bare "invalid", matching `prereview done`'s fail-loudly-with-the-id convention.
func ValidateQuiz(q Quiz) error {
	if q.File == "" {
		return fmt.Errorf("missing \"file\"")
	}
	if len(q.Questions) == 0 {
		return fmt.Errorf("quiz has no questions")
	}
	seen := make(map[string]bool, len(q.Questions))
	for i, qu := range q.Questions {
		if qu.ID == "" {
			return fmt.Errorf("question %d: missing \"id\"", i+1)
		}
		// Human-facing position (1-based) plus the id, so the agent can find it.
		where := fmt.Sprintf("question %d (%s)", i+1, qu.ID)
		if seen[qu.ID] {
			return fmt.Errorf("%s: duplicate id", where)
		}
		seen[qu.ID] = true
		if !slices.Contains(probeKinds, qu.Probe) {
			return fmt.Errorf("%s: unknown probe %q (want one of %v)", where, qu.Probe, probeKinds)
		}
		if qu.Prompt == "" {
			return fmt.Errorf("%s: missing \"prompt\"", where)
		}
		// Two options is the minimum that can discriminate at all; a single-option
		// "question" is not a question.
		if len(qu.Options) < 2 {
			return fmt.Errorf("%s: needs at least 2 options, got %d", where, len(qu.Options))
		}
		for j, opt := range qu.Options {
			if opt == "" {
				return fmt.Errorf("%s: option %d is empty", where, j+1)
			}
		}
		if qu.Answer < 0 || qu.Answer >= len(qu.Options) {
			return fmt.Errorf("%s: \"answer\" %d is out of range (0..%d)", where, qu.Answer, len(qu.Options)-1)
		}
		// Required, not optional: the explanation IS the learning payload. A quiz
		// that only scores you teaches nothing.
		if qu.Why == "" {
			return fmt.Errorf("%s: missing \"why\" (the explanation shown after answering)", where)
		}
		if !slices.Contains(questionKinds, qu.Kind) {
			return fmt.Errorf("%s: unknown kind %q (want one of %v)", where, qu.Kind, questionKinds)
		}
		// Each kind must actually carry its anchor. A line question without a line
		// is the case that matters: it would sail past the server's grounding check
		// by making no claim to falsify, which is exactly the dodge that check
		// exists to prevent. Use kind "file" to ask about the change as a whole.
		switch qu.Kind {
		case QuestionKindLine, QuestionKindText:
			if qu.FromLine < 1 {
				return fmt.Errorf("%s: a %q question needs \"from_line\" >= 1 (use kind %q to ask about the change as a whole, or about something absent from it)",
					where, qu.Kind, QuestionKindFile)
			}
			if qu.ToLine < qu.FromLine {
				return fmt.Errorf("%s: \"to_line\" (%d) precedes \"from_line\" (%d)", where, qu.ToLine, qu.FromLine)
			}
			if qu.Kind == QuestionKindText && qu.ToCol < qu.FromCol {
				return fmt.Errorf("%s: \"to_col\" (%d) precedes \"from_col\" (%d)", where, qu.ToCol, qu.FromCol)
			}
		case QuestionKindArea:
			if qu.Area == nil || (qu.Area.W == 0 && qu.Area.H == 0) {
				return fmt.Errorf("%s: an %q question needs a non-empty \"area\" rectangle", where, QuestionKindArea)
			}
		case QuestionKindRegion:
			if qu.URL == "" {
				return fmt.Errorf("%s: a %q question needs the page \"url\" it points at", where, QuestionKindRegion)
			}
		}
	}
	return nil
}

// NormalizeQuiz fills the defaults the agent may omit, so a payload round-trips
// through the loader unchanged. Applied by the CLI verb before validation.
func NormalizeQuiz(q Quiz) Quiz {
	if q.ID == "" {
		q.ID = NewQuizID()
	}
	for i := range q.Questions {
		qu := &q.Questions[i]
		if qu.ID == "" {
			qu.ID = fmt.Sprintf("q%d", i+1)
		}
		if qu.Kind == "" {
			// Line is the default, matching how the agent will nearly always anchor
			// and mirroring the comment CSV where "" reads as a line comment.
			qu.Kind = QuestionKindLine
		}
		if qu.Side == "" {
			qu.Side = "new"
		}
		if qu.LineAnchored() && qu.ToLine < qu.FromLine {
			qu.ToLine = qu.FromLine
		}
	}
	return q
}
