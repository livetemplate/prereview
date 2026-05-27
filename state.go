package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/livetemplate/prereview/gitdiff"
)

// PrereviewState is the per-session state cloned by livetemplate. Fields
// tagged `lvt:"persist"` survive WebSocket reconnects (browser refresh, etc.)
// so the user doesn't lose their selected file or comment draft if the page
// reloads.
type PrereviewState struct {
	// Session identity — set once by main.go, never mutated.
	RepoPath  string `json:"repo_path"  lvt:"persist"`
	Base      string `json:"base"       lvt:"persist"`
	StartedAt string `json:"started_at" lvt:"persist"`
	CSVPath   string `json:"csv_path"   lvt:"persist"`
	Version   string `json:"version"    lvt:"persist"`

	// File navigation.
	Files        []gitdiff.FileEntry `json:"files"`
	SelectedFile string              `json:"selected_file" lvt:"persist"`
	CurrentDiff  *gitdiff.FileDiff   `json:"current_diff"`

	// Two-click selection (anchor → end). 0 = nothing selected; first
	// click sets both to the same line; second click moves end; third
	// click reseats anchor.
	SelectionAnchor int    `json:"selection_anchor" lvt:"persist"`
	SelectionEnd    int    `json:"selection_end"    lvt:"persist"`
	SelectionSide   string `json:"selection_side"   lvt:"persist"` // "new"|"old"|"both"

	// Comment composer.
	DraftBody string `json:"draft_body" lvt:"persist"`

	// Comments accumulated during this session.
	Comments []Comment `json:"comments"`

	// UI status.
	LastSaved   string `json:"last_saved"`
	DoneWritten bool   `json:"done_written" lvt:"persist"`

	// Mobile drawer visibility. Persisted so a reconnect mid-drawer doesn't
	// surprise the user with a closed drawer. The desktop CSS ignores this
	// field (sidebar is always visible above 900px).
	FileDrawerOpen bool `json:"file_drawer_open" lvt:"persist"`

	// SkillMode is mirrored from the controller (set by --skill flag) into
	// state in Mount so the template can branch the top-bar button between
	// "Hand off → Claude" (skill) and "Quit" (standalone). Not persisted —
	// the controller is the source of truth; Mount refreshes it every connect.
	SkillMode bool `json:"skill_mode"`

	// NoGit is mirrored from the controller (set when --repo is a single
	// file or a non-git directory) into state in Mount so the template
	// can hide the base/branch picker — there are no refs to compare
	// against. Not persisted; the controller is the source of truth.
	NoGit bool `json:"no_git"`

	// Quitting flips true when the user clicks Quit. The template renders
	// a "Server stopping…" banner; ~250ms later the HTTP server actually
	// shuts down (giving the framework time to flush the render).
	Quitting bool `json:"quitting"`

	// EditingCommentID is set when the user has tapped Edit on an existing
	// comment. The composer label changes to "Editing comment on L28"
	// instead of "Comment on L28" so it's clear the next save replaces
	// rather than appends. Cleared by AddComment, ClearSelection.
	// Persisted so the edit mode survives a WebSocket reconnect — iPhone
	// Safari drops the WS aggressively on tab/app switch. Without persist,
	// AddComment would see EditingCommentID="" after the reconnect and
	// append a new comment instead of updating in place.
	EditingCommentID string `json:"editing_comment_id" lvt:"persist"`

	// ReanchorCommentID is set when the user taps "Re-anchor here" on an
	// outdated comment. The next line/range they select + Save re-points
	// that comment (and re-captures its content anchor) instead of
	// appending. Mutually exclusive with EditingCommentID. Persisted for
	// the same reconnect-resilience reason. Cleared by AddComment /
	// ClearSelection.
	ReanchorCommentID string `json:"reanchor_comment_id" lvt:"persist"`

	// LastDeletedComment holds the most recently deleted comment so the
	// user can undo. Cleared by ANY other mutation (add, edit, another
	// delete, hand off, quit) so the undo affordance can't surprise the
	// user later with state from minutes ago.
	LastDeletedComment *Comment `json:"last_deleted_comment"`

	// ViewedFiles is a per-session set of files the user has marked as
	// "reviewed" (GitHub PR convention). Persisted so the state survives
	// browser refresh; not written to CSV (this is UX state, not a comment).
	ViewedFiles map[string]bool `json:"viewed_files" lvt:"persist"`

	// FileFilter is the case-insensitive substring filter for the file
	// drawer. Persisted so a refresh doesn't drop the filter.
	FileFilter string `json:"file_filter" lvt:"persist"`

	// ShowAllFiles controls the drawer scope. Default (false) lists
	// only files that differ from the base — the common review case,
	// and the only sane default on a large repo. When there are zero
	// changed files (clean tree) the scope falls back to all so the
	// list isn't empty. true forces the full tracked-file list so the
	// user can comment on unchanged files too.
	ShowAllFiles bool `json:"show_all_files" lvt:"persist"`

	// ShowAllComments toggles the all-comments overview pane (replaces the
	// diff viewer). Not persisted — closing/reopening the browser starts
	// back in the diff view.
	ShowAllComments bool `json:"show_all_comments"`

	// ScrollToCommentID, when non-empty for one render, drives the
	// `lvt-fx:scroll="into-view"` directive on the matching inline-comment.
	// Set by JumpToComment; the framework's one-shot guard
	// (data-lvt-iv-done) prevents repeated scrolls on subsequent renders.
	ScrollToCommentID string `json:"scroll_to_comment_id"`

	// ScrollToHeadingID, when non-empty, drives the
	// `lvt-fx:scroll="into-view"` directive on the MarkdownBlock that
	// contains the heading with this ID. Set by NavigateToHeading (TOC
	// click) so a heading clicked from inside the all-comments overview
	// actually lands the user on that section once ShowAllComments
	// flips back off — the all-comments view replaces the md-view in the
	// DOM, so the rendered markdown is fresh on return and the framework's
	// data-lvt-iv-done guard hasn't been set yet (issue #12).
	ScrollToHeadingID string `json:"scroll_to_heading_id"`

	// ShowResolved, when true, includes resolved comments in the inline
	// comment stream + all-comments view. Default false so the viewer
	// focuses on what's still actionable. Persisted across reconnects.
	ShowResolved bool `json:"show_resolved" lvt:"persist"`

	// MoreMenuOpen drives the 3-dots overflow menu in the top bar where
	// secondary controls (All comments, Show resolved) live on narrow
	// viewports. Not persisted — closing across a reconnect is the right
	// default; nobody expects an overflow menu to survive a refresh.
	MoreMenuOpen bool `json:"more_menu_open"`

	// TOCOpen drives the mobile Table-of-Contents overlay. On desktop
	// the TOC is always a right sidebar (no state needed); on narrow
	// viewports it lives behind a 3-dots menu item that flips this flag,
	// rendering the heading list as a full-screen overlay. Not persisted
	// for the same reason as MoreMenuOpen.
	TOCOpen bool `json:"toc_open"`

	// FileView, when true, turns off the diff overlay: deleted lines are
	// hidden, +/- gutter markers disappear, and add/del row coloring is
	// dropped. The user sees the file as it currently exists in the
	// working tree. Equivalent to GitHub's "View file" toggle. Persisted
	// as a per-user preference. Defaults false (diff is the primary
	// reviewing mode).
	FileView bool `json:"file_view" lvt:"persist"`

	// RawMarkdown shows a .md/.markdown file as the raw line view
	// instead of the rendered default. Persisted per-user. Defaults
	// false: Markdown renders by default; the user toggles to raw to
	// see the source lines. Non-Markdown files ignore this.
	RawMarkdown bool `json:"raw_markdown" lvt:"persist"`

	// BaseChoices populates the base-picker dropdown. Computed in
	// Mount: ["HEAD", "HEAD~1", "HEAD~5", <local branches…>] plus the
	// current state.Base if it isn't already in the list (so custom
	// refs typed via the freeform fallback still appear as the
	// selected option). Not persisted — recomputed each Mount so newly
	// created branches show up without a process restart.
	BaseChoices []string `json:"base_choices"`
}

// VisibleComments returns Comments filtered by ShowResolved. Zero-arg so
// the framework eagerly evaluates and the template iterates the filtered
// list directly.
func (s PrereviewState) VisibleComments() []Comment {
	if s.ShowResolved {
		return s.Comments
	}
	out := make([]Comment, 0, len(s.Comments))
	for _, c := range s.Comments {
		if !c.Resolved {
			out = append(out, c)
		}
	}
	return out
}

// ResolvedCount returns how many of the current comments are resolved —
// useful for "(N resolved hidden)" status copy.
func (s PrereviewState) ResolvedCount() int {
	n := 0
	for _, c := range s.Comments {
		if c.Resolved {
			n++
		}
	}
	return n
}

// OutdatedCount returns how many non-resolved comments could not be
// confidently re-anchored (their line numbers no longer point at the
// intended content) — drives the header "N need re-anchoring" hint so
// drift is discoverable without opening every file.
func (s PrereviewState) OutdatedCount() int {
	n := 0
	for _, c := range s.Comments {
		if !c.Resolved && c.AnchorOutdated() {
			n++
		}
	}
	return n
}

// scopedFiles applies the changed-only / all-files scope, independent
// of the search filter. Default is changed-only; if no file differs
// from the base (clean tree) it falls back to all so the list is never
// pointlessly empty. ShowAllFiles forces the full list. Used by the
// drawer (via FilteredFiles), Mount auto-select, and Next/PrevFile so
// navigation stays consistent with what the drawer shows.
func (s PrereviewState) scopedFiles() []gitdiff.FileEntry {
	if s.ShowAllFiles || gitdiff.ChangedCount(s.Files) == 0 {
		return s.Files
	}
	out := make([]gitdiff.FileEntry, 0, len(s.Files))
	for _, f := range s.Files {
		if f.Status != "" {
			out = append(out, f)
		}
	}
	return out
}

// ChangedFilesCount is how many files differ from the base. Zero-arg
// so the template can label the scope toggle without computing.
func (s PrereviewState) ChangedFilesCount() int {
	return gitdiff.ChangedCount(s.Files)
}

// VisibleLines is the line set the viewer renders for the selected
// file, per the current mode:
//
//   - File view  (FileView == true): the entire current working-tree
//     file — every line that exists on the new side (add + ctx),
//     deletions excluded since they aren't in the file anymore. No
//     diff, no folds.
//   - Diff view  (FileView == false): a real diff — only changed
//     lines plus 3 lines of context, long unchanged gaps replaced by
//     fold markers (see gitdiff.CollapseToHunks). An unchanged file
//     has no diff, so CollapseToHunks returns it whole.
//
// Zero-arg so the framework pre-computes it once per render and the
// template ranges `$.VisibleLines`. Line numbers are identical across
// modes, so comments anchored in one mode resolve in the other.
func (s PrereviewState) VisibleLines() []gitdiff.DiffLine {
	if s.CurrentDiff == nil {
		return nil
	}
	if s.FileView {
		out := make([]gitdiff.DiffLine, 0, len(s.CurrentDiff.Lines))
		for _, l := range s.CurrentDiff.Lines {
			if l.NewNum > 0 { // exists in the working-tree file
				out = append(out, l)
			}
		}
		return out
	}
	return gitdiff.CollapseToHunks(s.CurrentDiff.Lines, diffContextLines)
}

// diffContextLines is how many unchanged lines flank each change in
// Diff view (git's default).
const diffContextLines = 3

// IsMarkdown reports whether the selected file is Markdown.
func (s PrereviewState) IsMarkdown() bool {
	return s.CurrentDiff != nil && gitdiff.IsMarkdownPath(s.CurrentDiff.Path)
}

// BinaryKind classifies an IsBinary FileDiff by extension so the viewer
// can render a real preview (<img>, <iframe>, <video>, <audio>) instead
// of the bare "Binary file — cannot display" message. Returns "" for
// formats with no in-browser viewer (e.g. .zip, .so, unknown ext) so
// the template falls back to the cannot-display copy. Extensions must
// be a subset of staticAllowedExt in main.go — the static fallback
// only serves what's on that allowlist, and an <img>/<iframe> pointing
// at a 404 would just show a broken icon.
func (s PrereviewState) BinaryKind() string {
	if s.CurrentDiff == nil || !s.CurrentDiff.IsBinary {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(s.CurrentDiff.Path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".ico":
		return "image"
	case ".pdf":
		return "pdf"
	case ".mp4", ".webm":
		return "video"
	case ".mp3", ".wav":
		return "audio"
	}
	return ""
}

// ShowRenderedMarkdown is true when the viewer should show the
// rendered-Markdown blocks instead of the line view: a Markdown file,
// not toggled to raw, with at least one rendered block. Zero-arg so
// the template can branch on `$.ShowRenderedMarkdown`.
func (s PrereviewState) ShowRenderedMarkdown() bool {
	return s.IsMarkdown() && !s.RawMarkdown && len(s.CurrentDiff.MarkdownBlocks) > 0
}

// RenderedMarkdown is the block list for the rendered view (nil unless
// ShowRenderedMarkdown). Each block carries its real source line range
// so comments stay line-accurate across rendered and raw views.
func (s PrereviewState) RenderedMarkdown() []gitdiff.MarkdownBlock {
	if !s.ShowRenderedMarkdown() {
		return nil
	}
	return s.CurrentDiff.MarkdownBlocks
}

// ScrollHeadingBlockKey returns the `data-key` (e.g. "MB-5-10") of the
// MarkdownBlock containing the heading currently targeted by
// ScrollToHeadingID — or "" when no scroll is requested or the heading
// is not found. The template compares this against each block's own
// data-key to gate a `lvt-fx:scroll="into-view"` directive; see
// NavigateToHeading and issue #12.
//
// Zero-arg by design: livetemplate's template evaluator
// (livetemplate/internal/parse/eval.go callMethod) only auto-invokes
// methods with NumIn() == 0, so a "does this block match" predicate
// taking the block's range as arguments isn't reachable from the
// template. Precomputing the matching key here and comparing with the
// builtin `eq`/`printf` keeps the hot path in Go.
func (s PrereviewState) ScrollHeadingBlockKey() string {
	if s.ScrollToHeadingID == "" {
		return ""
	}
	var line int
	for _, h := range s.CurrentDiff.Headings {
		if h.ID == s.ScrollToHeadingID {
			line = h.Line
			break
		}
	}
	if line == 0 {
		return ""
	}
	for _, b := range s.CurrentDiff.MarkdownBlocks {
		if line >= b.StartLine && line <= b.EndLine {
			return fmt.Sprintf("MB-%d-%d", b.StartLine, b.EndLine)
		}
	}
	return ""
}

// RenderedHeadings returns the TOC entries for the current Markdown
// file, filtered to h1–h3 (deeper levels create visual noise without
// adding navigational value for typical docs). Returns nil when the
// rendered view isn't showing OR when there are fewer than two
// headings — a TOC with one entry is pointless and just steals
// horizontal real estate from the prose.
func (s PrereviewState) RenderedHeadings() []gitdiff.Heading {
	if !s.ShowRenderedMarkdown() {
		return nil
	}
	all := s.CurrentDiff.Headings
	if len(all) == 0 {
		return nil
	}
	out := make([]gitdiff.Heading, 0, len(all))
	for _, h := range all {
		if h.Level >= 1 && h.Level <= 3 {
			out = append(out, h)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// FilteredFiles returns the scoped files (see scopedFiles) further
// narrowed by FileFilter (case-insensitive substring match against
// path). Zero-arg so the framework pre-computes it once per render and
// the template can iterate `$.FilteredFiles`.
func (s PrereviewState) FilteredFiles() []gitdiff.FileEntry {
	files := s.scopedFiles()
	if strings.TrimSpace(s.FileFilter) == "" {
		return files
	}
	q := strings.ToLower(strings.TrimSpace(s.FileFilter))
	out := make([]gitdiff.FileEntry, 0, len(files))
	for _, f := range files {
		if strings.Contains(strings.ToLower(f.Path), q) {
			out = append(out, f)
		}
	}
	return out
}

// ViewedCount returns the number of files in state.Files marked as viewed.
// Zero-arg so the template can show "N of M reviewed" without computing.
func (s PrereviewState) ViewedCount() int {
	if len(s.ViewedFiles) == 0 {
		return 0
	}
	n := 0
	for _, f := range s.Files {
		if s.ViewedFiles[f.Path] {
			n++
		}
	}
	return n
}

// Comment is one row in the CSV output (and one entry in state).
type Comment struct {
	ID       string    `json:"id"`
	File     string    `json:"file"`
	FromLine int       `json:"from_line"`
	ToLine   int       `json:"to_line"`
	Side     string    `json:"side"`
	Body     string    `json:"body"`
	Created  time.Time `json:"created"`
	// Resolved marks the comment as "addressed; keep as history". The skill
	// should act only on unresolved comments. Toggled via ResolveComment.
	Resolved bool `json:"resolved"`
	// Anchor is the content fingerprint captured at create/edit time so
	// the comment can be re-located when the file changes (see anchor.go).
	// AnchorStatus is "ok" | "moved" | "outdated" (empty == ok for
	// legacy pre-migration comments).
	Anchor       CommentAnchor `json:"anchor"`
	AnchorStatus string        `json:"anchor_status"`
}

// AnchorOutdated reports that re-location could not confidently place
// the comment — its line numbers no longer point at the intended
// content and a human (or the skill) must re-anchor or resolve it.
func (c Comment) AnchorOutdated() bool { return c.AnchorStatus == anchorOutdated }

// AnchorMoved reports that the comment was auto-shifted to follow its
// content after the file changed (purely informational in the UI).
func (c Comment) AnchorMoved() bool { return c.AnchorStatus == anchorMoved }

// LineSpan returns "L42" for single-line and "L42-L48" for ranges.
// Method on Comment so the template can call {{.LineSpan}} on each entry.
func (c Comment) LineSpan() string {
	if c.FromLine == c.ToLine {
		return fmt.Sprintf("L%d", c.FromLine)
	}
	return fmt.Sprintf("L%d-L%d", c.FromLine, c.ToLine)
}

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
		if c.Resolved && !s.ShowResolved {
			continue
		}
		out[c.ToLine] = append(out[c.ToLine], c)
	}
	return out
}

// FileComments returns the selected file's visible comments (honoring
// ShowResolved) as a flat slice. Zero-arg so the rendered-Markdown
// view can, per block, show the comments whose ToLine falls in that
// block's source range — the line-view path uses CommentsByEndLine
// (exact-line map) instead.
func (s PrereviewState) FileComments() []Comment {
	if s.SelectedFile == "" {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if c.File != s.SelectedFile {
			continue
		}
		if c.Resolved && !s.ShowResolved {
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
// (e.g. "L42" or "L42-L48"). Empty string when nothing is selected.
func (s PrereviewState) SelectionLabel() string {
	if s.SelectionAnchor == 0 {
		return ""
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
