package main

import (
	"encoding/json"
	"strings"

	"github.com/livetemplate/prereview/gitdiff"
)

// Comment anchoring solves "comments from a previous version of the doc
// get misplaced". Comments are stored by raw line integers; the
// new-side file is re-read live every load, so any edit above a comment
// slides it onto the wrong content. We capture a small content
// fingerprint at comment time and, on load, re-locate it:
//
//   - exact text still uniquely present  -> shift the comment (moved)
//   - text unchanged at the same lines   -> no-op (ok)
//   - gone / ambiguous / changed         -> flag (outdated), DON'T guess
//
// "Confident-heal hybrid": we only move a comment on an exact,
// uniquely-placed (context-disambiguated) match, because a wrong guess
// would silently misdirect the skill. No fuzzy/edit-distance matching.

const (
	anchorOK       = "ok"
	anchorMoved    = "moved"
	anchorOutdated = "outdated"
	anchorContextN = 3 // lines of before/after context kept for disambiguation
)

// CommentAnchor is the content fingerprint captured when a comment is
// created/edited. Serialized as the opaque `anchor` JSON CSV column —
// the skill must not parse it.
type CommentAnchor struct {
	Text   string   `json:"text"`             // normalized join of FromLine..ToLine
	Before []string `json:"before,omitempty"` // <=3 normalized lines before FromLine (source order)
	After  []string `json:"after,omitempty"`  // <=3 normalized lines after ToLine (source order)
}

// Empty reports whether there is nothing to re-locate against (legacy
// pre-migration comments, or a capture that found no content).
func (a CommentAnchor) Empty() bool { return a.Text == "" }

// JSON serializes the anchor for the opaque `anchor` CSV column.
// Empty anchor → "" (keeps legacy/zero rows clean).
func (a CommentAnchor) JSON() string {
	if a.Empty() {
		return ""
	}
	b, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseAnchor is the inverse of JSON; a malformed/empty blob yields a
// zero anchor (relocation then skips that comment — never a crash).
func parseAnchor(s string) CommentAnchor {
	if s == "" {
		return CommentAnchor{}
	}
	var a CommentAnchor
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return CommentAnchor{}
	}
	return a
}

// relocateComments re-anchors every comment whose File == file against
// diff, in place. Returns true if any comment's range or status
// changed (so the caller can self-heal the CSV).
func relocateComments(comments []Comment, file string, diff *gitdiff.FileDiff) bool {
	changed := false
	for i := range comments {
		if comments[i].File != file {
			continue
		}
		if relocate(diff, &comments[i]) {
			changed = true
		}
	}
	return changed
}

// normLine canonicalizes a line for content comparison: collapse every
// whitespace run (incl. leading/trailing) to a single space. Case is
// significant (prose/code). One-sentence-per-line markdown stays
// near-unique under this, which is what makes exact matching reliable.
func normLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// sideContent returns the file's lines for the given side in source
// order; index i holds line number i+1. The new side is the live
// working tree; the old side is the base blob. side "both" anchors
// against the new side (that is what drifts under working-tree edits).
func sideContent(diff *gitdiff.FileDiff, side string) []string {
	if diff == nil {
		return nil
	}
	useOld := side == "old"
	out := make([]string, 0, len(diff.Lines))
	for _, l := range diff.Lines {
		n := l.NewNum
		if useOld {
			n = l.OldNum
		}
		if n <= 0 {
			continue
		}
		out = append(out, l.Content)
	}
	return out
}

// joinNorm normalizes lines[from1..to1] (1-based inclusive) and joins
// with "\n". Returns "" if the range is out of bounds.
func joinNorm(lines []string, from1, to1 int) string {
	if from1 < 1 || to1 < from1 || to1 > len(lines) {
		return ""
	}
	parts := make([]string, 0, to1-from1+1)
	for i := from1; i <= to1; i++ {
		parts = append(parts, normLine(lines[i-1]))
	}
	return strings.Join(parts, "\n")
}

// captureAnchor fingerprints the content at [from,to] (1-based) on the
// given side of diff, so the comment can be re-located after edits.
// Returns an empty anchor when there is no content to anchor (caller
// then simply skips re-location for that comment).
func captureAnchor(diff *gitdiff.FileDiff, from, to int, side string) CommentAnchor {
	lines := sideContent(diff, side)
	text := joinNorm(lines, from, to)
	if text == "" {
		return CommentAnchor{}
	}
	before := make([]string, 0, anchorContextN)
	for i := from - anchorContextN; i < from; i++ {
		if i >= 1 && i <= len(lines) {
			before = append(before, normLine(lines[i-1]))
		}
	}
	after := make([]string, 0, anchorContextN)
	for i := to + 1; i <= to+anchorContextN; i++ {
		if i >= 1 && i <= len(lines) {
			after = append(after, normLine(lines[i-1]))
		}
	}
	return CommentAnchor{Text: text, Before: before, After: after}
}

// neighborScore counts how many of want (already normalized) match the
// live lines starting at start1 (1-based), walking with step (+1 for
// the "after" window, -1 for the "before" window). want is in source
// order; for the before-window we compare nearest-first.
func neighborScore(lines []string, start1, step int, want []string) int {
	score := 0
	pos := start1
	// Compare the window closest-to-the-anchor first so a partial
	// context still scores the lines that matter most.
	for i := range want {
		w := want[len(want)-1-i] // nearest neighbor first
		if step > 0 {
			w = want[i]
		}
		if pos < 1 || pos > len(lines) {
			break
		}
		if normLine(lines[pos-1]) == w {
			score++
		}
		pos += step
	}
	return score
}

// relocate re-anchors c against the current diff. It mutates
// c.FromLine/c.ToLine and c.AnchorStatus and reports whether anything
// changed (so the caller can decide to self-heal the CSV). Resolved
// comments and comments without an anchor are left untouched.
func relocate(diff *gitdiff.FileDiff, c *Comment) bool {
	if c.Resolved || c.Anchor.Empty() || diff == nil {
		return false
	}
	lines := sideContent(diff, c.Side)
	span := max(c.ToLine-c.FromLine, 0)
	prevStatus := c.AnchorStatus

	// Fast path: content still at the recorded position. Note `moved`
	// is STICKY — once a comment has drifted from where the user
	// authored it, that stays surfaced (the SSR+WS double-Mount would
	// otherwise settle it back to ok within one page load, so the badge
	// would never be seen). It clears only via an explicit user action
	// (Edit re-capture / Re-anchor). outdated→ok here means the content
	// genuinely came back to the recorded lines.
	if joinNorm(lines, c.FromLine, c.ToLine) == c.Anchor.Text {
		if c.AnchorStatus != anchorMoved {
			c.AnchorStatus = anchorOK
		}
		return c.AnchorStatus != prevStatus
	}

	// Find every position where the exact anchored text still appears.
	var starts []int
	for i := 1; i+span <= len(lines); i++ {
		if joinNorm(lines, i, i+span) == c.Anchor.Text {
			starts = append(starts, i)
		}
	}

	switch len(starts) {
	case 0:
		c.AnchorStatus = anchorOutdated
		return c.AnchorStatus != prevStatus
	case 1:
		return c.moveTo(starts[0], span, prevStatus)
	default:
		// Duplicate content: disambiguate with the before/after context
		// window. Move only if exactly one candidate scores highest.
		best, bestScore, tie := -1, -1, false
		for _, s := range starts {
			sc := neighborScore(lines, s-1, -1, c.Anchor.Before) +
				neighborScore(lines, s+span+1, +1, c.Anchor.After)
			switch {
			case sc > bestScore:
				best, bestScore, tie = s, sc, false
			case sc == bestScore:
				tie = true
			}
		}
		if tie || bestScore <= 0 {
			c.AnchorStatus = anchorOutdated
			return c.AnchorStatus != prevStatus
		}
		return c.moveTo(best, span, prevStatus)
	}
}

// moveTo shifts the comment to start1 (keeping its span) and marks it
// moved (or ok if it did not actually move). Reports whether the
// line range or status changed.
func (c *Comment) moveTo(start1, span int, prevStatus string) bool {
	newFrom, newTo := start1, start1+span
	moved := newFrom != c.FromLine || newTo != c.ToLine
	c.FromLine, c.ToLine = newFrom, newTo
	if moved {
		c.AnchorStatus = anchorMoved
	} else {
		c.AnchorStatus = anchorOK
	}
	return moved || c.AnchorStatus != prevStatus
}
