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

// Store sidecar names (kept in step with internal/review, which owns them).
const (
	commentsFile     = "comments.csv"
	suggestionsFile  = "suggestions.jsonl"
	agentRepliesFile = "agent-replies.jsonl"
	reviewerReplies  = "reviewer-replies.jsonl"
	decisionsFile    = "suggestion-decisions.jsonl"
)

// ParseRef splits "store:id". The store is everything before the LAST colon, so
// a Windows drive letter ("C:\…") survives.
func ParseRef(ref string) (store, id string, err error) {
	i := strings.LastIndex(ref, ":")
	if i <= 0 || i == len(ref)-1 {
		return "", "", fmt.Errorf("evidence %q: want <store>:<id>", ref)
	}
	return ref[:i], ref[i+1:], nil
}

// Resolve finds the record ref ("<store>:<id>") names and returns it as
// evidence, or an error naming what's missing. It only reads the store.
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
	if rows, err := csv.Read(filepath.Join(abs, commentsFile)); err == nil {
		for _, r := range rows {
			if r.ID == id {
				if isRequest(r.Body) {
					return Evidence{}, fmt.Errorf("evidence %q: that comment is a request to the agent, not a reviewer signal", ref)
				}
				return Evidence{Store: abs, ID: id, Kind: EvidenceComment, File: r.File, Line: r.FromLine,
					Quote: r.Body, At: r.CreatedAt.UTC().Format(time.RFC3339)}, nil
			}
		}
	}
	var found *Evidence
	eachLine(filepath.Join(abs, suggestionsFile), func(b []byte) {
		var s struct {
			ID       string `json:"id"`
			File     string `json:"file"`
			FromLine int    `json:"from_line"`
			Proposed string `json:"proposed"`
			Note     string `json:"note"`
		}
		if json.Unmarshal(b, &s) == nil && s.ID == id {
			q := s.Note
			if q == "" {
				q = s.Proposed
			}
			found = &Evidence{Store: abs, ID: id, Kind: EvidenceSuggestion, File: s.File, Line: s.FromLine, Quote: q}
		}
	})
	if found != nil {
		return *found, nil
	}
	if target, at, ok := strings.Cut(id, "@"); ok {
		for _, name := range []string{reviewerReplies, agentRepliesFile} {
			eachLine(filepath.Join(abs, name), func(b []byte) {
				var r struct {
					TargetID string `json:"target_id"`
					Body     string `json:"body"`
					At       int64  `json:"at"`
				}
				if json.Unmarshal(b, &r) == nil && r.TargetID == target && strconv.FormatInt(r.At, 10) == at {
					found = &Evidence{Store: abs, ID: id, Kind: EvidenceReply, Quote: r.Body, At: unixAt(r.At)}
				}
			})
			if found != nil {
				return *found, nil
			}
		}
	}
	return Evidence{}, fmt.Errorf("evidence %q: no comment, suggestion or reply with that id in %s", ref, abs)
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
