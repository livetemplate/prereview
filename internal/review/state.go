package review

import (
	"fmt"
	"path/filepath"
	"strings"

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

	// External (proxy) mode — `prereview --external <url>`. When true the
	// session reviews a live local website framed through the reverse proxy
	// instead of files in a repo: there is no git diff and no file list, and
	// comments anchor to URL + region rather than file + line.
	// ProxyBaseURL is the same-host second-origin URL the UI iframes;
	// TargetURL is the upstream the proxy fronts (shown to the user);
	// CurrentURL is the proxied page currently in the iframe, reported by the
	// injected beacon and used to scope which annotations are shown/placed.
	ExternalMode bool   `json:"external_mode"  lvt:"persist"`
	ProxyBaseURL string `json:"proxy_base_url" lvt:"persist"`
	TargetURL    string `json:"target_url"     lvt:"persist"`
	CurrentURL   string `json:"current_url"    lvt:"persist"`

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

	// CommentMode flags whether the open composer is targeting a line
	// range ("" — the historical default, paired with SelectionAnchor),
	// the whole file ("file" — SelectionAnchor stays 0), or an image
	// region ("area" — paired with SelectionArea). Persisted so a
	// WebSocket reconnect mid-composer doesn't drop the user back to
	// the file head.
	CommentMode string `json:"comment_mode" lvt:"persist"`

	// SelectionArea holds the in-progress rectangle for a kind=area
	// comment: 0..1 fractions of the image's rendered size. Set by
	// SelectImageArea (dispatched from the client's lvt-fx:area-select
	// directive on pointerup) and cleared by AddComment / ClearSelection
	// / SelectFile. Persisted so the pending overlay re-appears after a
	// WebSocket reconnect.
	SelectionArea Area `json:"selection_area" lvt:"persist"`

	// RegionSelectArmed gates the "draw a box to comment" overlay over a
	// preview (image / rendered HTML / code). Off by default so a one-
	// finger gesture scrolls the page normally; the "Select region" toggle
	// flips it on, the overlay then captures the drag, and capturing a
	// region (SelectImageArea / SelectBlock) disarms it again so the
	// composer shows and scrolling returns. Persisted so a reconnect
	// mid-selection doesn't silently disarm the overlay.
	RegionSelectArmed bool `json:"region_select_armed" lvt:"persist"`

	// Comments accumulated during this session.
	Comments []Comment `json:"comments"`

	// UI status.
	LastSaved   string `json:"last_saved"`
	DoneWritten bool   `json:"done_written" lvt:"persist"`

	// Mobile drawer visibility. Persisted so a reconnect mid-drawer doesn't
	// surprise the user with a closed drawer. The desktop CSS ignores this
	// field (sidebar is always visible above 900px).
	FileDrawerOpen bool `json:"file_drawer_open" lvt:"persist"`

	// AnnoDrawerOpen toggles the --external annotations sidebar. Collapsed by
	// default (zero value) so the framed live site gets the full width —
	// especially on a phone; the header "Annotations (N)" button opens it.
	// Persisted so the choice survives a reconnect.
	AnnoDrawerOpen bool `json:"anno_drawer_open" lvt:"persist"`

	// FocusedCommentID is the region annotation the user tapped in the
	// sidebar; its on-page pin renders highlighted, and the client asks the
	// iframe (via the beacon) to navigate to its page + scroll it into view.
	// FocusSeq changes on every focus so re-tapping the same annotation
	// re-triggers the client even when the id is unchanged.
	FocusedCommentID string `json:"focused_comment_id" lvt:"persist"`
	FocusSeq         int    `json:"focus_seq"          lvt:"persist"`

	// SkillMode is mirrored from the controller (set by --skill flag) into
	// state in Mount so the template can branch the top-bar button between
	// "Hand off → Claude" (skill) and "Quit" (standalone). Not persisted —
	// the controller is the source of truth; Mount refreshes it every connect.
	SkillMode bool `json:"skill_mode"`

	// StreamMode is mirrored from the controller (set by --stream flag) into
	// state in Mount. It implies SkillMode (the "Hand off" button) and adds the
	// "End session" button: in stream mode each Hand off emits a JSON handoff
	// event and End session emits the terminating session_end event. Not
	// persisted; the controller is the source of truth.
	StreamMode bool `json:"stream_mode"`

	// NoGit is mirrored from the controller (set when the path is a single
	// file or a non-git directory) into state in Mount so the template
	// can hide the base/branch picker — there are no refs to compare
	// against. Not persisted; the controller is the source of truth.
	NoGit bool `json:"no_git"`

	// Quitting flips true when the user clicks Quit. The template renders
	// a "Server stopping…" banner; ~250ms later the HTTP server actually
	// shuts down (giving the framework time to flush the render).
	Quitting bool `json:"quitting"`

	// SessionEnded flips true when the user clicks "End session" in stream
	// mode. Like Quitting it precedes a delayed graceful shutdown, but the
	// banner wording differs ("session ended") and EndSession also emits the
	// terminating session_end stream event before shutting down.
	SessionEnded bool `json:"session_ended"`

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

	// URLHashScrollAnchor persists the `h-<anchor>` target from a
	// deep-link URL across renders so the URL bar keeps `:h-<anchor>`
	// even after the scroll completed (shareable link stays valid).
	// Used by `state.URLHash()`. For markdown, the scroll itself routes
	// through the ScrollToHeadingID + ScrollHeadingBlockKey machinery —
	// this field only feeds URL serialisation. (HTML preview renders in
	// one iframe, so it has no block-level scroll target.) Cleared by
	// ClearSelection, by clicking a line, and by SelectFile.
	URLHashScrollAnchor string `json:"url_hash_scroll_anchor" lvt:"persist"`

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

	// KeyHelpOpen drives the keyboard-shortcuts help overlay, toggled by
	// the "?" key or the toolbar help button (both dispatch
	// toggleKeyboardHelp). Esc closes it via ClearSelection. Not persisted —
	// a refresh shouldn't reopen a help panel.
	KeyHelpOpen bool `json:"key_help_open"`

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

	// RawHTML is the .html/.htm equivalent of RawMarkdown: when true the
	// viewer shows the syntax-highlighted source instead of the
	// sandboxed-iframe preview. Persisted per-user. Defaults false.
	// Independent of RawMarkdown so a user's preference for one format
	// doesn't drag the other along. Non-HTML files ignore this.
	RawHTML bool `json:"raw_html" lvt:"persist"`

	// FocusMode, when true, hides both desktop side columns (the file
	// drawer on the left and the TOC sidebar on the right) so the center
	// reading surface gets the full width — a distraction-free reading
	// view for long docs/diffs. Desktop-only in effect: the hiding CSS
	// lives behind the ≥900px media query; on mobile the columns are
	// already overlays/modals, so the flag is a harmless no-op there.
	// Persisted as a per-user reading preference, like FileView.
	FocusMode bool `json:"focus_mode" lvt:"persist"`

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

// IsHTML reports whether the selected file is an HTML file (.html/.htm).
// Drives the Preview/Raw toolbar toggle and the iframe branch in the
// viewer template, parallel to IsMarkdown.
func (s PrereviewState) IsHTML() bool {
	return s.CurrentDiff != nil && gitdiff.IsHTMLPath(s.CurrentDiff.Path)
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

// ShowRenderedHTML is true when the viewer should swap the line view
// for the inline-blocks view: an HTML file, not toggled to raw, with at
// least one rendered block. Mirrors ShowRenderedMarkdown — a deleted /
// empty file falls through to the line view (showing the diff)
// instead of an empty preview pane.
func (s PrereviewState) ShowRenderedHTML() bool {
	return s.IsHTML() && !s.RawHTML &&
		s.CurrentDiff != nil && len(s.CurrentDiff.HTMLBlocks) > 0
}

// RenderedHTML is the block list for the rendered HTML view (nil unless
// ShowRenderedHTML). Each block carries its real source line range so
// comments stay line-accurate across rendered and raw views — same
// contract as RenderedMarkdown. The preview itself renders in a single
// iframe (RenderedHTMLDoc); these ranges drive the comments list below it.
func (s PrereviewState) RenderedHTML() []gitdiff.HTMLBlock {
	if !s.ShowRenderedHTML() {
		return nil
	}
	return s.CurrentDiff.HTMLBlocks
}

// RenderedHTMLDoc is the preview document for the iframe srcdoc (empty
// unless ShowRenderedHTML). The whole file rendered with real-document
// fidelity; the client wires clicks inside it back to a block via the
// data-from/data-to ranges.
func (s PrereviewState) RenderedHTMLDoc() string {
	if !s.ShowRenderedHTML() {
		return ""
	}
	return s.CurrentDiff.HTMLDoc
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

// URLHash returns the canonical hash string for the current state,
// suitable for placement in `data-lvt-url-hash` on the body. Returns
// "" when no file is selected (the directive then no-ops on mirror).
// Order of precedence: SelectedFile + SelectionAnchor/End (line range
// is the most specific target a user is viewing) > SelectedFile +
// URLHashScrollAnchor > SelectedFile alone. The line-range form
// matches the gutter `<a>` permalinks; the anchor form survives a
// markdown TOC click or an HTML deep link until the user moves.
func (s PrereviewState) URLHash() string {
	if s.SelectedFile == "" {
		return ""
	}
	from, to := s.SelectionAnchor, s.SelectionEnd
	if to < from {
		from, to = to, from
	}
	return gitdiff.FormatHash(s.SelectedFile, from, to, s.URLHashScrollAnchor)
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
