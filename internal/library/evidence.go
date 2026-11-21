package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/livetemplate/prereview/csv"
)

// Evidence kinds: the reviewer signals a learning can cite.
const (
	EvidenceComment    = "comment"
	EvidenceSuggestion = "suggestion"
	EvidenceReply      = "reply"
)

// Evidence is one real record a learning cites — a comment, a suggestion or a
// thread reply in a review store — resolved when the learning is proposed, so a
// learning can never cite nothing (the quiz's grounding rule, applied here).
type Evidence struct {
	Store string `json:"store"` // absolute store path
	ID    string `json:"id"`    // comment / suggestion id, or "<target id>@<at>" for a reply
	Kind  string `json:"kind"`
	File  string `json:"file,omitempty"`
	Line  int    `json:"line,omitempty"`
	Quote string `json:"quote,omitempty"` // the signal's text, for the card
	At    string `json:"at,omitempty"`    // when it was made (RFC 3339)
}

// Store layout: the review store's directory and sidecar names. The library
// owns them (it can't import internal/review); review and main alias them, so a
// rename can't leave Resolve, Scan and the signal count reading nothing.
const (
	StoreDir            = ".prereview"
	CommentsFile        = "comments.csv"
	SuggestionsFile     = "suggestions.jsonl"
	DecisionsFile       = "suggestion-decisions.jsonl"
	AgentRepliesFile    = "agent-replies.jsonl"
	ReviewerRepliesFile = "reviewer-replies.jsonl"
	QuizFile            = "quiz.jsonl"
	VersionsDirName     = "versions"
	TimelineFileName    = "timeline.jsonl"
	// TargetsDir is where a directory's per-target (single-file review) stores
	// live inside its .prereview (#199).
	TargetsDir = "files"
)

// ParseRef splits "store:id". The store is the longest-from-the-right prefix
// ending at a colon that is a directory, so a Windows drive letter ("C:\…") and
// a quiz reply's id ("<quizID>:<questionID>@<at>") both survive. When no prefix
// is a directory, the last colon splits, so the error names the same store.
func ParseRef(ref string) (store, id string, err error) {
	last := strings.LastIndex(ref, ":")
	if last <= 0 || last == len(ref)-1 {
		return "", "", fmt.Errorf("evidence %q: want <store>:<id>", ref)
	}
	for i := last; i > 0; i = strings.LastIndex(ref[:i], ":") {
		if fi, err := os.Stat(ref[:i]); err == nil && fi.IsDir() {
			return ref[:i], ref[i+1:], nil
		}
	}
	return ref[:last], ref[last+1:], nil
}

// Resolve finds the reviewer signal ref ("<store>:<id>") names and returns it as
// evidence, or an error naming what's missing. Evidence is only what the
// reviewer said or decided: a comment, their thread reply, or their decision on
// a suggestion. The agent's replies, suggestions and quiz questions are context,
// so citing one is refused with what to cite instead. It only reads the store.
func Resolve(ref string) (Evidence, error) {
	store, id, err := ParseRef(ref)
	if err != nil {
		return Evidence{}, err
	}
	abs, err := filepath.Abs(store)
	if err != nil {
		return Evidence{}, err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return Evidence{}, fmt.Errorf("evidence %q: no store at %s", ref, abs)
	}
	r := storeReader{abs}
	refuse := func(why string) (Evidence, error) { return Evidence{}, fmt.Errorf("evidence %q: %s", ref, why) }
	if target, atStr, ok := strings.Cut(id, "@"); ok {
		at, err := strconv.ParseInt(atStr, 10, 64)
		if err != nil {
			return refuse("a reply is cited as <target_id>@<at>, with <at> a number")
		}
		if body, ok := r.reply(ReviewerRepliesFile, target, at); ok {
			file, line := r.where(target)
			return Evidence{Store: abs, ID: id, Kind: EvidenceReply, File: file, Line: line, Quote: body, At: unixAt(at)}, nil
		}
		if _, ok := r.reply(AgentRepliesFile, target, at); ok {
			return refuse("that's the agent's reply, not a reviewer signal; cite the reviewer's reply or comment on it")
		}
		return refuse("no reviewer reply with that id in " + abs)
	}
	if c, ok := r.comment(id); ok {
		if c.IsRequest() {
			return refuse("that comment is a request to the agent, not a reviewer signal")
		}
		if imp, ok := Imports(r.dir)[id]; ok {
			return refuse("that comment was imported from " + imp.Source + ", so someone else wrote it; cite a comment the reviewer left in prereview")
		}
		return Evidence{Store: abs, ID: id, Kind: EvidenceComment, File: c.File, Line: c.FromLine,
			Quote: c.Body, At: c.CreatedAt.UTC().Format(time.RFC3339)}, nil
	}
	if sg, ok := r.suggestion(id); ok {
		d, ok := r.decision(id)
		if !ok {
			return refuse("that suggestion has no decision yet; cite it once the reviewer accepts or rejects it")
		}
		q := d.Verdict + "ed: " + sg.Proposed // accepted: / rejected:
		if d.Note != "" {
			q += " (" + d.Note + ")"
		}
		return Evidence{Store: abs, ID: id, Kind: EvidenceSuggestion, File: sg.File, Line: sg.FromLine, Quote: q,
			At: d.Created.UTC().Format(time.RFC3339)}, nil
	}
	if _, _, ok := r.question(id); ok {
		return refuse("that's the agent's quiz question, not a reviewer signal; cite the reviewer's reply on it")
	}
	return refuse("no comment, suggestion or reply with that id in " + abs)
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

// storeReader reads one review store's records for Resolve.
type storeReader struct{ dir string }

func (r storeReader) comment(id string) (csv.Row, bool) {
	rows, _ := csv.Read(filepath.Join(r.dir, CommentsFile))
	for _, c := range rows {
		if c.ID == id {
			return c, true
		}
	}
	return csv.Row{}, false
}

type storeSuggestion struct {
	ID       string `json:"id"`
	File     string `json:"file"`
	FromLine int    `json:"from_line"`
	Proposed string `json:"proposed"`
}

// suggestion is the latest record for id (re-submitting an id revises it).
func (r storeReader) suggestion(id string) (storeSuggestion, bool) {
	var found storeSuggestion
	ok := false
	eachLine(filepath.Join(r.dir, SuggestionsFile), func(b []byte) {
		var s storeSuggestion
		if json.Unmarshal(b, &s) == nil && s.ID == id {
			found, ok = s, true
		}
	})
	return found, ok
}

type storeDecision struct {
	SuggestionID string    `json:"suggestion_id"`
	Verdict      string    `json:"verdict"` // accept | reject
	Note         string    `json:"note"`
	Created      time.Time `json:"created"`
}

// decision is the reviewer's accept or reject on suggestion id (last line wins;
// a legacy "revise" line is skipped, as review's loadDecisions does).
func (r storeReader) decision(id string) (storeDecision, bool) {
	var found storeDecision
	ok := false
	eachLine(filepath.Join(r.dir, DecisionsFile), func(b []byte) {
		var d storeDecision
		if json.Unmarshal(b, &d) == nil && d.SuggestionID == id && (d.Verdict == "accept" || d.Verdict == "reject") {
			found, ok = d, true
		}
	})
	return found, ok
}

// reply finds the reply on target at at in a replies file.
func (r storeReader) reply(name, target string, at int64) (body string, ok bool) {
	eachLine(filepath.Join(r.dir, name), func(b []byte) {
		var rp storeReply
		if json.Unmarshal(b, &rp) == nil && rp.TargetID == target && rp.At == at && rp.RemoteID == "" {
			body, ok = rp.Body, true
		}
	})
	return body, ok
}

// storeReply is a thread entry in a replies file. RemoteID marks a reply
// imported from a remote copy (#209): someone else's, never evidence.
type storeReply struct {
	TargetID string `json:"target_id"`
	Body     string `json:"body"`
	At       int64  `json:"at"`
	RemoteID string `json:"remote_id,omitempty"`
}

// question finds a quiz question by its thread id ("<quizID>:<questionID>").
func (r storeReader) question(threadID string) (file string, line int, ok bool) {
	quizID, qID, isQuiz := SplitQuizThreadID(threadID)
	if !isQuiz {
		return "", 0, false
	}
	eachLine(filepath.Join(r.dir, QuizFile), func(b []byte) {
		var q struct {
			ID        string `json:"id"`
			File      string `json:"file"`
			Questions []struct {
				ID       string `json:"id"`
				FromLine int    `json:"from_line"`
			} `json:"questions"`
		}
		if json.Unmarshal(b, &q) != nil || q.ID != quizID {
			return
		}
		for _, qu := range q.Questions {
			if qu.ID == qID {
				file, line, ok = q.File, qu.FromLine, true
			}
		}
	})
	return file, line, ok
}

// where is the file and line of a reply's target: a comment, a suggestion or a
// quiz question.
func (r storeReader) where(target string) (string, int) {
	if c, ok := r.comment(target); ok {
		return c.File, c.FromLine
	}
	if sg, ok := r.suggestion(target); ok {
		return sg.File, sg.FromLine
	}
	file, line, _ := r.question(target)
	return file, line
}

// unixAt renders a unix timestamp (seconds, milliseconds or nanoseconds — replies
// use nanoseconds) as RFC 3339.
func unixAt(v int64) string {
	switch {
	case v == 0:
		return ""
	case v > 1e17:
		return time.Unix(0, v).UTC().Format(time.RFC3339)
	case v > 1e12:
		return time.UnixMilli(v).UTC().Format(time.RFC3339)
	default:
		return time.Unix(v, 0).UTC().Format(time.RFC3339)
	}
}
