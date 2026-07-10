package review

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// thread.go is the #149 conversation substrate: a thread is an ordered list of
// entries attached to a target (a comment ID or a suggestion ID — one shape for
// both). Entries live in two append-only sidecars merged at Mount, mirroring the
// processed.jsonl (agent) + reenqueued.jsonl (server) dual-writer idiom — never one
// file with two writers:
//
//   - agent-replies.jsonl    — agent-owned, appended by `prereview reply <id>`.
//   - reviewer-replies.jsonl — server-owned, appended by the browser (added in the
//                              reviewer-reply phase; the loader below already merges
//                              it so this phase forward-declares the name).
//
// This phase (agent→reviewer) loads and renders the agent side only.

const (
	// AgentRepliesFileName is the agent-written, append-only thread log. Durable
	// across relaunch (openStore does not reset it), like processed.jsonl.
	AgentRepliesFileName = "agent-replies.jsonl"
	// ReviewerRepliesFileName is the server-written thread log (reviewer-reply
	// phase). Forward-declared so the merge below is complete from the start.
	ReviewerRepliesFileName = "reviewer-replies.jsonl"
)

// Thread entry authors.
const (
	AuthorAgent    = "agent"
	AuthorReviewer = "reviewer"
)

// ThreadEntry is one message in a comment's or suggestion's thread. At is a
// nanosecond timestamp (time.Now().UnixNano()): the agent CLI and the server share
// the same box + clock, so an agent reply is naturally later than the reviewer
// prompt it answers, and (At, author) sorts stably.
type ThreadEntry struct {
	TargetID string `json:"target_id"`
	Author   string `json:"author"`
	Body     string `json:"body"`
	At       int64  `json:"at"`
}

// When renders At as a compact wall-clock time for the card (e.g. "3:04 PM").
func (e ThreadEntry) When() string {
	if e.At == 0 {
		return ""
	}
	return time.Unix(0, e.At).Format("3:04 PM")
}

// AgentRepliesPath / ReviewerRepliesPath resolve the two sidecars for a store whose
// CSV lives at csvPath — alongside processed.jsonl, so subcommand + Mount agree.
func AgentRepliesPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), AgentRepliesFileName)
}

func ReviewerRepliesPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), ReviewerRepliesFileName)
}

// loadThreadEntries reads an append-only ThreadEntry JSONL file. Tolerant exactly
// like loadSuggestions/loadMarkCounts: a missing file yields nil and a torn/blank/
// target-less line is skipped rather than failing the load — a review must never
// break on an agent-appended sidecar.
func loadThreadEntries(path string) []ThreadEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil // missing (common) or unreadable → no entries
	}
	defer f.Close()
	var out []ThreadEntry
	sc := bufio.NewScanner(f)
	// Reply bodies can be long; give the scanner room so one never truncates.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e ThreadEntry
		if err := json.Unmarshal(line, &e); err != nil || e.TargetID == "" || e.Body == "" {
			continue // torn/partial/blank line — skip, next may be fine
		}
		out = append(out, e)
	}
	return out
}

// loadThreads merges the agent + reviewer sidecars, sorted by (At, author) with the
// agent breaking ties AFTER the reviewer — an agent reply causally follows the
// reviewer prompt it answers, so on an exact-nanosecond tie it must still sort last.
func loadThreads(csvPath string) []ThreadEntry {
	entries := loadThreadEntries(AgentRepliesPath(csvPath))
	entries = append(entries, loadThreadEntries(ReviewerRepliesPath(csvPath))...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].At != entries[j].At {
			return entries[i].At < entries[j].At
		}
		// tie: reviewer before agent (agent answers the reviewer)
		return entries[i].Author == AuthorReviewer && entries[j].Author == AuthorAgent
	})
	return entries
}
