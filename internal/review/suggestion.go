package review

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/prereview/gitdiff"
)

// suggestion.go wires the LLM's INBOUND "here's an edit I propose" signal into
// the review UI (issue #98). It is the mirror image of the comment flow:
//
//   - a COMMENT is authored by the human in the browser, stored in the
//     server-owned comments.csv, and consumed by the LLM on hand-off.
//   - a SUGGESTION is authored by the LLM (via `prereview suggest`), stored in
//     the agent-owned, append-only .prereview/suggestions.jsonl, and consumed by
//     the human, who accepts / rejects / requests-revision (Phase 2). Those
//     decisions ship back to the LLM on the next hand-off (Phase 3), and the LLM
//     — not the server — applies the accepted edits. prereview stays read-only on
//     the reviewed files.
//
// Like processed.jsonl, suggestions.jsonl is agent-owned and never rewritten by
// the server (so it never races comments.csv), durable across launches (openStore
// does NOT reset it), and read on every Mount + on each change (the llm-status
// watcher covers it via agentSignalFingerprint). It's surfaced live: writing a
// suggestion makes its box appear inline with no server restart.

// SuggestionFileName is the fixed name of the agent-written, append-only
// suggestions file under .prereview/. Durable across launches.
const SuggestionFileName = "suggestions.jsonl"

// Suggestion is one proposed edit: replace the OriginalText currently at
// [FromLine,ToLine] (new side) with ProposedText. It is rendered inline as a
// suggestion box, visually distinct from a comment.
//
// The JSON tags are the agent-facing schema written to suggestions.jsonl by
// `prereview suggest` (snake_case, mirroring the CSV column names). FromLine/
// ToLine are the LLM's hint; the authoritative fingerprint is OriginalText,
// which is re-located in the live file on every load (see relocateSuggestion),
// so a suggestion drifts exactly like a comment and goes `outdated` when its
// target text is gone or ambiguous — it is never silently misplaced onto the
// wrong content.
type Suggestion struct {
	ID           string `json:"id"`
	File         string `json:"file"`
	FromLine     int    `json:"from_line"`
	ToLine       int    `json:"to_line"`
	Side         string `json:"side"` // "new" (default) | "old" | "both"
	OriginalText string `json:"original"`
	ProposedText string `json:"proposed"`
	Note         string `json:"note,omitempty"` // optional rationale from the LLM
	// Anchor/AnchorStatus are DERIVED at load time from OriginalText + the live
	// file (not part of the agent's payload) — the drift fingerprint. Kept off
	// the JSON so the append-only file stays the agent's pure input.
	Anchor       CommentAnchor `json:"-"`
	AnchorStatus string        `json:"-"`
}

// AnchorOutdated reports that the suggestion's OriginalText could no longer be
// confidently located — the reviewer/LLM must re-derive it. Parallels
// Comment.AnchorOutdated.
func (s Suggestion) AnchorOutdated() bool { return s.AnchorStatus == anchorOutdated }

// AnchorMoved reports that the suggestion was auto-shifted to follow its target
// text after the file changed (informational). Parallels Comment.AnchorMoved.
func (s Suggestion) AnchorMoved() bool { return s.AnchorStatus == anchorMoved }

// LineSpan renders "L42" / "L42-L48" for the card badge, matching Comment's
// line-level span (suggestions are always line-anchored).
func (s Suggestion) LineSpan() string {
	if s.FromLine == s.ToLine {
		return fmt.Sprintf("L%d", s.FromLine)
	}
	return fmt.Sprintf("L%d-L%d", s.FromLine, s.ToLine)
}

// NewSuggestionID mints a stable, sortable suggestion ID for the `suggest`
// subcommand when the agent omits one (same scheme as comment IDs). Exported
// because the CLI lives in the main package.
func NewSuggestionID() string { return newCommentID() }

// SuggestionPath returns the suggestions-file path for a store whose CSV lives at
// csvPath — i.e. <csv dir>/suggestions.jsonl, alongside processed.jsonl. Centralised
// so the subcommand (main package), the watcher, and Mount all agree on one
// location.
func SuggestionPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), SuggestionFileName)
}

// loadSuggestions reads the append-only suggestions file into a slice, deduped by
// ID (last write wins, so the LLM can revise a suggestion by re-appending the same
// ID). Tolerant by design, exactly like loadProcessedIDs: a missing file yields
// nil, and any unparseable/torn line is skipped rather than failing the load — the
// file is agent-appended and a review must never break on it. Order is stable
// (first-seen ID order) so the UI doesn't reshuffle on each poll.
func loadSuggestions(path string) []Suggestion {
	f, err := os.Open(path)
	if err != nil {
		return nil // missing (common) or unreadable → no suggestions
	}
	defer f.Close()
	order := make([]string, 0, 16)
	byID := make(map[string]Suggestion)
	sc := bufio.NewScanner(f)
	// Proposed text can be multi-line-but-single-JSON-line and long; give the
	// scanner room so a big suggestion never silently truncates.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var s Suggestion
		if err := json.Unmarshal(line, &s); err != nil || s.ID == "" || s.File == "" {
			continue // torn/partial/blank/id-less line — skip, next may be fine
		}
		if s.Side == "" {
			s.Side = "new"
		}
		if _, seen := byID[s.ID]; !seen {
			order = append(order, s.ID)
		}
		byID[s.ID] = s
	}
	out := make([]Suggestion, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// normJoinText normalizes an OriginalText blob into the same "\n"-joined,
// whitespace-collapsed form joinNorm produces for a file's line range, so a
// suggestion's OriginalText can be matched against the live file by the shared
// relocate engine. A trailing newline is trimmed so the line count (and thus the
// anchored span) matches the file's.
func normJoinText(s string) string {
	trimmed := strings.TrimRight(s, "\n")
	if trimmed == "" {
		return ""
	}
	// Reuse the same per-line normalize+join the file-range engine uses, so a
	// suggestion's OriginalText and a file range canonicalize identically.
	lines := strings.Split(trimmed, "\n")
	return joinNorm(lines, 1, len(lines))
}

// relocateSuggestion re-anchors s against the current diff via its OriginalText
// fingerprint, mutating FromLine/ToLine/AnchorStatus. Returns whether anything
// changed. An empty OriginalText (a pure insertion) has nothing to drift against,
// so it is left at its stated line. Reuses the same line-range engine as comment
// drift so the two never diverge.
func relocateSuggestion(diff *gitdiff.FileDiff, s *Suggestion) bool {
	if s.Anchor.Empty() || diff == nil {
		return false
	}
	lines := sideContent(diff, s.Side)
	return relocateLineRange(lines, &s.FromLine, &s.ToLine, &s.AnchorStatus, s.Anchor)
}

// relocateSuggestions re-anchors every suggestion whose File == file against
// diff, in place. The suggestion analogue of relocateComments; no bool return
// because suggestions are never persisted (the file is agent-owned).
func relocateSuggestions(suggestions []Suggestion, file string, diff *gitdiff.FileDiff) {
	for i := range suggestions {
		if suggestions[i].File != file {
			continue
		}
		relocateSuggestion(diff, &suggestions[i])
	}
}

// applySuggestions loads the agent-written suggestions into state and derives each
// one's drift anchor from its OriginalText (stateless — recomputed every load,
// since the append-only file can't hold a server-captured anchor). The span is
// taken from OriginalText's line count so ToLine stays consistent with the content
// even if the LLM's hint was off. Actual re-location against the live file happens
// in relocateSuggestionsSelected once the selected diff is loaded. Cheap and
// safe to call from both Mount and the watcher fan-out (LLMStatusChanged).
func (c *PrereviewController) applySuggestions(state *PrereviewState) {
	sugs := loadSuggestions(c.suggestionsPath())
	for i := range sugs {
		text := normJoinText(sugs[i].OriginalText)
		sugs[i].Anchor = CommentAnchor{Text: text}
		sugs[i].AnchorStatus = anchorOK
		if text != "" {
			// Derive the span from the fingerprint so a from/to hint that
			// disagrees with the original's line count doesn't leave a stale span.
			span := strings.Count(text, "\n")
			sugs[i].ToLine = sugs[i].FromLine + span
		}
	}
	state.Suggestions = sugs
}

// relocateSuggestionsSelected re-anchors the selected file's suggestions against
// CurrentDiff (the live working-tree file), so a suggestion whose target text was
// edited away renders `outdated` and one whose text moved follows it. Only the
// selected file is relocated because that's the only file whose suggestions render
// (SuggestionsByEndLine filters by SelectedFile) — mirrors relocateSelected for
// comments. Best-effort and read-only: nothing is persisted (the file is
// agent-owned).
func (c *PrereviewController) relocateSuggestionsSelected(state *PrereviewState) {
	if state.CurrentDiff == nil || state.SelectedFile == "" {
		return
	}
	relocateSuggestions(state.Suggestions, state.SelectedFile, state.CurrentDiff)
}

// relocateSuggestionsAll re-anchors EVERY file's suggestions (loading each file's
// diff via the per-file cache), so the decisions emitted at hand-off carry
// accurate line numbers + anchor_status even for files the reviewer never opened —
// the suggestion analogue of relocateAll for comments. Used only at hand-off (the
// CSV/stream become a contract there). Read-only: nothing is persisted.
func (c *PrereviewController) relocateSuggestionsAll(state *PrereviewState) {
	base := c.effectiveBase(state)
	seen := map[string]bool{}
	for _, sg := range state.Suggestions {
		if sg.Anchor.Empty() || seen[sg.File] {
			continue
		}
		seen[sg.File] = true
		// Fresh (uncached) load so a suggestion whose target was just edited away
		// re-anchors `outdated` and drops from the snapshot — the mtime cache can
		// serve a stale diff for edits within one tick (part of #121).
		diff, err := c.loadDiffFresh(base, sg.File)
		if err != nil {
			slog.Warn("relocateSuggestionsAll: load diff", "file", sg.File, "base", base, "err", err)
			continue
		}
		relocateSuggestions(state.Suggestions, sg.File, diff)
	}
}

// suggestionsPath is the .prereview/suggestions.jsonl path for this session's store.
func (c *PrereviewController) suggestionsPath() string {
	return SuggestionPath(c.CSVPath)
}
