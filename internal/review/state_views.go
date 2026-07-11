package review

import (
	"fmt"
	"html/template"
	"path/filepath"
	"strings"

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

// SelectedFileBase / SelectedFileDir split the selected file's path for the
// file-head: the basename is shown bold and always in full (it identifies the
// file), the directory prefix is muted and truncates when the header is tight, so
// a long/deep path never wraps into a multi-line block that shoves the action
// chips off-screen. Dir includes a trailing "/"; empty for a top-level file.
// Zero-arg state methods (safe — a method on the nested *FileDiff wouldn't render).
func (s PrereviewState) SelectedFileBase() string {
	return filepath.Base(s.SelectedFile)
}

func (s PrereviewState) SelectedFileDir() string {
	d := filepath.Dir(s.SelectedFile)
	if d == "." || d == "" || d == "/" {
		return ""
	}
	return d + "/"
}

// ReadPercent is how far through the selected file the reviewer has read (0..100):
// the read high-water mark (ReadThrough — the furthest new-side line scrolled past)
// as a fraction of the file's line count. Drives the top reading-progress bar
// (#128). Zero-arg; 0 when nothing's been read or the file length is unknown.
// Because it's the high-water mark, it only grows — scrolling back up doesn't
// shrink it (you've still reviewed that far).
func (s PrereviewState) ReadPercent() int {
	if len(s.ReadThrough) == 0 || s.SelectedFile == "" || s.CurrentDiff == nil {
		return 0
	}
	mark := s.ReadThrough[s.SelectedFile]
	if mark == 0 {
		return 0
	}
	total := 0
	for _, ln := range s.CurrentDiff.Lines {
		if ln.NewNum > total {
			total = ln.NewNum
		}
	}
	if total == 0 {
		return 0
	}
	if pct := mark * 100 / total; pct < 100 {
		return pct
	}
	return 100
}

// visibleSuggestions is the SINGLE source every render surface iterates —
// SuggestionsByEndLine (line view), FileSuggestions (block view), and
// SuggestionGroups (the "N of M" count) all go through it, so a suggestion
// excluded here can never leak into one view while showing in another. It drops
// suggestions the reviewer has individually hidden (fingerprint-gated: a revised
// suggestion's content no longer matches the stored hide, so it reappears).
func (s PrereviewState) visibleSuggestions() []Suggestion {
	if len(s.Hidden) == 0 {
		return s.Suggestions
	}
	hidden := s.hiddenFingerprints()
	out := make([]Suggestion, 0, len(s.Suggestions))
	for _, sg := range s.Suggestions {
		if isHidden(hidden, sg) {
			continue // hidden against this exact content
		}
		out = append(out, sg)
	}
	return out
}

// hiddenFingerprints maps each hidden suggestion's ID to the content fingerprint
// it was hidden against, so visibleSuggestions can drop only suggestions whose
// content still matches (a revision un-hides). Small (hidden set is tiny).
func (s PrereviewState) hiddenFingerprints() map[string]string {
	m := make(map[string]string, len(s.Hidden))
	for _, h := range s.Hidden {
		m[h.SuggestionID] = h.Fingerprint
	}
	return m
}

// isHidden reports whether sg is hidden against its CURRENT content: its ID is in
// the hidden set AND the pinned fingerprint still matches (a revision drops the
// match, so the suggestion reappears). The membership gate means only the tiny
// hidden set is ever hashed — never every suggestion.
func isHidden(hidden map[string]string, sg Suggestion) bool {
	fp, ok := hidden[sg.ID]
	return ok && fp == suggestionFingerprint(sg)
}

// HiddenSuggestionCount is how many of the SELECTED file's suggestions are
// currently hidden (fingerprint-matching), driving the "N hidden · show"
// affordance. Scoped to the selected file so the count matches what a reviewer
// could reveal on the page they're looking at. Zero-arg.
func (s PrereviewState) HiddenSuggestionCount() int {
	if len(s.Hidden) == 0 || s.SelectedFile == "" {
		return 0
	}
	hidden := s.hiddenFingerprints()
	n := 0
	for _, sg := range s.Suggestions {
		if sg.File == s.SelectedFile && isHidden(hidden, sg) {
			n++
		}
	}
	return n
}

// suggestionCollapsed reports that an APPLIED suggestion is collapsed to its
// right-margin ✦ badge (#159 M4.3b) — the default for an applied edit, unless the
// reviewer expanded it to peek (ExpandedSuggestions). Only applied ids ever
// collapse; an accepted-pending or undecided suggestion always renders inline.
func (s PrereviewState) suggestionCollapsed(id string) bool {
	return s.Applied[id] && !s.ExpandedSuggestions[id]
}

// suggestionsGroupedBy groups the selected file's visible suggestions by ToLine,
// keeping only those the predicate accepts. Nil when suggestions are toggled off
// (HideSuggestions) or none match. Shared by the zero-arg public views below (the
// framework only pre-computes zero-arg methods, so this stays private). The three
// views over it — inline boxes, ✦ applied badges, green count — use three different
// predicates rather than one complement, because an applied suggestion that's
// expanded shows in BOTH the inline set (the peek) and the ✦ set (the toggle).
func (s PrereviewState) suggestionsGroupedBy(keep func(Suggestion) bool) map[int][]Suggestion {
	if s.SelectedFile == "" || s.HideSuggestions {
		return nil
	}
	out := make(map[int][]Suggestion)
	for _, sg := range s.visibleSuggestions() {
		if sg.File != s.SelectedFile || !keep(sg) {
			continue
		}
		out[sg.ToLine] = append(out[sg.ToLine], sg)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// countByLine collapses a by-ToLine suggestion grouping into per-row counts keyed
// "<toLine>-<side>" (both sides for a whole-line suggestion), driving the right-
// margin badges. Nil when empty.
func countByLine(byLine map[int][]Suggestion) map[string]int {
	out := map[string]int{}
	for ln, sgs := range byLine {
		for _, sg := range sgs {
			countRowSides(out, ln, sg.Side)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SuggestionsByEndLine groups the selected file's INLINE suggestion boxes by ToLine,
// so the template renders each right after its trailing line — like CommentsByEndLine
// does for comments. A suggestion renders inline unless it's a collapsed applied one
// (Applied && !Expanded) — those show only as a right-margin ✦ badge (AppliedByLine).
func (s PrereviewState) SuggestionsByEndLine() map[int][]Suggestion {
	return s.suggestionsGroupedBy(func(sg Suggestion) bool { return !s.suggestionCollapsed(sg.ID) })
}

// AppliedByLine groups the selected file's APPLIED suggestions by ToLine — one ✦
// badge each in the right margin, ALWAYS present for an applied suggestion (whether
// collapsed or expanded), because the badge IS the expand/collapse toggle. The
// template marks the badge is-expanded when the box is currently peeked open.
func (s PrereviewState) AppliedByLine() map[int][]Suggestion {
	return s.suggestionsGroupedBy(func(sg Suggestion) bool { return s.Applied[sg.ID] })
}

// AppliedBadgeLines reports, per line-row, how many ✦ applied badges render there —
// the presence gate for the right-margin container. Zero-arg; nil when none.
func (s PrereviewState) AppliedBadgeLines() map[string]int {
	return countByLine(s.AppliedByLine())
}

// CommentCountLines reports, per line-row (keyed "<toLine>-<side>", e.g. "4-new"),
// HOW MANY comments render on that row — driving the #151 right-margin count badge
// (and, via count>0, the #136 presence marks). Derived from the SAME filtered map the
// cards render from (CommentsByEndLine), so the badge's number always equals the cards
// actually rendered — side, individually-hidden, resolved filter, and file scope are
// all inherited. Zero-arg so the framework pre-computes it (a method WITH an arg
// silently breaks rendering); the row looks itself up as
// {{index $.CommentCountLines (printf "%d-%s" $ln $lside)}}. Nil when none, so a
// missing key indexes to 0. (SuggestionCountLines is its counterpart below.)
func (s PrereviewState) CommentCountLines() map[string]int {
	out := map[string]int{}
	for ln, cs := range s.CommentsByEndLine() {
		for _, c := range cs {
			countRowSides(out, ln, c.Side)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SuggestionCountLines counts the NON-applied suggestions per row — the green
// "to review" badge. Applied suggestions are excluded (they show as the ✦ applied
// badge instead, via AppliedByLine), so the two right-margin suggestion badges never
// double-report the same suggestion, whether it's collapsed or expanded.
func (s PrereviewState) SuggestionCountLines() map[string]int {
	return countByLine(s.suggestionsGroupedBy(func(sg Suggestion) bool { return !s.Applied[sg.ID] }))
}

// HasMarks reports whether the selected file has any per-line comment/suggestion
// badges — i.e. whether the "Hide annotations" toggle (ToggleMarks) has anything to
// act on. Gates that menu entry. Reads the same memoized count maps the badges use.
func (s PrereviewState) HasMarks() bool {
	return len(s.CommentCountLines()) > 0 || len(s.SuggestionCountLines()) > 0
}

// Threads groups the loaded conversation entries (#149) by target ID (a comment or
// suggestion ID), so a card renders its own thread with {{index $.Threads .ID}}.
// Entries arrive already sorted (loadThreads), so each group is chronological.
// Zero-arg so the framework pre-computes it; nil when there are no threads.
func (s PrereviewState) Threads() map[string][]ThreadEntry {
	return groupThreads(s.ThreadEntries)
}

// AwaitingAgent maps each target ID whose thread ends with a REVIEWER entry — the
// reviewer replied and is waiting on the agent (#149) — so a card can badge itself
// "awaiting agent". Zero-arg for the framework; nil when none.
func (s PrereviewState) AwaitingAgent() map[string]bool {
	out := map[string]bool{}
	for id, th := range groupThreads(s.ThreadEntries) {
		if hasUnreadReviewerReply(th) {
			out[id] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CollapsedRows returns the collapsed diff rows (#112) for the SELECTED file, keyed
// by rowkey ("L<old>-<new>") — the file prefix that scopes CollapsedLines across
// files is stripped so the template can look a row up directly with
// {{index $.CollapsedRows $lkey}}. Zero-arg so the framework pre-computes it; nil
// when nothing is collapsed on this file (a missing key indexes to false).
func (s PrereviewState) CollapsedRows() map[string]bool {
	if len(s.CollapsedLines) == 0 || s.SelectedFile == "" {
		return nil
	}
	prefix := collapsedLineKey(s.SelectedFile, "") // "<file>\n" — same encoding as the keys
	out := map[string]bool{}
	for k := range s.CollapsedLines {
		if rowkey, ok := strings.CutPrefix(k, prefix); ok {
			out[rowkey] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// collapsedLineKey is the CollapsedLines map key for a row on the given file —
// file-scoped so equal line numbers on different files don't collide.
func collapsedLineKey(file, rowkey string) string {
	return file + "\n" + rowkey
}

// countRowSides increments the row key(s) an annotation on line ln / side occupies,
// matching the template's {{if or (eq .Side $lside) (eq .Side "")}} gate: a
// whole-line annotation (Side "") counts on BOTH the old and new rows; a one-sided
// one counts on just its side. This per-row identity (new = #new + #"", old = #old +
// #"") is what makes a badge's number equal the cards actually rendered on the row.
func countRowSides(out map[string]int, ln int, side string) {
	for _, k := range rowKeysFor(ln, side) {
		out[k]++
	}
}

// rowKeysFor returns the "<line>-<side>" key(s) an annotation on line ln / side
// occupies: a whole-line one (Side "") is on BOTH the old and new rows; a one-sided
// one is on just its side. Shared by the count badges and the awaiting-reply dots so
// they land on the same rows.
func rowKeysFor(ln int, side string) []string {
	if side == "" {
		return []string{fmt.Sprintf("%d-old", ln), fmt.Sprintf("%d-new", ln)}
	}
	return []string{fmt.Sprintf("%d-%s", ln, side)}
}

// AwaitingLines reports, per line-row ("<line>-<side>"), whether any comment or
// suggestion rendered there is awaiting the agent (the reviewer replied last, #149) —
// so the #151 count badge on that row can show an unread dot. Derived from the same
// filtered ByEndLine maps as the badges, gated by AwaitingAgent. Zero-arg; nil when
// nothing is awaiting.
func (s PrereviewState) AwaitingLines() map[string]bool {
	awaiting := s.AwaitingAgent()
	if len(awaiting) == 0 {
		return nil
	}
	out := map[string]bool{}
	mark := func(ln int, id, side string) {
		if awaiting[id] {
			for _, k := range rowKeysFor(ln, side) {
				out[k] = true
			}
		}
	}
	for ln, cs := range s.CommentsByEndLine() {
		for _, c := range cs {
			mark(ln, c.ID, c.Side)
		}
	}
	for ln, sgs := range s.SuggestionsByEndLine() {
		for _, sg := range sgs {
			mark(ln, sg.ID, sg.Side)
		}
	}
	if len(out) == 0 {
		return nil
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
			// #159 M4.2: a revert the agent already COMPLETED (Revert set, but the
			// suggestion is no longer applied — appliedCount<=revertedCount) drops back
			// to UNDECIDED. Revert-PENDING ones (still applied) stay, so the agent still
			// sees the task and the card shows "reverting". The stale Revert=true row in
			// decisions.jsonl self-cleans on the next commitDecisions rewrite. This is the
			// single chokepoint — the map feeds BOTH the template and the agent snapshot.
			if d.Revert && !s.Applied[sg.ID] {
				continue
			}
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
