package review

import (
	"fmt"
	"html/template"
	"path/filepath"

	"github.com/livetemplate/prereview/gitdiff"
)

// lineEndSentinel is a "to end of line" upper bound for a multi-line text
// comment's mark on its first / interior lines; gitdiff.MarkRanges clamps it to
// the actual line length.
const lineEndSentinel = 1 << 30

// CSVBasename returns just the filename portion of CSVPath — useful for
// compact toast/banner display where the full repo path is noise.
func (s PrereviewState) CSVBasename() string {
	return filepath.Base(s.CSVPath)
}

// SelectionEmpty reports whether nothing is currently selected.
func (s PrereviewState) SelectionEmpty() bool { return s.SelectionAnchor == 0 }

// CommentsByEndLine groups the current comments by their ToLine — the line
// the comment trails. The template renders each line N, then inlines any
// comment whose ToLine == N right after it (GitHub-mobile-style). Zero-arg
// for the same reason as SelectedLines: the livetemplate framework only
// pre-computes zero-arg methods into the data map.
//
// Restricted to the currently-selected file. Resolved comments are filtered
// out when ShowResolved is false so the diff stays focused on open issues.
func (s PrereviewState) CommentsByEndLine() map[int][]Comment {
	if s.SelectedFile == "" {
		return nil
	}
	out := make(map[int][]Comment)
	for _, c := range s.Comments {
		if c.File != s.SelectedFile {
			continue
		}
		if c.IsFileLevel() || c.IsAreaLevel() {
			continue
		}
		if s.commentHiddenFromView(c) {
			continue
		}
		out[c.ToLine] = append(out[c.ToLine], c)
	}
	return out
}

// visibleSuggestions is the SINGLE source every render surface iterates —
// SuggestionsByEndLine (line view), FileSuggestions (block view), and
// SuggestionGroups (the "N of M" count) all go through it, so a suggestion
// excluded here can never leak into one view while showing in another. (The hide
// feature filters individually-hidden suggestions here.)
func (s PrereviewState) visibleSuggestions() []Suggestion {
	return s.Suggestions
}

// SuggestionsByEndLine groups the selected file's suggestions by ToLine, so the
// template can inline each suggestion box right after its trailing line — exactly
// like CommentsByEndLine does for comments. Zero-arg (the framework only
// pre-computes zero-arg methods). Returns nil when suggestions are toggled off
// (HideSuggestions) so the whole surface disappears with one flag.
func (s PrereviewState) SuggestionsByEndLine() map[int][]Suggestion {
	if s.SelectedFile == "" || s.HideSuggestions {
		return nil
	}
	out := make(map[int][]Suggestion)
	for _, sg := range s.visibleSuggestions() {
		if sg.File != s.SelectedFile {
			continue
		}
		out[sg.ToLine] = append(out[sg.ToLine], sg)
	}
	return out
}

// SuggestionCount is the number of suggestions on the selected file, used to
// label the toolbar toggle and gate its visibility. Counts regardless of the
// HideSuggestions toggle (it's the total available, not the shown count).
func (s PrereviewState) SuggestionCount() int {
	if s.SelectedFile == "" {
		return 0
	}
	n := 0
	for _, sg := range s.Suggestions {
		if sg.File == s.SelectedFile {
			n++
		}
	}
	return n
}

// DecisionsBySuggestion maps each CURRENT suggestion's ID to its recorded decision,
// but ONLY when the decision's fingerprint still matches the suggestion's content.
// A same-id revision (new proposed text) changes the fingerprint, so its stale
// decision drops and the suggestion reads as undecided again; orphan decisions
// (the suggestion is gone) are dropped too. Zero-arg so the framework pre-computes
// it; the suggestionCard looks up its own ID via {{index $.DecisionsBySuggestion .ID}}.
func (s PrereviewState) DecisionsBySuggestion() map[string]SuggestionDecision {
	if len(s.Decisions) == 0 || len(s.Suggestions) == 0 {
		return nil
	}
	byID := make(map[string]SuggestionDecision, len(s.Decisions))
	for _, d := range s.Decisions {
		byID[d.SuggestionID] = d
	}
	out := make(map[string]SuggestionDecision)
	for _, sg := range s.Suggestions {
		if d, ok := byID[sg.ID]; ok && d.Fingerprint == suggestionFingerprint(sg) {
			out[sg.ID] = d
		}
	}
	return out
}

// DecisionCount is the number of current suggestions carrying a live (fingerprint-
// matching) decision — drives the "N decisions recorded" status. Zero-arg.
func (s PrereviewState) DecisionCount() int {
	return len(s.DecisionsBySuggestion())
}

// SuggestionGroupInfo positions a suggestion within its group of same-area
// alternatives (#117).
type SuggestionGroupInfo struct {
	Index int // 1-based position within the group, in submission order
	Total int // group size; only groups of >1 members are surfaced
}

// SuggestionGroups maps each suggestion ID that belongs to a multi-member
// alternatives group (same File/Side/range/OriginalText) to its position + group
// size, so the card can show "N of M" + a shared visual — a clear signal that the
// suggestions are alternatives, not independent edits. Zero-arg so the framework
// pre-computes it; the card looks up {{index $.SuggestionGroups .ID}}. Computed
// over the SAME visible set the views render (hidden suggestions don't count), so
// the "N of M" never over- or under-states what the reviewer can see.
func (s PrereviewState) SuggestionGroups() map[string]SuggestionGroupInfo {
	vis := s.visibleSuggestions()
	if len(vis) < 2 {
		return nil
	}
	byKey := make(map[string][]string)
	for _, sg := range vis {
		k := sg.groupKey()
		byKey[k] = append(byKey[k], sg.ID)
	}
	var out map[string]SuggestionGroupInfo
	for _, ids := range byKey {
		if len(ids) < 2 {
			continue // not a group
		}
		if out == nil {
			out = make(map[string]SuggestionGroupInfo, len(ids))
		}
		for i, id := range ids {
			out[id] = SuggestionGroupInfo{Index: i + 1, Total: len(ids)}
		}
	}
	return out
}

// FileSuggestions returns the selected file's suggestions as a flat slice (nil
// when toggled off), so the rendered-Markdown view can, per block, show the
// suggestions whose ToLine falls in that block's source range — the parallel of
// FileComments for the block view. The line/code view uses SuggestionsByEndLine.
func (s PrereviewState) FileSuggestions() []Suggestion {
	if s.SelectedFile == "" || s.HideSuggestions {
		return nil
	}
	var out []Suggestion
	for _, sg := range s.visibleSuggestions() {
		if sg.File == s.SelectedFile {
			out = append(out, sg)
		}
	}
	return out
}

// FileComments returns the selected file's visible LINE-anchored
// comments (honoring ShowResolved) as a flat slice. Zero-arg so the
// rendered-Markdown view can, per block, show the comments whose
// ToLine falls in that block's source range — the line-view path
// uses CommentsByEndLine (exact-line map) instead.
//
// File-level (Kind=="file") and area-level (Kind=="area") comments
// are excluded here so they don't accidentally try to anchor at line
// 0 inside a block range. They're rendered in their own sections via
// FileLevelComments() and AreaComments().
func (s PrereviewState) FileComments() []Comment {
	if s.SelectedFile == "" {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if c.File != s.SelectedFile {
			continue
		}
		if c.IsFileLevel() || c.IsAreaLevel() {
			continue
		}
		if s.commentHiddenFromView(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// LineDisplay maps each visible diff line's key ("L<old>-<new>", matching the
// template's $lkey) to its rendered content with every character-range
// (kind=text) comment wrapped in a <mark class="comment-span">. Zero-arg so
// livetemplate pre-computes it into the data map (it only pre-computes zero-arg
// methods — a method taking the line as an argument does NOT render); the
// template indexes it with {{index $.LineDisplay $lkey}}, mirroring how
// CommentsByEndLine / SelectedLines are consumed. (OldNum,NewNum) is unique per
// line — adds are (0,n), dels (m,0), ctx (m,n) — so the key never collides.
func (s PrereviewState) LineDisplay() map[string]template.HTML {
	lines := s.VisibleLines()
	out := make(map[string]template.HTML, len(lines))
	for _, line := range lines {
		if line.Kind == "fold" {
			continue // fold rows render their own label, no code content
		}
		out[fmt.Sprintf("L%d-%d", line.OldNum, line.NewNum)] = s.markedLineContent(line)
	}
	return out
}

// markedLineContent returns a diff line's rendered content with every
// character-range (kind=text) comment on that line wrapped in a
// <mark class="comment-span">. Falls back to the escaped raw content when the
// line carries no chroma highlight, and returns the fragment untouched when no
// text comment covers the line — so an ordinary line pays only a slice scan.
func (s PrereviewState) markedLineContent(line gitdiff.DiffLine) template.HTML {
	frag := line.HighlightedContent
	if frag == "" {
		frag = template.HTML(template.HTMLEscapeString(line.Content))
	}
	ranges := s.textMarksForLine(line)
	if len(ranges) == 0 {
		return frag
	}
	return gitdiff.MarkRanges(frag, ranges)
}

// textMarksForLine collects the rune ranges to highlight on one diff line from
// the selected file's visible kind=text comments. A comment spanning several
// lines contributes [FromCol, end) on its first line, the whole line on
// interiors, and [0, ToCol) on its last; a single-line comment contributes
// [FromCol, ToCol). Outdated comments are skipped — their stored columns no
// longer point at the intended content, so marking them would highlight the
// wrong text (the card still shows, flagged, via CommentsByEndLine).
func (s PrereviewState) textMarksForLine(line gitdiff.DiffLine) []gitdiff.ColRange {
	if s.SelectedFile == "" {
		return nil
	}
	side, ln := "new", line.NewNum
	if line.Kind == "del" {
		side, ln = "old", line.OldNum
	}
	if ln == 0 {
		return nil
	}
	var out []gitdiff.ColRange
	for _, c := range s.Comments {
		if !c.IsTextLevel() || c.File != s.SelectedFile || c.Side != side {
			continue
		}
		if c.AnchorOutdated() || s.commentHiddenFromView(c) {
			continue
		}
		if ln < c.FromLine || ln > c.ToLine {
			continue
		}
		from, to := 0, lineEndSentinel
		if ln == c.FromLine {
			from = c.FromCol
		}
		if ln == c.ToLine {
			to = c.ToCol
		}
		out = append(out, gitdiff.ColRange{From: from, To: to})
	}
	return out
}

// FileLevelComments returns the selected file's visible whole-file
// comments (Kind == "file") in creation order. Rendered in a dedicated
// section above the per-line body so reviewers see "comments on the
// file itself" before any line-anchored feedback.
func (s PrereviewState) FileLevelComments() []Comment {
	if s.SelectedFile == "" {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if c.File != s.SelectedFile {
			continue
		}
		if !c.IsFileLevel() {
			continue
		}
		if s.commentHiddenFromView(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// AreaComments returns the selected file's visible image-area comments
// (Kind == "area") in creation order. Rendered as semi-transparent
// rectangle overlays inside the image wrapper, with paired list
// entries for body + Resolve/Edit/Delete actions in the file-comments
// section. Same shape as FileLevelComments — parallel iteration.
func (s PrereviewState) AreaComments() []Comment {
	if s.SelectedFile == "" {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if c.File != s.SelectedFile {
			continue
		}
		if !c.IsAreaLevel() {
			continue
		}
		if s.commentHiddenFromView(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// RegionComments returns the visible region annotations (kind="region")
// for the CURRENT proxied page (--external mode), in creation order.
// Rendered as the re-pin overlay markers + paired list entries. Scoped by
// CurrentURL (the page the iframe is on) rather than SelectedFile, since
// external mode has no file. Zero-arg so the template can range over it.
func (s PrereviewState) RegionComments() []Comment {
	if !s.ExternalMode || s.CurrentURL == "" {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if !c.IsRegionLevel() || c.URL != s.CurrentURL {
			continue
		}
		if s.commentHiddenFromView(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// FocusedComment returns the region annotation the user tapped to locate
// (FocusedCommentID), or nil. The template reads its URL + Area to tell the
// client which page to show and where to scroll the iframe.
func (s PrereviewState) FocusedComment() *Comment {
	if s.FocusedCommentID == "" {
		return nil
	}
	for i := range s.Comments {
		if s.Comments[i].ID == s.FocusedCommentID {
			return &s.Comments[i]
		}
	}
	return nil
}

// AllRegionComments returns every visible region annotation for the
// --external sidebar, in creation order. Unlike RegionComments it is NOT
// scoped to the current page, so the sidebar can show annotations across
// every page the user has visited.
func (s PrereviewState) AllRegionComments() []Comment {
	if !s.ExternalMode {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if !c.IsRegionLevel() {
			continue
		}
		if s.commentHiddenFromView(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// SelectionEndMax returns max(SelectionAnchor, SelectionEnd) — the line
// after which the inline composer should render. Zero means "no selection,
// don't render the composer". Order-independent so the user can pick
// anchor=10 → end=5 and the composer still lands after line 10.
func (s PrereviewState) SelectionEndMax() int {
	if s.SelectionAnchor == 0 {
		return 0
	}
	if s.SelectionEnd > s.SelectionAnchor {
		return s.SelectionEnd
	}
	return s.SelectionAnchor
}

// SelectedLines returns a set of line numbers currently selected. Zero-arg
// so the livetemplate framework eagerly pre-computes it once per render
// (the framework only pre-computes zero-arg methods, so a SelectionContains(n)
// helper would not be callable from the template). The template membership-tests
// with `{{index $.SelectedLines $n}}` — index returns the zero value (false)
// for missing keys, which is exactly what we want for unselected lines.
func (s PrereviewState) SelectedLines() map[int]bool {
	if s.SelectionAnchor == 0 {
		return nil
	}
	lo, hi := s.SelectionAnchor, s.SelectionEnd
	if lo > hi {
		lo, hi = hi, lo
	}
	out := make(map[int]bool, hi-lo+1)
	for n := lo; n <= hi; n++ {
		out[n] = true
	}
	return out
}

// SelectionLabel returns the human-readable form of the current selection
// (e.g. "L42" or "L42-L48"; for a text selection "L42:6-12" or "L42:6-L48:3").
// Empty string when nothing is selected. Kept consistent with
// Comment.LineSpan so the composer heading matches the saved card's badge.
func (s PrereviewState) SelectionLabel() string {
	if s.SelectionAnchor == 0 {
		return ""
	}
	// Text mode carries character offsets; render them so a word/phrase
	// selection reads precisely (the columns pair with anchor/end, which
	// SelectText stores doc-ordered — do not swap lines without the cols).
	if s.CommentMode == commentKindText {
		// Rendered-origin (Preview) selections carry no columns — show a plain
		// line span, matching Comment.LineSpan.
		if s.SelectionFromCol == 0 && s.SelectionToCol == 0 {
			if s.SelectionAnchor == s.SelectionEnd {
				return fmt.Sprintf("L%d", s.SelectionAnchor)
			}
			return fmt.Sprintf("L%d-L%d", s.SelectionAnchor, s.SelectionEnd)
		}
		if s.SelectionAnchor == s.SelectionEnd {
			return fmt.Sprintf("L%d:%d-%d", s.SelectionAnchor, s.SelectionFromCol, s.SelectionToCol)
		}
		return fmt.Sprintf("L%d:%d-L%d:%d", s.SelectionAnchor, s.SelectionFromCol, s.SelectionEnd, s.SelectionToCol)
	}
	lo, hi := s.SelectionAnchor, s.SelectionEnd
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo == hi {
		return fmt.Sprintf("L%d", lo)
	}
	return fmt.Sprintf("L%d-L%d", lo, hi)
}
