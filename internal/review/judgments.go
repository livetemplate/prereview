package review

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/livetemplate/prereview/internal/checks"
)

// Judges (checks M2). A judge is a Markdown rubric in the check library; the
// coding agent answers "Run judges" requests with `prereview judge`, which
// appends verdicts to the AGENT-owned judgments.jsonl. The reviewer's Agree /
// Disagree answers go to the SERVER-owned judge-labels.jsonl (one writer per
// file, like quiz.jsonl / quiz-answers.jsonl). Both are append-only and loaded
// last-write-wins, so a re-run or a changed answer simply appends.
const (
	JudgmentsFileName   = "judgments.jsonl"
	JudgeLabelsFileName = "judge-labels.jsonl"
)

// Judgment is one verdict, the agent-facing schema of judgments.jsonl.
type Judgment struct {
	File        string `json:"file"`  // repo-relative
	Judge       string `json:"judge"` // the judge's check ID
	Verdict     string `json:"verdict"`
	Critique    string `json:"critique"`
	Quote       string `json:"quote,omitempty"` // exact text the critique is about
	Model       string `json:"model,omitempty"`
	ContentHash string `json:"content_hash"` // checks.ContentHash of the file judged
	RubricHash  string `json:"rubric_hash"`  // the rubric version judged against
	At          string `json:"at"`           // RFC 3339
}

// JudgeLabel is the reviewer's answer to one verdict: the calibration label M5
// scores judges against.
type JudgeLabel struct {
	File  string `json:"file"`
	Judge string `json:"judge"`
	At    string `json:"at"`    // the verdict's At, so a re-run asks again
	Label string `json:"label"` // agree | disagree
	When  string `json:"when"`
}

// Judge labels.
const (
	LabelAgree    = "agree"
	LabelDisagree = "disagree"
)

// JudgmentsPath / JudgeLabelsPath sit beside comments.csv in the review's store.
func JudgmentsPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), JudgmentsFileName)
}

func JudgeLabelsPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), JudgeLabelsFileName)
}

// judgmentKey identifies a verdict slot: one judge on one file.
func judgmentKey(file, judge string) string { return file + "\x00" + judge }

// labelKey identifies one verdict's label.
func labelKey(file, judge, at string) string { return file + "\x00" + judge + "\x00" + at }

// loadJudgments reads the agent's verdicts, latest per (file, judge). Tolerant:
// a missing file is no verdicts and a torn line is skipped — the file is
// agent-appended and a review must never break on it.
func loadJudgments(path string) map[string]Judgment {
	out := map[string]Judgment{}
	readJSONL(path, func(line []byte) {
		var j Judgment
		if json.Unmarshal(line, &j) != nil || j.File == "" || j.Judge == "" || !ValidVerdict(j.Verdict) {
			return
		}
		out[judgmentKey(j.File, j.Judge)] = j
	})
	return out
}

// loadJudgeLabels reads the reviewer's answers, latest per verdict.
func loadJudgeLabels(path string) map[string]string {
	out := map[string]string{}
	readJSONL(path, func(line []byte) {
		var l JudgeLabel
		if json.Unmarshal(line, &l) != nil || l.File == "" || l.Judge == "" {
			return
		}
		switch l.Label {
		case LabelAgree, LabelDisagree:
			out[labelKey(l.File, l.Judge, l.At)] = l.Label
		case "":
			delete(out, labelKey(l.File, l.Judge, l.At)) // an answer taken back
		}
	})
	return out
}

func readJSONL(path string, each func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if line := sc.Bytes(); len(line) > 0 {
			each(line)
		}
	}
}

// ValidVerdict reports one of the only verdicts: binary, with a critique (no scores).
func ValidVerdict(v string) bool { return v == checks.StatusPass || v == checks.StatusFail }

// AppendJSONL appends one JSON line per value to path, creating it: the
// sidecar format every agent verb and the server use. The lines go out in one
// write, so a reader never sees half of a batch. No values writes nothing.
func AppendJSONL(path string, vs ...any) error {
	if len(vs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, v := range vs {
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// AppendJudgment validates and records a verdict (the `prereview judge` write
// path). The caller supplies the hashes it computed from the file and rubric.
func AppendJudgment(csvPath string, j Judgment) error {
	if !ValidVerdict(j.Verdict) {
		return fmt.Errorf("verdict must be pass or fail, got %q", j.Verdict)
	}
	if strings.TrimSpace(j.Critique) == "" {
		return fmt.Errorf("a verdict needs a critique (why it passed or failed)")
	}
	if j.At == "" {
		j.At = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return AppendJSONL(JudgmentsPath(csvPath), j)
}

// judgeResult turns a judge rule and its latest verdict into the Result the
// checks views render: not run, stale (the file or the rubric changed since),
// or the verdict. contentHash is the file's checks.ContentHash as shown; lines
// is read only to locate a current verdict's quote, which becomes its one
// finding so the file view can mark it and jump to it.
func judgeResult(r *checks.Rule, j Judgment, ok bool, contentHash string, lines func() []string) checks.Result {
	if r.Err != "" {
		return checks.ErrorResult(r, r.Err)
	}
	res := checks.NewResult(r)
	switch {
	case !ok:
		res.Status = checks.StatusNotRun
		return res
	}
	res.Critique, res.Model, res.JudgedAt = j.Critique, j.Model, j.At
	if j.ContentHash != contentHash || j.RubricHash != r.RubricHash {
		res.Status = checks.StatusStale
		return res
	}
	res.Status = j.Verdict
	if j.Quote != "" { // read the text only for a verdict that quotes it
		if f, found := quoteFinding(lines(), j.Quote); found {
			f.Message, f.Severity = j.Critique, checks.LevelSuggestion
			res.Findings = []checks.Finding{f}
		}
	}
	return res
}

// quoteFinding locates quote in lines (first occurrence; may span lines, with
// the quote's own line breaks or a single space standing for one) and returns
// its span in rune columns, the coordinates every finding uses.
func quoteFinding(lines []string, quote string) (checks.Finding, bool) {
	q := strings.TrimSpace(quote)
	if q == "" {
		return checks.Finding{}, false
	}
	text := strings.Join(lines, "\n")
	idx := strings.Index(text, q)
	if idx < 0 {
		// tolerate the agent re-flowing a multi-line passage onto one line
		flat := strings.ReplaceAll(text, "\n", " ")
		if idx = strings.Index(flat, q); idx < 0 {
			return checks.Finding{}, false
		}
	}
	start, end := runePos(text, idx), runePos(text, idx+len(q))
	return checks.Finding{Line: start.line, FromCol: start.col, EndLine: end.line, ToCol: end.col, Match: q}, true
}

type linePos struct{ line, col int }

// runePos converts a byte offset in text (lines joined by \n) to a 1-based line
// and a 0-based rune column.
func runePos(text string, off int) linePos {
	p := linePos{line: 1}
	for _, r := range text[:off] {
		if r == '\n' {
			p.line++
			p.col = 0
			continue
		}
		p.col++
	}
	return p
}
