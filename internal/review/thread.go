package review

import (
	"bufio"
	"encoding/json"
	"fmt"
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

// groupThreads groups sorted thread entries by target ID (comment or suggestion),
// preserving chronological order within each group. Shared by the render path
// (Threads) and the snapshot path (LoadComments / EmitSnapshot) so both agree.
func groupThreads(entries []ThreadEntry) map[string][]ThreadEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string][]ThreadEntry)
	for _, e := range entries {
		out[e.TargetID] = append(out[e.TargetID], e)
	}
	return out
}

// hasUnreadReviewerReply reports whether a target's thread ends with a REVIEWER
// entry — i.e. the reviewer has spoken since the agent last did, so the agent owes a
// response. Because entries are sorted, "last entry is the reviewer's" is exactly
// "newer than the agent's last entry". Empty thread → false.
func hasUnreadReviewerReply(thread []ThreadEntry) bool {
	return len(thread) > 0 && thread[len(thread)-1].Author == AuthorReviewer
}

// trailingReviewerReplies counts the run of REVIEWER entries at the END of a thread — the
// replies the reviewer has posted since the agent last spoke, i.e. the messages still
// awaiting an agent response. Zero when the thread is empty or ends with the agent. Unlike
// hasUnreadReviewerReply (a yes/no "does this comment need the agent"), this is a tally:
// three reviewer replies in a row count as three (#164, the per-reply queue counter), and
// it drops back to zero the instant the agent replies.
func trailingReviewerReplies(thread []ThreadEntry) int {
	n := 0
	for i := len(thread) - 1; i >= 0; i-- {
		if thread[i].Author != AuthorReviewer {
			break
		}
		n++
	}
	return n
}

// threadActionable decides whether a comment/suggestion belongs in the agent's
// snapshot, given its thread and whether it is SETTLED — resolved by the reviewer OR
// outdated because the agent edited the anchored line (#164). The unread-reply model
// (#149), NOT auto-reopen:
//   - no thread yet      → actionable iff not settled (the fresh-comment case).
//   - thread, reviewer-last (unread) → actionable, even if settled: a fresh reviewer
//     reply steers the agent again, overriding resolved AND outdated. Without this, a
//     reply on a comment the agent edited was captured on disk but silently dropped.
//   - thread, agent-last → NOT actionable (the agent replied and is waiting on the
//     reviewer; without this it would re-act on a handled comment forever — the
//     exact gap a plain !settled filter leaves open).
func threadActionable(settled bool, thread []ThreadEntry) bool {
	if len(thread) == 0 {
		return !settled
	}
	return hasUnreadReviewerReply(thread)
}

// appendReviewerReply records a reviewer's thread entry in the server-owned
// reviewer-replies.jsonl (append-only, mirroring processed.go's appendMark). at is a
// nanosecond timestamp so it sorts against the agent's replies.
func appendReviewerReply(csvPath, targetID, body string, at int64) error {
	line, err := json.Marshal(ThreadEntry{TargetID: targetID, Author: AuthorReviewer, Body: body, At: at})
	if err != nil {
		return err
	}
	path := ReviewerRepliesPath(csvPath)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append %s: %w", filepath.Base(path), err)
	}
	return nil
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
