package review

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// hidden.go records which suggestions the reviewer has HIDDEN from view (a
// declutter beyond the global toggle: hide a single suggestion or a whole group
// of alternatives). Like suggestion-decisions.jsonl it is SERVER-owned — the
// reviewer hides in the browser, and suggestions.jsonl is agent-owned,
// append-only, so it can't carry reviewer state — and it's atomically rewritten
// on every change. Hiding is a pure VIEW filter: it never counts as a reject and
// never reaches the LLM.
//
// Each entry carries a Fingerprint of the suggestion content it was hidden
// against, so a hide auto-invalidates when the LLM revises that suggestion: the
// new content no longer matches, the suggestion reappears (mirrors how a decision
// drops on revision — fresh work is never silently swallowed, the same guarantee
// #116 makes for the global toggle).

// HiddenSuggestionFileName is the fixed name of the server-owned hidden-set file
// under .prereview/. Durable across launches (openStore does NOT reset it).
const HiddenSuggestionFileName = "hidden-suggestions.jsonl"

// HiddenSuggestion marks one suggestion hidden from view, pinned to the content
// it was hidden against so a later revision un-hides it automatically.
type HiddenSuggestion struct {
	SuggestionID string `json:"suggestion_id"`
	Fingerprint  string `json:"fingerprint"`
}

// HiddenSuggestionPath returns the hidden-set path for a store whose CSV lives at
// csvPath — i.e. <csv dir>/hidden-suggestions.jsonl.
func HiddenSuggestionPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), HiddenSuggestionFileName)
}

// newHidden pins a hide to the suggestion's current content fingerprint.
func newHidden(sg Suggestion) HiddenSuggestion {
	return HiddenSuggestion{SuggestionID: sg.ID, Fingerprint: suggestionFingerprint(sg)}
}

// loadHidden reads the hidden-set file, deduped by SuggestionID (last write wins).
// Tolerant like loadDecisions: a missing file yields nil and any torn/blank line
// is skipped — a review must never break on it. First-seen order is kept.
func loadHidden(path string) []HiddenSuggestion {
	f, err := os.Open(path)
	if err != nil {
		return nil // missing (common) or unreadable → nothing hidden
	}
	defer f.Close()
	order := make([]string, 0, 16)
	byID := make(map[string]HiddenSuggestion)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var h HiddenSuggestion
		if err := json.Unmarshal(line, &h); err != nil || h.SuggestionID == "" {
			continue // torn/partial/blank/id-less — skip
		}
		if _, seen := byID[h.SuggestionID]; !seen {
			order = append(order, h.SuggestionID)
		}
		byID[h.SuggestionID] = h
	}
	out := make([]HiddenSuggestion, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// applyHidden loads the server-owned hidden set into state (like Decisions — the
// file is the source of truth, reloaded every Mount). Fingerprint gating happens
// at render time, in visibleSuggestions.
func (c *PrereviewController) applyHidden(state *PrereviewState) {
	state.Hidden = loadHidden(c.hiddenPath())
}

// hiddenPath is the .prereview/hidden-suggestions.jsonl path for this store.
func (c *PrereviewController) hiddenPath() string {
	return HiddenSuggestionPath(c.CSVPath)
}

// writeHidden atomically rewrites the hidden-set file with exactly list (temp +
// fsync + rename), so a reader never sees a half-written file and the server stays
// the sole writer. Mirrors writeDecisions.
func writeHidden(path string, list []HiddenSuggestion) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".prereview-hidden-*.tmp")
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
	for i := range list {
		if err := enc.Encode(list[i]); err != nil {
			tmp.Close()
			return fmt.Errorf("encode hidden %s: %w", list[i].SuggestionID, err)
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
