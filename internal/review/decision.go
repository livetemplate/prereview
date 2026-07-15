package review

import (
	"bufio"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// decision.go records the reviewer's verdict on each LLM suggestion (issue #98
// Phase 2): accept / reject. Unlike the
// agent-owned suggestions.jsonl, the decisions file is SERVER-owned — the reviewer
// makes the call in the browser — so the server is its sole writer and rewrites it
// atomically on every change (a decision is mutable: accept→reject, or cleared).
// It's the mirror of comments.csv (server-owned, atomic rewrite) but for the
// reverse-direction signal.
//
// Nothing is applied to the reviewed files here. A recorded decision is PENDING
// until the reviewer hands off (Phase 3), at which point the batch ships to the
// LLM, which applies the accepted edits itself — prereview stays read-only.
//
// A decision carries a Fingerprint of the suggestion content it was made against,
// so it auto-invalidates when the suggestion changes: if the LLM revises a
// suggestion (re-appends the same id with new proposed text — see loadSuggestions),
// the old "accepted"/"rejected" no longer matches and the suggestion reads as
// undecided again. This is the anchor-drift idea applied to decisions — a stale
// verdict can never silently ride a changed proposal.

// SuggestionDecisionFileName is the fixed name of the server-owned decisions file
// under .prereview/. Durable across launches (openStore does NOT reset it).
const SuggestionDecisionFileName = "suggestion-decisions.jsonl"

// Decision verdicts. accept/reject are STORED on a decision; "revert" is
// wire-output only (#159 M4.2) — a revert-pending accept is emitted to the agent
// with this verdict, never stored.
const (
	verdictAccept = "accept"
	verdictReject = "reject"
	verdictRevert = "revert"
)

// SuggestionDecision is the reviewer's recorded verdict on one suggestion.
type SuggestionDecision struct {
	SuggestionID string    `json:"suggestion_id"`
	Verdict      string    `json:"verdict"` // accept | reject
	Note         string    `json:"note,omitempty"`
	// Auto marks a reject that was a SIDE EFFECT of accepting another alternative
	// in the same group (#117), as opposed to a reject the reviewer clicked. It
	// lets ClearSuggestionDecision re-open the whole group when the accept is
	// undone (radio-button semantics). Invisible to the LLM — a reject is a reject.
	Auto        bool      `json:"auto,omitempty"`
	// Revert marks that the reviewer asked to UNDO an already-applied accept (#159
	// M4.2): the verdict stays "accept" but the agent must restore the original text
	// on disk. Set by RequestRevert, surfaced to the agent as verdict="revert", and
	// self-clears once the agent acks (`reverted` drops the suggestion out of the
	// applied set → DecisionsBySuggestion filters the now-revert-complete decision
	// back to undecided).
	Revert      bool      `json:"revert,omitempty"`
	Fingerprint string    `json:"fingerprint"` // of the suggestion content decided on
	Created     time.Time `json:"created"`
}

// newDecision builds a decision stamped with the suggestion's current content
// fingerprint (so it auto-invalidates if the suggestion later changes) + time.
func newDecision(sg Suggestion, verdict, note string, auto bool) SuggestionDecision {
	return SuggestionDecision{
		SuggestionID: sg.ID,
		Verdict:      verdict,
		Note:         note,
		Auto:         auto,
		Fingerprint:  suggestionFingerprint(sg),
		Created:      time.Now().UTC(),
	}
}

// upsertDecision returns list with any existing decision for d.SuggestionID
// replaced by d (last-write-wins for one id), keeping the rest.
func upsertDecision(list []SuggestionDecision, d SuggestionDecision) []SuggestionDecision {
	list = slices.DeleteFunc(list, func(x SuggestionDecision) bool { return x.SuggestionID == d.SuggestionID })
	return append(list, d)
}

// suggestionFingerprint hashes the decided content (Original + Proposed) so a
// decision survives an identical re-append (idempotent) but drops the moment the
// suggestion's text changes. A short non-cryptographic hash is plenty — this only
// needs to detect change, not resist forgery.
func suggestionFingerprint(s Suggestion) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s.OriginalText))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(s.ProposedText))
	return fmt.Sprintf("%016x", h.Sum64())
}

// SuggestionDecisionPath returns the decisions-file path for a store whose CSV
// lives at csvPath — i.e. <csv dir>/suggestion-decisions.jsonl.
func SuggestionDecisionPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), SuggestionDecisionFileName)
}

// loadDecisions reads the decisions file into a slice, deduped by SuggestionID
// (last write wins). Tolerant like loadSuggestions: a missing file yields nil and
// any torn/blank line is skipped — a review must never break on it. First-seen
// order is kept (irrelevant to the map consumers, but stable for tests).
func loadDecisions(path string) []SuggestionDecision {
	f, err := os.Open(path)
	if err != nil {
		return nil // missing (common) or unreadable → no decisions
	}
	defer f.Close()
	order := make([]string, 0, 16)
	byID := make(map[string]SuggestionDecision)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var d SuggestionDecision
		if err := json.Unmarshal(line, &d); err != nil || d.SuggestionID == "" || d.Verdict == "" {
			continue // torn/partial/blank/incomplete — skip
		}
		if d.Verdict == "revise" {
			// #168: the "revise" verdict was removed. Drop any legacy on-disk row so
			// it can never reach the UI/stream/queue — the suggestion reads as
			// undecided again, and a reply on its thread is the replacement path.
			continue
		}
		if _, seen := byID[d.SuggestionID]; !seen {
			order = append(order, d.SuggestionID)
		}
		byID[d.SuggestionID] = d
	}
	out := make([]SuggestionDecision, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// writeDecisions atomically rewrites the decisions file with exactly decisions
// (temp + fsync + rename), so a reader never sees a half-written file and the
// server stays the sole writer. Mirrors the comments CSV writer's durability.
func writeDecisions(path string, decisions []SuggestionDecision) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".prereview-decisions-*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	enc := json.NewEncoder(tmp)
	for i := range decisions {
		if err := enc.Encode(decisions[i]); err != nil {
			tmp.Close()
			return fmt.Errorf("encode decision %s: %w", decisions[i].SuggestionID, err)
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename tmp -> %s: %w", path, err)
	}
	tmpName = ""
	return nil
}

// applyDecisions loads the server-owned decisions into state (like Comments from
// the CSV — the file is the source of truth, reloaded every Mount). The
// fingerprint gating happens later, at render time, in DecisionsBySuggestion.
func (c *PrereviewController) applyDecisions(state *PrereviewState) {
	state.Decisions = loadDecisions(c.decisionsPath())
}

// decisionsPath is the .prereview/suggestion-decisions.jsonl path for this store.
func (c *PrereviewController) decisionsPath() string {
	return SuggestionDecisionPath(c.CSVPath)
}
