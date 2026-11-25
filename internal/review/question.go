package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/livetemplate/prereview/csv"
)

// Agent questions: the agent asks the reviewer to pick a direction, or to fill
// in missing or ambiguous data, from two to four options, one of them
// recommended. The reviewer answers with a tap instead of a typed reply.
//
// The storage copies the quiz's split (see quiz.go), one writer per file:
//
//   - .prereview/questions.jsonl        is AGENT-owned and append-only (written
//     by `prereview ask`), deduped by set id, last write wins.
//   - .prereview/question-answers.jsonl is SERVER-owned: the reviewer's picks,
//     rewritten atomically.
const (
	QuestionsFileName       = "questions.jsonl"
	QuestionAnswersFileName = "question-answers.jsonl"
)

// The limits follow Claude Code's own options picker (AskUserQuestion): up to
// four questions in steps, two to four options each.
const (
	maxQuestionSteps   = 4
	minQuestionOptions = 2
	maxQuestionOptions = 4
)

// askKinds are the anchors a question set may take. Live-site regions and
// image areas are not supported yet.
var askKinds = []string{QuestionKindLine, QuestionKindText, QuestionKindFile}

// QuestionSet is one `prereview ask` payload: up to four questions about one
// place, answered as steps and delivered to the agent together.
type QuestionSet struct {
	ID string `json:"id"`
	// For names the comment whose thread the set belongs to; empty for a set
	// anchored on its own.
	For  string `json:"for,omitempty"`
	File string `json:"file"`
	Kind string `json:"kind"` // line (default) | text | file

	FromLine int    `json:"from_line"`
	ToLine   int    `json:"to_line"`
	FromCol  int    `json:"from_col,omitempty"`
	ToCol    int    `json:"to_col,omitempty"`
	Side     string `json:"side"` // "new" (default) | "old"

	Steps     []QuestionStep `json:"steps"`
	CreatedAt string         `json:"created_at,omitempty"`

	// AnchorStatus is derived against the change index, never written.
	AnchorStatus string `json:"-"`
}

// QuestionStep is one question of a set.
type QuestionStep struct {
	ID     string `json:"id"`
	Header string `json:"header,omitempty"` // a short label for the step chip
	Prompt string `json:"prompt"`
	// Multi lets the reviewer pick any number of options instead of one.
	Multi   bool             `json:"multi,omitempty"`
	Options []QuestionOption `json:"options"`
	// Recommended is the 0-based index of the option the agent recommends, or
	// nil for none.
	Recommended *int `json:"recommended,omitempty"`
}

// QuestionOption is one choice of a step.
type QuestionOption struct {
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// QuestionAnswer is the reviewer's answer to one step, in the server-owned
// answers file: the indexes picked, or a typed Other answer. Sent is set on
// every step of a set once the reviewer sends it to the agent.
type QuestionAnswer struct {
	SetID  string `json:"set_id"`
	StepID string `json:"step_id"`
	Picked []int  `json:"picked,omitempty"`
	Other  string `json:"other,omitempty"`
	Sent   bool   `json:"sent,omitempty"`
	At     int64  `json:"at"` // UnixNano, matching ThreadEntry
}

// answered reports whether a carries an answer: a pick or a typed one.
func (a QuestionAnswer) answered() bool { return len(a.Picked) > 0 || a.Other != "" }

// NormalizeQuestionSet fills the defaults the agent may omit, so a payload
// round-trips through the loader unchanged. Applied by `prereview ask` before
// validation.
func NormalizeQuestionSet(s QuestionSet) QuestionSet {
	if s.ID == "" {
		s.ID = newCommentID()
	}
	if s.Kind == "" {
		s.Kind = QuestionKindLine
	}
	if s.Side == "" {
		s.Side = "new"
	}
	if s.Kind != QuestionKindFile && s.ToLine < s.FromLine {
		s.ToLine = s.FromLine
	}
	for i := range s.Steps {
		if s.Steps[i].ID == "" {
			s.Steps[i].ID = fmt.Sprintf("q%d", i+1)
		}
	}
	return s
}

// ValidateQuestionSet enforces the structure of a set. It does not check that
// the lines exist or that For names a real comment: only the review holds
// those (the CLI checks For; the server grounds the lines).
func ValidateQuestionSet(s QuestionSet) error {
	if !filepath.IsLocal(s.File) {
		return fmt.Errorf("\"file\" %q must be a path relative to the repo, inside it", s.File)
	}
	if !slices.Contains(askKinds, s.Kind) {
		return fmt.Errorf("unknown kind %q (want one of %v)", s.Kind, askKinds)
	}
	if s.Kind != QuestionKindFile && s.FromLine < 1 {
		return fmt.Errorf("a %q question needs \"from_line\" >= 1 (use kind %q for the file as a whole)", s.Kind, QuestionKindFile)
	}
	if s.Kind == QuestionKindText && s.ToLine == s.FromLine && s.ToCol < s.FromCol {
		return fmt.Errorf("\"to_col\" (%d) precedes \"from_col\" (%d)", s.ToCol, s.FromCol)
	}
	if n := len(s.Steps); n == 0 || n > maxQuestionSteps {
		return fmt.Errorf("needs 1 to %d questions in \"steps\", got %d", maxQuestionSteps, n)
	}
	seen := make(map[string]bool, len(s.Steps))
	for i, st := range s.Steps {
		where := fmt.Sprintf("question %d (%s)", i+1, st.ID)
		if seen[st.ID] {
			return fmt.Errorf("%s: duplicate id", where)
		}
		seen[st.ID] = true
		if st.Prompt == "" {
			return fmt.Errorf("%s: missing \"prompt\"", where)
		}
		if n := len(st.Options); n < minQuestionOptions || n > maxQuestionOptions {
			return fmt.Errorf("%s: needs %d to %d options, got %d (the reviewer can always type something else)", where, minQuestionOptions, maxQuestionOptions, n)
		}
		for j, o := range st.Options {
			if o.Label == "" {
				return fmt.Errorf("%s: option %d has no \"label\"", where, j+1)
			}
		}
		if r := st.Recommended; r != nil && (*r < 0 || *r >= len(st.Options)) {
			return fmt.Errorf("%s: \"recommended\" %d is out of range (0..%d)", where, *r, len(st.Options)-1)
		}
	}
	return nil
}

// QuestionsPath and QuestionAnswersPath are the two files beside the CSV.
func QuestionsPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), QuestionsFileName)
}

func QuestionAnswersPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), QuestionAnswersFileName)
}

// LoadQuestionSets reads the agent's question sets for the store whose CSV
// lives at csvPath: last write per id, tolerant of torn lines like the quiz.
func LoadQuestionSets(csvPath string) []QuestionSet {
	var all []QuestionSet
	readJSONL(QuestionsPath(csvPath), func(line []byte) {
		var s QuestionSet
		if json.Unmarshal(line, &s) != nil || s.ID == "" || len(s.Steps) == 0 {
			return
		}
		// The writer normalizes too; a hand-written line gets the same defaults.
		all = append(all, NormalizeQuestionSet(s))
	})
	return keepLastWrite(all, func(s QuestionSet) string { return s.ID })
}

// questionAnswerKey keys an answer by set and step: step ids are only unique
// within their set.
func questionAnswerKey(setID, stepID string) string { return setID + "\x00" + stepID }

// loadQuestionAnswers reads the reviewer's answers, keyed by questionAnswerKey.
func loadQuestionAnswers(path string) map[string]QuestionAnswer {
	out := map[string]QuestionAnswer{}
	readJSONL(path, func(line []byte) {
		var a QuestionAnswer
		if json.Unmarshal(line, &a) != nil || a.SetID == "" || a.StepID == "" {
			return
		}
		out[questionAnswerKey(a.SetID, a.StepID)] = a
	})
	return out
}

// saveQuestionAnswers rewrites the answers file atomically. Safe because the
// server is its only writer.
func saveQuestionAnswers(path string, answers map[string]QuestionAnswer) error {
	return saveJSONLMap(path, answers)
}

// saveJSONLMap rewrites path as one JSON line per map value, sorted by key so
// the file is stable and diffable, through a temp file and a rename. For the
// server-owned answer files, which have one writer.
func saveJSONLMap[T any](path string, m map[string]T) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, k := range keys {
		if err := enc.Encode(m[k]); err != nil {
			return fmt.Errorf("encode %s: %w", k, err)
		}
	}
	return writeFileAtomicVersions(path, buf.Bytes())
}

// StreamQuestion is a question set as the agent reads it in a snapshot: where
// it is, and the reviewer's answer to each step, by label.
type StreamQuestion struct {
	ID           string                 `json:"id"`
	For          string                 `json:"for,omitempty"`
	File         string                 `json:"file"`
	FromLine     int                    `json:"from_line"`
	ToLine       int                    `json:"to_line"`
	Side         string                 `json:"side"`
	AnchorStatus string                 `json:"anchor_status,omitempty"`
	Answers      []StreamQuestionAnswer `json:"answers"`
	Thread       []StreamReply          `json:"thread,omitempty"`
}

// StreamQuestionAnswer is one step's answer. Picked carries the option
// labels, so the agent never maps indexes back to its payload.
type StreamQuestionAnswer struct {
	Step   string   `json:"step"`
	Prompt string   `json:"prompt"`
	Picked []string `json:"picked,omitempty"`
	Other  string   `json:"other,omitempty"`
}

// actionableQuestions is what the agent must act on: every set the reviewer has
// sent and the agent has not marked done, plus any set whose thread ends with
// the reviewer. A set that is not sent yet never ships: its answers may still
// change.
func actionableQuestions(sets []QuestionSet, answers map[string]QuestionAnswer, done map[string]bool, threadByID map[string][]ThreadEntry) []StreamQuestion {
	var out []StreamQuestion
	for _, s := range sets {
		thread := threadByID[s.ID]
		sent := questionSetSent(s, answers)
		if !hasUnreadReviewerReply(thread) && (!sent || done[s.ID]) {
			continue
		}
		q := StreamQuestion{ID: s.ID, For: s.For, File: s.File, FromLine: s.FromLine, ToLine: s.ToLine,
			Side: s.Side, AnchorStatus: s.AnchorStatus, Answers: []StreamQuestionAnswer{}, Thread: toStreamThread(thread)}
		if sent {
			for _, st := range s.Steps {
				a := answers[questionAnswerKey(s.ID, st.ID)]
				sa := StreamQuestionAnswer{Step: st.ID, Prompt: st.Prompt, Other: a.Other}
				for _, i := range a.Picked {
					if i >= 0 && i < len(st.Options) {
						sa.Picked = append(sa.Picked, st.Options[i].Label)
					}
				}
				q.Answers = append(q.Answers, sa)
			}
		}
		out = append(out, q)
	}
	return out
}

// questionSetSent reports whether the reviewer has sent set s: every step is
// answered and marked sent.
func questionSetSent(s QuestionSet, answers map[string]QuestionAnswer) bool {
	for _, st := range s.Steps {
		a, ok := answers[questionAnswerKey(s.ID, st.ID)]
		if !ok || !a.Sent || !a.answered() {
			return false
		}
	}
	return true
}

// The kinds of item an id can name in a review store. Comments, suggestions,
// quiz questions and question sets share one id space: threads and done marks
// key on the bare id.
const (
	TargetComment      = "comment"
	TargetSuggestion   = "suggestion"
	TargetQuizQuestion = "quiz question" // addressed "<quizID>:<questionID>"
	TargetQuestionSet  = "question set"
)

// ResolveTarget reports which kind of item id names in the store whose CSV is
// at csvPath, or "" for none. empty reports that the store holds no item of
// any kind, which nearly always means a wrong --out.
func ResolveTarget(csvPath, id string) (kind string, empty bool) {
	comments, _ := csv.Read(csvPath)
	sugs := LoadSuggestions(csvPath)
	quizzes := LoadQuizzes(csvPath)
	sets := LoadQuestionSets(csvPath)
	switch {
	case slices.ContainsFunc(comments, func(r csv.Row) bool { return r.ID == id }):
		return TargetComment, false
	case slices.ContainsFunc(sugs, func(s Suggestion) bool { return s.ID == id }):
		return TargetSuggestion, false
	case slices.ContainsFunc(sets, func(s QuestionSet) bool { return s.ID == id }):
		return TargetQuestionSet, false
	}
	if qid, quest, ok := SplitQuizThreadID(id); ok {
		for _, q := range quizzes {
			if q.ID == qid && slices.ContainsFunc(q.Questions, func(qu Question) bool { return qu.ID == quest }) {
				return TargetQuizQuestion, false
			}
		}
	}
	return "", len(comments) == 0 && len(sugs) == 0 && len(quizzes) == 0 && len(sets) == 0
}
