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

	// Artifact versioning (#90). Versions is the selected file's version
	// timeline (newest first), populated from the version store each Mount /
	// version action for the Versions panel. When ViewingVersion is true the
	// right pane shows a historical version (VersionViewSeq) of SelectedFile
	// read-only instead of the live diff — CurrentDiff holds the rendered
	// historical content. ViewingVersion is deliberately NOT lvt:"persist": it
	// is a transient view mode that every live-diff rebuild (Mount, SelectFile,
	// RefreshDiff) must clear, so a reconnect or background re-render lands back
	// on the live diff rather than stale history mislabeled as current.
	Versions       []VersionListItem `json:"versions"`
	ViewingVersion bool              `json:"viewing_version"`
	VersionViewSeq int               `json:"version_view_seq"`
	// VersionViewDiff distinguishes the two historical views: false = the version's
	// content read-only ("View"), true = that version diffed against the current
	// file ("Diff vs current"). Only meaningful while ViewingVersion.
	VersionViewDiff bool `json:"version_view_diff"`

	// AgentPaused mirrors the .prereview/paused marker: a rollback force-pauses
	// the agent so it stops applying while the reviewer re-steers (#90). Read
	// from disk each Mount; M2's continuous drain gates emission on it.
	AgentPaused bool `json:"agent_paused"`

	// Two-click selection (anchor → end). 0 = nothing selected; first
	// click sets both to the same line; second click moves end; third
	// click reseats anchor.
	SelectionAnchor int    `json:"selection_anchor" lvt:"persist"`
	SelectionEnd    int    `json:"selection_end"    lvt:"persist"`
	SelectionSide   string `json:"selection_side"   lvt:"persist"` // "new"|"old"|"both"

	// SelectionFromCol/ToCol/Text hold the in-progress CHARACTER range for a
	// kind=text comment (composer CommentMode == "text"): rune offsets into
	// FromLine/ToLine and the exact selected substring, set by SelectText
	// (dispatched from the client's lvt-fx:text-select directive) and cleared
	// by AddComment / ClearSelection / SelectFile. Persisted so the composer
	// survives a WebSocket reconnect mid-compose.
	SelectionFromCol int    `json:"selection_from_col" lvt:"persist"`
	SelectionToCol   int    `json:"selection_to_col"   lvt:"persist"`
	SelectionText    string `json:"selection_text"     lvt:"persist"`

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

	// Suggestions are the LLM's proposed edits (issue #98), loaded from the
	// agent-owned .prereview/suggestions.jsonl on every Mount (like Comments from
	// the CSV — not lvt:"persist"; the file is the source of truth). Rendered
	// inline as suggestion boxes on the selected file.
	Suggestions []Suggestion `json:"suggestions"`

	// HideSuggestions toggles the inline suggestion boxes off for the current
	// view (a declutter, so the reviewer can read the doc without the proposed
	// edits overlaid). Session-scoped (survives reconnect via persist); does not
	// affect the underlying suggestions.
	HideSuggestions bool `json:"hide_suggestions" lvt:"persist"`

	// Decisions are the reviewer's recorded verdicts on suggestions (issue #98
	// Phase 2), loaded from the server-owned .prereview/suggestion-decisions.jsonl
	// every Mount (the file is the source of truth — not lvt:"persist"). Overlaid
	// onto suggestions by ID + content fingerprint in DecisionsBySuggestion.
	Decisions []SuggestionDecision `json:"decisions"`

	// Hidden is the reviewer's hidden-from-view suggestion set, loaded from the
	// server-owned .prereview/hidden-suggestions.jsonl every Mount (the file is
	// the source of truth — not lvt:"persist"). A pure view filter applied in
	// visibleSuggestions, fingerprint-gated so a revised suggestion reappears.
	Hidden []HiddenSuggestion `json:"hidden_suggestions"`

	// RevisingSuggestionID is the suggestion whose inline "request revision" note
	// form is open (mirrors EditingCommentID); RevisionDraft holds the in-progress
	// note so it survives reconnects. Empty = no revision form open.
	RevisingSuggestionID string `json:"revising_suggestion_id" lvt:"persist"`
	RevisionDraft        string `json:"revision_draft" lvt:"persist"`

	// UI status.
	LastSaved   string `json:"last_saved"`
	DoneWritten bool   `json:"done_written" lvt:"persist"`

	// LLMState mirrors the agent's inbound status signal
	// (.prereview/llm-status.json): "working" while the agent applies a handoff
	// batch, "done" once finished, "" idle. LLMMessage is the optional detail
	// shown in the pill. Written by the agent (skill), watched by the server, and
	// pushed to every open tab via the llm-status watcher → LLMStatusChanged
	// fan-out; also refreshed from the file on each connect so a late/reconnecting
	// tab shows current status. Not persisted — the file is the source of truth
	// (like Comments), re-read every connect. (The file also carries updated_at;
	// it's a debug field, not surfaced in the UI, so it isn't mirrored here.)
	LLMState   string `json:"llm_state"`
	LLMMessage string `json:"llm_message"`

	// PendingRefresh is set when the agent's status transitions working→done:
	// the agent just edited files, so this tab's diff is now stale. It drives a
	// non-intrusive "Changes applied — Refresh diff" affordance the user clicks
	// to reload (RefreshDiff), preserving scroll + any in-progress draft until
	// then. Per-connection and NOT persisted: a reconnect re-runs Mount, which
	// rebuilds the diff fresh, so the nudge is moot after a reload.
	PendingRefresh bool `json:"pending_refresh"`

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

	// Read progress (#128), keyed by file path. ReadThrough is the furthest
	// new-side line number the reviewer has scrolled past (a high-water mark →
	// lines at/above it render "read"). LastReadTopKey is the topmost visible line
	// key at the last report → the scroll-restore target when the file is
	// re-opened. Both are reported by the client's lvt-fx:viewport-report directive
	// and persisted so they survive a reconnect within the review session. See
	// readprogress.go.
	ReadThrough    map[string]int    `json:"read_through" lvt:"persist"`
	LastReadTopKey map[string]string `json:"last_read_top_key" lvt:"persist"`

	// ScrollToReadKey is the transient scroll-restore target: SelectFile sets it to
	// the file's LastReadTopKey so the browser re-opens where the reviewer left off.
	// NOT persisted — a one-render nudge (like ScrollToCommentID), cleared once the
	// reviewer scrolls (ReportViewport) so a re-render can't yank them back.
	ScrollToReadKey string `json:"scroll_to_read_key"`

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
	// focuses on what's still actionable. A durable per-user view pref: NOT
	// lvt:"persist" — it lives in the on-disk prefs file (see uiprefs.go) so it
	// survives a server relaunch, not just a reload; applyUIPrefs reloads it on
	// every Mount/OnConnect.
	ShowResolved bool `json:"show_resolved"`

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

	// SearchOpen drives the cmd+k search palette (issue #91), toggled by the
	// Mod+k binding / the toolbar magnifier (openSearch) and closed by Esc
	// (ClearSelection) or the close button. SearchQuery is the live-debounced
	// text; SearchScopeAll switches between the changed set (default) and all
	// files; SearchHits is the computed result list (filled by the controller —
	// a zero-arg state method can't reach loadDiffCached). None persisted: a
	// reopened tab shouldn't resurrect a stale palette (mirrors KeyHelpOpen).
	SearchOpen     bool        `json:"search_open"`
	SearchQuery    string      `json:"search_query"`
	SearchScopeAll bool        `json:"search_scope_all"`
	SearchHits     []SearchHit `json:"search_hits"`

	// RevealFile is a search-jump's transient "show this file's full RAW source"
	// override: JumpToSearchResult sets it to the hit's path so the exact matched
	// line exists in the DOM to scroll to — even in an unchanged region diff-view
	// would fold, and even for Markdown/HTML (which otherwise render as blocks /
	// preview with no L<old>-<new> rows). Honoured by Revealing() → VisibleLines
	// (full file) + ShowRenderedMarkdown/ShowRenderedHTML (fall to line view). It
	// deliberately does NOT touch the durable FileView pref. Cleared on SelectFile
	// so it never leaks onto later navigation. Not persisted.
	RevealFile string `json:"reveal_file"`

	// CursorKey is the data-key ("L<old>-<new>") of the diff line the keyboard
	// line cursor is on. ArrowUp/ArrowDown move it (CursorUp/CursorDown); the
	// matching line button is highlighted, scrolled into view, and focused
	// (lvt-autofocus) so Enter activates it → the line composer. Empty = no
	// cursor yet (first arrow press seeds it). Not persisted — a transient
	// navigation aid. Reset when the selected file changes.
	CursorKey string `json:"cursor_key"`

	// Flash is a transient status message shown as an auto-dismissing toast —
	// e.g. pressing "r" (Show resolved) when there are no resolved comments,
	// where toggling would otherwise do nothing visible. Set by the action,
	// cleared by clearFlash (auto after a few seconds, or manual dismiss). Not
	// persisted; a refresh shouldn't resurrect a stale notice.
	Flash string `json:"flash"`

	// FileView, when true, turns off the diff overlay: deleted lines are
	// hidden, +/- gutter markers disappear, and add/del row coloring is
	// dropped. The user sees the file as it currently exists in the
	// working tree. Equivalent to GitHub's "View file" toggle. A durable
	// per-user view pref (see uiprefs.go / ShowResolved) — not lvt:"persist".
	// Defaults false (diff is the primary reviewing mode).
	FileView bool `json:"file_view"`

	// RawMarkdown shows a .md/.markdown file as the raw line view
	// instead of the rendered default. Durable per-user view pref (uiprefs.go).
	// Defaults false: Markdown renders by default; the user toggles to raw to
	// see the source lines. Non-Markdown files ignore this.
	RawMarkdown bool `json:"raw_markdown"`

	// RawHTML is the .html/.htm equivalent of RawMarkdown: when true the
	// viewer shows the syntax-highlighted source instead of the
	// sandboxed-iframe preview. Durable per-user view pref (uiprefs.go).
	// Defaults false. Independent of RawMarkdown so a user's preference for one
	// format doesn't drag the other along. Non-HTML files ignore this.
	RawHTML bool `json:"raw_html"`

	// FocusMode, when true, hides both desktop side columns (the file
	// drawer on the left and the TOC sidebar on the right) so the center
	// reading surface gets the full width — a distraction-free reading
	// view for long docs/diffs. Desktop-only in effect: the hiding CSS
	// lives behind the ≥900px media query; on mobile the columns are
	// already overlays/modals, so the flag is a harmless no-op there.
	// Durable per-user view pref (uiprefs.go), like FileView.
	FocusMode bool `json:"focus_mode"`

	// ThemeMode is the Light/Dark/System color-mode preference (issue #60),
	// cycled by the toolbar toggle. "" means System (the default): the page
	// omits the data-mode attribute and follows the OS via prefers-color-scheme
	// (see the Solarized-dark block + /syntax.css). "light"/"dark" force the
	// mode regardless of the OS. Durable per-user view pref (uiprefs.go).
	// Orthogonal to SchemeName.
	ThemeMode string `json:"theme_mode"`

	// SchemeName is the curated color-scheme preference — the Theme axis,
	// orthogonal to ThemeMode's Light/Dark/System. "" (and any unregistered
	// value) means the default scheme, gitdiff.Schemes[0] (Solarized). Cycled
	// by the toolbar theme picker; the page re-renders with the new data-scheme
	// attribute on .theme-root and the cascade re-skins chrome + diff —
	// /syntax.css already carries every registered scheme × mode, so there is
	// no JS and no CSS refetch. Durable per-user view pref (uiprefs.go).
	SchemeName string `json:"scheme_name"`

	// BaseChoices populates the base-picker dropdown. Computed in
	// Mount: ["HEAD", "HEAD~1", "HEAD~5", <local branches…>] plus the
	// current state.Base if it isn't already in the list (so custom
	// refs typed via the freeform fallback still appear as the
	// selected option). Not persisted — recomputed each Mount so newly
	// created branches show up without a process restart.
	BaseChoices []string `json:"base_choices"`
}

// commentHiddenFromView is the single visibility rule for resolved/hidden
// state, replacing the copy-pasted `c.Resolved && !s.ShowResolved` guards
// scattered across the view helpers (issue #88). A RESOLVED comment is omitted
// from every view when either the whole resolved group is hidden (ShowResolved
// off) OR it has been individually re-hidden (Hidden). Non-resolved comments are
// never hidden by this rule — Hidden is meaningless on them.
func (s PrereviewState) commentHiddenFromView(c Comment) bool {
	return c.Resolved && (!s.ShowResolved || c.Hidden)
}

// VisibleComments returns Comments filtered by the resolved/hidden view rule.
// Zero-arg so the framework eagerly evaluates and the template iterates the
// filtered list directly. Note there is no `if ShowResolved { return all }`
// fast-path: an individually-hidden resolved comment must stay out even when the
// group is shown.
func (s PrereviewState) VisibleComments() []Comment {
	out := make([]Comment, 0, len(s.Comments))
	for _, c := range s.Comments {
		if s.commentHiddenFromView(c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ResolvedCount returns how many of the current comments are resolved —
// useful for "(N resolved hidden)" status copy. Counts every resolved comment,
// individually-hidden or not (it gates the "Show resolved" toggle's visibility).
func (s PrereviewState) ResolvedCount() int {
	n := 0
	for _, c := range s.Comments {
		if c.Resolved {
			n++
		}
	}
	return n
}

// HiddenResolvedCount returns how many resolved comments have been individually
// re-hidden — drives the "Unhide N" affordance next to the Show-resolved toggle.
func (s PrereviewState) HiddenResolvedCount() int {
	n := 0
	for _, c := range s.Comments {
		if c.Resolved && c.Hidden {
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

// DataMode is the value for the .theme-root data-mode attribute: "light" or
// "dark" when the mode is forced, "" for System (in which case the template
// omits the attribute and prefers-color-scheme drives the theme). Zero-arg so
// the page template can emit `{{with .DataMode}} data-mode="{{.}}"{{end}}`.
func (s PrereviewState) DataMode() string {
	switch s.ThemeMode {
	case "light", "dark":
		return s.ThemeMode
	default:
		return "" // System (and any unrecognised value) → follow the OS
	}
}

// NextThemeMode is the mode the toolbar toggle advances to from the current
// one, cycling System → Light → Dark → System. Drives both the CycleTheme
// action and the toggle's label/icon ("show what a click switches TO").
func (s PrereviewState) NextThemeMode() string {
	switch s.ThemeMode {
	case "light":
		return "dark"
	case "dark":
		return "" // back to System
	default:
		return "light" // System → Light
	}
}

// ThemeModeLabel / NextThemeModeLabel are the human names of the current and
// next modes, for the toggle's tooltip ("Theme: System — switch to Light") and
// the overflow-menu item. (The button's icon is chosen by an explicit ladder
// in the template, since Go templates can't take a dynamic {{template}} name.)
func (s PrereviewState) ThemeModeLabel() string     { return themeModeLabel(s.ThemeMode) }
func (s PrereviewState) NextThemeModeLabel() string { return themeModeLabel(s.NextThemeMode()) }

// themeModeLabel names a mode string; "" (and anything unrecognised) is System.
func themeModeLabel(mode string) string {
	switch mode {
	case "light":
		return "Light"
	case "dark":
		return "Dark"
	default:
		return "System"
	}
}

// DataScheme is the value for the .theme-root data-scheme attribute: the
// persisted SchemeName when it names a registered scheme, else the default
// (gitdiff.Schemes[0]). Always non-empty so the wrapper always carries a
// scheme — mirrors DataMode's fall-through-to-default contract. Zero-arg so
// the page template can emit `data-scheme="{{.DataScheme}}"`.
func (s PrereviewState) DataScheme() string {
	for _, sc := range gitdiff.Schemes {
		if sc.Name == s.SchemeName {
			return sc.Name
		}
	}
	return gitdiff.Schemes[0].Name
}

// NextScheme is the scheme the picker advances to from the current one,
// cycling gitdiff.Schemes in registry order and wrapping back to the first.
// Drives both the CycleScheme action and the picker's "switch to" label.
func (s PrereviewState) NextScheme() string {
	cur := s.DataScheme()
	for i, sc := range gitdiff.Schemes {
		if sc.Name == cur {
			return gitdiff.Schemes[(i+1)%len(gitdiff.Schemes)].Name
		}
	}
	return gitdiff.Schemes[0].Name
}

// SchemeLabel / NextSchemeLabel are the human names (gitdiff.Scheme.Label) of
// the current and next schemes, for the picker's tooltip ("Theme: Solarized —
// switch to Gruvbox") and the overflow-menu item.
func (s PrereviewState) SchemeLabel() string     { return schemeLabel(s.DataScheme()) }
func (s PrereviewState) NextSchemeLabel() string { return schemeLabel(s.NextScheme()) }

// schemeLabel maps a scheme name to its registry Label; an unknown name
// echoes back unchanged (defensive — DataScheme/NextScheme never produce one).
func schemeLabel(name string) string {
	for _, sc := range gitdiff.Schemes {
		if sc.Name == name {
			return sc.Label
		}
	}
	return name
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
	// FileView (durable pref) OR Revealing() (a transient search-jump reveal) OR
	// a file carrying visible LLM suggestions shows the whole file so an arbitrary
	// target line is present; otherwise diff view collapses unchanged runs into
	// folds. The suggestion case is load-bearing (#98): the LLM emits arbitrary
	// line numbers, so a suggestion can land on an unchanged line that diff-view
	// would fold away — hiding the box with no hint. Revealing the full file keeps
	// every suggestion visible (no effect when suggestions are toggled off —
	// FileSuggestions returns nil then).
	if s.FileView || s.Revealing() || len(s.FileSuggestions()) > 0 {
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
	// Revealing() forces the raw line view so a search-jump lands on the exact
	// source line (rendered blocks carry no L<old>-<new> rows to scroll to).
	// A version DIFF also forces the line view — a diff must show add/del rows,
	// not the rendered Markdown of one side (#90). A plain version VIEW keeps the
	// rendered preview (seeing the old doc rendered is useful).
	return s.IsMarkdown() && !s.RawMarkdown && !s.Revealing() && !s.diffingVersion() &&
		len(s.CurrentDiff.MarkdownBlocks) > 0
}

// diffingVersion reports whether the pane is showing a version-vs-current diff
// (as opposed to a plain read-only version view or the live diff).
func (s PrereviewState) diffingVersion() bool {
	return s.ViewingVersion && s.VersionViewDiff
}

// ShowRenderedHTML is true when the viewer should swap the line view
// for the inline-blocks view: an HTML file, not toggled to raw, with at
// least one rendered block. Mirrors ShowRenderedMarkdown — a deleted /
// empty file falls through to the line view (showing the diff)
// instead of an empty preview pane.
func (s PrereviewState) ShowRenderedHTML() bool {
	// Revealing() forces the raw line view so a search-jump lands on the exact
	// source line (the preview iframe has no L<old>-<new> rows to scroll to).
	return s.IsHTML() && !s.RawHTML && !s.Revealing() && !s.diffingVersion() &&
		s.CurrentDiff != nil && len(s.CurrentDiff.HTMLBlocks) > 0
}

// Revealing reports whether the currently-selected file is being shown as its
// full RAW source because a search-jump set RevealFile to it — see RevealFile.
// Zero-arg so VisibleLines / ShowRendered* can gate on it.
func (s PrereviewState) Revealing() bool {
	return s.RevealFile != "" && s.CurrentDiff != nil && s.CurrentDiff.Path == s.RevealFile
}

// SearchScopeCount is the number of files the current search scope covers —
// changed files by default, or every file when SearchScopeAll. Drives the scope
// toggle's count. Zero-arg for the template.
func (s PrereviewState) SearchScopeCount() int {
	if s.SearchScopeAll {
		return len(s.Files)
	}
	return s.ChangedFilesCount()
}

// SearchScopeLabel is the human label for the current search scope toggle.
func (s PrereviewState) SearchScopeLabel() string {
	if s.SearchScopeAll {
		return "All files"
	}
	return "Changed files"
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

// ScopeFileCount / ScopeViewedCount drive the "X/Y viewed" toolbar progress so
// it counts against the files actually in the review SCOPE (what the drawer
// shows) — changed-only by default. So a repo with 144 tracked files and 1
// changed reads "0/1 viewed", not "0/144". Falls back to all files when the
// scope is all (clean tree or ShowAllFiles), consistent with scopedFiles.
// Zero-arg so the template can read them directly.
func (s PrereviewState) ScopeFileCount() int { return len(s.scopedFiles()) }

func (s PrereviewState) ScopeViewedCount() int {
	if len(s.ViewedFiles) == 0 {
		return 0
	}
	n := 0
	for _, f := range s.scopedFiles() {
		if s.ViewedFiles[f.Path] {
			n++
		}
	}
	return n
}
