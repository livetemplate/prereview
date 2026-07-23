package review

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

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
	// anchorEdited: the recorded text is gone from its lines and appears nowhere
	// else, BUT the surrounding context still matches at the recorded position —
	// i.e. exactly these lines were edited in place. A deterministic "likely
	// addressed" signal, distinct from outdated (region deleted/restructured). It
	// stays actionable (a badge, not a drop): edited != correctly addressed.
	anchorEdited   = "edited"
	anchorContextN = 3 // lines of before/after context kept for disambiguation
)

// Comment kinds — mirror csv.ColKind values. "" / commentKindLine for
// line-anchored comments; commentKindFile for whole-file comments;
// commentKindArea for image-overlay annotations (rectangle in
// Comment.Area); commentKindRegion for live-site annotations in --external
// mode (rectangle in Comment.Area + page in Comment.URL).
const (
	commentKindLine   = "line"
	commentKindFile   = "file"
	commentKindArea   = "area"
	commentKindRegion = "region"
	// commentKindText anchors a comment to a character range within a
	// line (or across lines): FromLine/ToLine + FromCol/ToCol delimit the
	// exact selected substring. It reuses the line-anchor drift machinery
	// (Anchor.Text is still the whole-line join, so relocate shifts the
	// line range); Anchor.Snippet holds the exact selected substring for
	// sub-line re-location and disambiguation.
	commentKindText = "text"
)

// CommentAnchor is the content fingerprint captured when a comment is
// created/edited. Serialized as the opaque `anchor` JSON CSV column —
// the skill must not parse it.
type CommentAnchor struct {
	Text   string   `json:"text"`             // normalized join of FromLine..ToLine
	Before []string `json:"before,omitempty"` // <=3 normalized lines before FromLine (source order)
	After  []string `json:"after,omitempty"`  // <=3 normalized lines after ToLine (source order)
	// Snippet is the exact selected substring for kind=text comments
	// (raw, un-normalized, spanning FromCol..ToCol across FromLine..ToLine).
	// Empty for every other kind. Used to re-locate the character range
	// after edits and to disambiguate a substring that repeats on a line.
	Snippet string `json:"snippet,omitempty"`
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
// comments, file-level comments, area-level comments, region comments,
// and comments without a captured anchor are left untouched — there's
// nothing to drift against.
func relocate(diff *gitdiff.FileDiff, c *Comment) bool {
	if c.Resolved || c.IsFileLevel() || c.IsAreaLevel() || c.IsRegionLevel() || c.Anchor.Empty() || diff == nil {
		return false
	}
	lines := sideContent(diff, c.Side)
	changed := relocateLineRange(lines, &c.FromLine, &c.ToLine, &c.AnchorStatus, c.Anchor)
	// A kind=text comment's LINE range drifts via the line anchor above; its
	// sub-line COLUMNS then re-track by locating the exact selected snippet
	// within the (possibly moved) line — so inserting text earlier on the line
	// keeps the highlight on the right characters. Only for a placed
	// (non-outdated) single-line span; multi-line spans keep their columns.
	if c.IsTextLevel() && !c.AnchorOutdated() && relocateTextColumns(lines, c) {
		changed = true
	}
	return changed
}

// relocateLineRange re-anchors a line range (*fromLine/*toLine + *status,
// mutated through the given pointers) against the side's live lines via the
// whole-line content fingerprint in anchor. Reports whether the range or status
// changed. Sharing the engine through pointers (rather than a *Comment) lets
// suggestion drift (relocateSuggestion) reuse it verbatim, so the two never
// diverge.
func relocateLineRange(lines []string, fromLine, toLine *int, status *string, anchor CommentAnchor) bool {
	span := max(*toLine-*fromLine, 0)
	prevStatus := *status

	// Fast path: content still at the recorded position. Note `moved`
	// is STICKY — once an anchor has drifted from where it was
	// authored, that stays surfaced (the SSR+WS double-Mount would
	// otherwise settle it back to ok within one page load, so the badge
	// would never be seen). It clears only via an explicit user action
	// (Edit re-capture / Re-anchor). outdated→ok here means the content
	// genuinely came back to the recorded lines.
	if joinNorm(lines, *fromLine, *toLine) == anchor.Text {
		if *status != anchorMoved {
			*status = anchorOK
		}
		return *status != prevStatus
	}

	// Find every position where the exact anchored text still appears.
	var starts []int
	for i := 1; i+span <= len(lines); i++ {
		if joinNorm(lines, i, i+span) == anchor.Text {
			starts = append(starts, i)
		}
	}

	switch len(starts) {
	case 0:
		// Recorded text is gone from its lines and appears nowhere else. If the
		// IMMEDIATELY-adjacent neighbor on each side still sits where it was, exactly
		// these lines were EDITED in place: a deterministic "likely addressed".
		// Checking the nearest neighbor (not any context line) is what separates an
		// in-place edit from a deletion or a restructure — a deletion shifts the
		// after-neighbor up, a restructure changes the before-neighbor — and avoids a
		// far-off blank line coincidentally passing. A side with no recorded context
		// can't disagree, but at least one side must exist to judge.
		beforeOK := len(anchor.Before) == 0 ||
			(*fromLine >= 2 && normLine(lines[*fromLine-2]) == anchor.Before[len(anchor.Before)-1])
		afterOK := len(anchor.After) == 0 ||
			(*toLine < len(lines) && normLine(lines[*toLine]) == anchor.After[0])
		hasContext := len(anchor.Before) > 0 || len(anchor.After) > 0
		if hasContext && beforeOK && afterOK {
			*status = anchorEdited
		} else {
			*status = anchorOutdated
		}
		return *status != prevStatus
	case 1:
		return moveRangeTo(fromLine, toLine, status, starts[0], span, prevStatus)
	default:
		// Duplicate content: disambiguate with the before/after context
		// window. Move only if exactly one candidate scores highest.
		best, bestScore, tie := -1, -1, false
		for _, s := range starts {
			sc := neighborScore(lines, s-1, -1, anchor.Before) +
				neighborScore(lines, s+span+1, +1, anchor.After)
			switch {
			case sc > bestScore:
				best, bestScore, tie = s, sc, false
			case sc == bestScore:
				tie = true
			}
		}
		if tie || bestScore <= 0 {
			*status = anchorOutdated
			return *status != prevStatus
		}
		return moveRangeTo(fromLine, toLine, status, best, span, prevStatus)
	}
}

// moveRangeTo shifts [*fromLine,*toLine] to start1 (keeping span) and marks it
// moved (or ok if it did not actually move). Reports whether the line range or
// status changed.
func moveRangeTo(fromLine, toLine *int, status *string, start1, span int, prevStatus string) bool {
	newFrom, newTo := start1, start1+span
	moved := newFrom != *fromLine || newTo != *toLine
	*fromLine, *toLine = newFrom, newTo
	if moved {
		*status = anchorMoved
	} else {
		*status = anchorOK
	}
	return moved || *status != prevStatus
}

// relocateTextColumns re-locates a single-line kind=text comment's FromCol/ToCol
// by finding the exact selected snippet (Anchor.Snippet) inside its line's live
// content. Returns whether the columns changed. It is best-effort: no snippet,
// a multi-line span, or a snippet no longer found verbatim (e.g. whitespace on
// the line changed) leaves the columns as-is — the line anchor already placed
// the comment, and marking it outdated for a cosmetic column shift would be
// noisier than a slightly-off highlight.
func relocateTextColumns(lines []string, c *Comment) bool {
	if c.Anchor.Snippet == "" || c.FromLine != c.ToLine {
		return false
	}
	if c.FromLine < 1 || c.FromLine > len(lines) {
		return false
	}
	byteIdx := strings.Index(lines[c.FromLine-1], c.Anchor.Snippet)
	if byteIdx < 0 {
		return false
	}
	from := utf8.RuneCountInString(lines[c.FromLine-1][:byteIdx])
	to := from + utf8.RuneCountInString(c.Anchor.Snippet)
	if from == c.FromCol && to == c.ToCol {
		return false
	}
	c.FromCol, c.ToCol = from, to
	return true
}
