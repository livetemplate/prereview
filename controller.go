package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
)

// PrereviewController holds singleton dependencies. State is cloned per
// session by the framework — never store per-user data on the controller.
type PrereviewController struct {
	// RepoPath, Base, CSVPath, DonePath are set once by main.go and are
	// read-only. CSVWriter is a goroutine-safe serializer over CSVPath.
	RepoPath string
	Base     string
	CSVPath  string
	DonePath string

	// NoGit is true when --repo was a single file or a directory with no
	// .git: the file list and per-file diff are synthesized from the
	// filesystem (gitdiff.ListFilesNoGit / LoadDiffNoGit) instead of git,
	// the base picker is suppressed, and Base is unused. SingleFile, when
	// non-empty (single-file mode), is the only reviewable file —
	// RepoPath is its parent directory.
	NoGit      bool
	SingleFile string
	// Version is the build version (main.version; "dev" for source
	// builds) surfaced into state for the footer.
	Version   string
	CSVWriter *csv.Writer

	// SkillMode is true when prereview is launched via `--skill` (the
	// Claude skill sets this). It selects the top-bar button label:
	// "Hand off → Claude" vs "Quit".
	SkillMode bool

	// ShutdownReq receives a struct{} when the user clicks Quit. main.go
	// listens for it and triggers graceful HTTP shutdown.
	ShutdownReq chan<- struct{}

	// diffCache memoises parsed+highlighted FileDiffs so switching back
	// to a file the user has already viewed skips the git shell + parser
	// + chroma tokenize cost (~50-150 ms for medium-large files). Keyed
	// by `base + "\t" + path`; invalidated when the working-tree file's
	// mtime changes (covers user edits, branch swaps that touch the
	// file, and stash pops). Safe for concurrent use via sync.Map —
	// concurrent reads of the same file are racy but the worst case is
	// re-doing the work, not a stale read of stale data.
	diffCache sync.Map // map[string]cachedDiff
}

type cachedDiff struct {
	diff  *gitdiff.FileDiff
	mtime time.Time // working-tree file mtime when this was cached
}

// loadDiffCached returns the highlighted FileDiff for (base, path), reusing
// a previously-parsed copy when the working-tree file's mtime hasn't
// changed since it was cached.
func (c *PrereviewController) loadDiffCached(base, path string) (*gitdiff.FileDiff, error) {
	key := base + "\t" + path
	curMtime := fileMtime(filepath.Join(c.RepoPath, path))
	if v, ok := c.diffCache.Load(key); ok {
		cd := v.(cachedDiff)
		if cd.mtime.Equal(curMtime) {
			return cd.diff, nil
		}
	}
	var diff *gitdiff.FileDiff
	var err error
	if c.NoGit {
		diff, err = gitdiff.LoadDiffNoGit(c.RepoPath, path)
	} else {
		diff, err = gitdiff.LoadDiff(c.RepoPath, base, path)
	}
	if err != nil {
		return nil, err
	}
	c.diffCache.Store(key, cachedDiff{diff: diff, mtime: curMtime})
	return diff, nil
}

// relocateSelected re-anchors the selected file's comments against
// CurrentDiff and self-heals the CSV. Best-effort: a persist error is
// logged, not fatal.
func (c *PrereviewController) relocateSelected(state *PrereviewState) {
	if state.CurrentDiff == nil || state.SelectedFile == "" {
		return
	}
	if relocateComments(state.Comments, state.SelectedFile, state.CurrentDiff) {
		if err := c.persist(state.Comments); err != nil {
			slog.Warn("self-heal persist (selected file)", "err", err)
		}
	}
}

// relocateAll re-anchors every commented file (loading each diff via
// the per-file cache) and self-heals the CSV once if anything changed.
// Used where the CSV / all-comments overview must be accurate even for
// files the user never opened this session: the skill handoff and
// opening the all-comments view.
func (c *PrereviewController) relocateAll(state *PrereviewState) {
	seen := map[string]bool{}
	anyChanged := false
	for _, cm := range state.Comments {
		if cm.Resolved || cm.Anchor.Empty() || seen[cm.File] {
			continue
		}
		seen[cm.File] = true
		diff, err := c.loadDiffCached(state.Base, cm.File)
		if err != nil {
			slog.Warn("relocateAll: load diff", "file", cm.File, "err", err)
			continue
		}
		if relocateComments(state.Comments, cm.File, diff) {
			anyChanged = true
		}
	}
	if anyChanged {
		if err := c.persist(state.Comments); err != nil {
			slog.Warn("self-heal persist (all files)", "err", err)
		}
	}
}

// fileMtime returns the file's mtime or the zero time if it can't be
// stat'd. Used as the cache-invalidation hash for diffCache.
func fileMtime(full string) time.Time {
	st, err := os.Stat(full)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// Mount runs on every HTTP GET and WebSocket connect. It rebuilds the file
// list from `git diff` so the user sees current state without restarting
// the binary.
func (c *PrereviewController) Mount(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	// Reload comments from CSV every Mount. The framework doesn't persist
	// state.Comments across WebSocket reconnects (it's not lvt:"persist"
	// tagged, and we don't want it to be — comments can be large).
	// The CSV file is the source of truth.
	state.Comments = c.loadCommentsFromDisk()

	// SkillMode is mirror-only: refresh from the controller every connect
	// so a binary launched with --skill renders the right button even after
	// a session-storage reconnect.
	state.SkillMode = c.SkillMode

	// NoGit is mirror-only too (controller is source of truth): the
	// template hides the base picker and the diff/file-view affordances
	// stay diff-free when there's no git base.
	state.NoGit = c.NoGit

	// First connect: hydrate state.Base from the CLI flag. Subsequent
	// reconnects keep the user's choice (state.Base is lvt:persist).
	if state.Base == "" {
		state.Base = c.Base
	}

	var files []gitdiff.FileEntry
	var err error
	if c.NoGit {
		// No git base ⇒ no refs to pick and no diff to compute. The file
		// list is the filesystem; every file is wholly "new".
		state.BaseChoices = nil
		files, err = gitdiff.ListFilesNoGit(c.RepoPath, c.SingleFile)
	} else {
		// Populate the base-picker dropdown choices fresh each Mount so
		// newly created branches appear without restarting the server.
		// The current state.Base is prepended if it's not a preset/branch
		// (e.g., a typed commit SHA) so the select still shows what we're
		// currently comparing against.
		choices := []string{"HEAD", "HEAD~1", "HEAD~3", "HEAD~5", "HEAD~10"}
		choices = append(choices, gitdiff.ListBranches(c.RepoPath)...)
		choices = append(choices, gitdiff.ListRemoteBranches(c.RepoPath)...)
		choices = uniqueStrings(choices)
		if !slices.Contains(choices, state.Base) {
			choices = append([]string{state.Base}, choices...)
		}
		state.BaseChoices = choices

		files, err = gitdiff.ListFiles(c.RepoPath, state.Base)
	}
	if err != nil {
		return state, fmt.Errorf("list files: %w", err)
	}
	state.Files = files
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	state.CSVPath = c.CSVPath
	state.Version = c.Version

	// If the previously-selected file disappeared (working tree edits,
	// branch swap), reset before the auto-select block fires so we pick
	// a still-existing file instead of trying to load a stale path.
	if state.SelectedFile != "" && !fileInList(state.Files, state.SelectedFile) {
		state.SelectedFile = ""
		state.CurrentDiff = nil
	}

	// Auto-select the first file when nothing is selected — happens on
	// initial connect and after the gone-file reset above. Saves the
	// user a tap and avoids landing on the empty "Pick a file" state.
	// Pick from the scoped list so we don't land on an unchanged file
	// that the (default changed-only) drawer isn't even showing.
	if state.SelectedFile == "" {
		if scoped := state.scopedFiles(); len(scoped) > 0 {
			state.SelectedFile = scoped[0].Path
		}
	}

	// Eager-load the diff for the selected file so the right pane is
	// populated on first paint (no second-roundtrip needed).
	if state.SelectedFile != "" {
		diff, err := c.loadDiffCached(state.Base, state.SelectedFile)
		if err != nil {
			slog.Warn("load diff in mount", "path", state.SelectedFile, "err", err)
			state.CurrentDiff = nil
		} else {
			state.CurrentDiff = diff
		}
	}
	// Re-anchor the selected file's comments against the just-loaded
	// (live working-tree) diff so a doc edited since the comment was
	// made shows the comment on its content, not a stale line.
	c.relocateSelected(&state)
	return state, nil
}

// SetBase changes the comparison ref. The picker is a dropdown of refs
// we enumerated this Mount (HEAD~N presets + local/remote branches), so
// the value is already a valid ref. The rev-parse check stays as cheap
// defense against a race (a branch deleted between Mount and select);
// on a miss we just no-op and keep the current base.
//
// On success, rebuilds the file list against the new base. If the
// previously selected file no longer exists in the new file list,
// SelectedFile is cleared.
func (c *PrereviewController) SetBase(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.NoGit {
		// No refs in no-git mode; the picker is hidden. Guard anyway so a
		// stale persisted client can't shell git for a meaningless ref.
		return state, nil
	}
	ref := strings.TrimSpace(ctx.GetString("ref"))
	if ref == "" || !gitdiff.IsValidRef(c.RepoPath, ref) {
		return state, nil
	}
	state.Base = ref

	files, err := gitdiff.ListFiles(c.RepoPath, state.Base)
	if err != nil {
		return state, fmt.Errorf("list files for base %q: %w", ref, err)
	}
	state.Files = annotateCommentCounts(files, state.Comments)

	// If the previously selected file isn't in the new diff range, reset
	// so we don't render a stale viewer. Drawer reopens so the user can
	// pick from the new file list.
	if state.SelectedFile != "" && !fileInList(state.Files, state.SelectedFile) {
		state.SelectedFile = ""
		state.CurrentDiff = nil
		state.FileDrawerOpen = true
	} else if state.SelectedFile != "" {
		// Same file is still in the diff — reload it against the new base.
		diff, err := c.loadDiffCached(state.Base, state.SelectedFile)
		if err != nil {
			slog.Warn("reload diff after SetBase", "path", state.SelectedFile, "err", err)
			state.CurrentDiff = nil
		} else {
			state.CurrentDiff = diff
		}
	}
	return state, nil
}

// SelectFile loads the diff for the file path supplied as a hidden form
// input (`name="path"`). Resets any line selection. Auto-closes the
// mobile file drawer so tapping a file goes straight to the diff —
// the user doesn't have to also close the drawer manually.
func (c *PrereviewController) SelectFile(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path := ctx.GetString("path")
	if path == "" {
		return state, fmt.Errorf("selectFile: missing path")
	}
	diff, err := c.loadDiffCached(state.Base, path)
	if err != nil {
		return state, fmt.Errorf("load diff %s: %w", path, err)
	}
	state.SelectedFile = path
	state.CurrentDiff = diff
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.FileDrawerOpen = false
	c.relocateSelected(&state)
	return state, nil
}

// ToggleFiles flips the mobile file-drawer's open state. Bound to the
// hamburger button and to the drawer's backdrop "close" form.
func (c *PrereviewController) ToggleFiles(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileDrawerOpen = !state.FileDrawerOpen
	return state, nil
}

// NextFile selects the next file in state.Files relative to SelectedFile.
// Wraps to the first file from the last. If no file is selected, picks the
// first file. Falls back to no-op for an empty file list.
func (c *PrereviewController) NextFile(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.stepFile(state, +1)
}

// PrevFile selects the previous file. Wraps to the last file from the first.
func (c *PrereviewController) PrevFile(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.stepFile(state, -1)
}

func (c *PrereviewController) stepFile(state PrereviewState, delta int) (PrereviewState, error) {
	files := state.scopedFiles()
	if len(files) == 0 {
		return state, nil
	}
	cur := -1
	for i, f := range files {
		if f.Path == state.SelectedFile {
			cur = i
			break
		}
	}
	next := cur + delta
	n := len(files)
	// Wrap. (-1+1)%n = 0 (lands on first file when nothing selected and Next).
	next = ((next % n) + n) % n
	path := files[next].Path
	diff, err := c.loadDiffCached(state.Base, path)
	if err != nil {
		return state, fmt.Errorf("load diff %s: %w", path, err)
	}
	state.SelectedFile = path
	state.CurrentDiff = diff
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.LastDeletedComment = nil
	state.EditingCommentID = ""
	c.relocateSelected(&state)
	return state, nil
}

// CloseFiles unconditionally hides the file drawer. Distinct from
// ToggleFiles because the backdrop tap should only close, never open.
func (c *PrereviewController) CloseFiles(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileDrawerOpen = false
	return state, nil
}

// ToggleViewed flips the "reviewed" flag for the file passed via the hidden
// `path` input. Bound to a per-file checkbox/button in the drawer.
func (c *PrereviewController) ToggleViewed(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	path := ctx.GetString("path")
	if path == "" {
		return state, fmt.Errorf("toggleViewed: missing path")
	}
	if state.ViewedFiles == nil {
		state.ViewedFiles = map[string]bool{}
	}
	if state.ViewedFiles[path] {
		delete(state.ViewedFiles, path)
	} else {
		state.ViewedFiles[path] = true
	}
	return state, nil
}

// SetFileFilter updates the search filter applied to the file drawer.
// Bound to the search input via lvt-on:input with a debounce modifier.
func (c *PrereviewController) SetFileFilter(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileFilter = ctx.GetString("filter")
	return state, nil
}

// ToggleCommentList flips between the diff viewer and the all-comments
// overview pane. Bound to the "N comments" entry in the overflow menu.
// Closes the menu so the user sees the result immediately.
func (c *PrereviewController) ToggleCommentList(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ShowAllComments = !state.ShowAllComments
	state.MoreMenuOpen = false
	// Opening the overview must show accurate badges/snippets for every
	// commented file, including ones never opened this session.
	if state.ShowAllComments {
		c.relocateAll(&state)
	}
	return state, nil
}

// ToggleShowResolved flips whether resolved comments are visible in the
// inline diff and the all-comments overview. Default off — resolved
// comments add noise once they're handled. Bound to an entry in the
// overflow menu. Closes the menu so the user can immediately see the
// effect on the diff.
func (c *PrereviewController) ToggleShowResolved(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ShowResolved = !state.ShowResolved
	state.MoreMenuOpen = false
	return state, nil
}

// ToggleMoreMenu opens/closes the 3-dots overflow menu in the top bar.
// Mirrors the file-drawer pattern: state-driven boolean + CSS class
// toggle. No JS. Backdrop tap submits CloseMoreMenu.
func (c *PrereviewController) ToggleMoreMenu(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.MoreMenuOpen = !state.MoreMenuOpen
	return state, nil
}

// CloseMoreMenu is the explicit close action — bound to the menu
// backdrop so tapping outside dismisses without toggling the open state
// to "true" on a subsequent click.
func (c *PrereviewController) CloseMoreMenu(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.MoreMenuOpen = false
	return state, nil
}

// ToggleFileView flips between diff-overlay mode (default) and plain
// file-view mode. See PrereviewState.FileView. Closes the overflow
// menu so the effect on the diff is immediately visible.
func (c *PrereviewController) ToggleFileView(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.FileView = !state.FileView
	state.MoreMenuOpen = false
	return state, nil
}

// ToggleFileScope flips the drawer file list between changed-only
// (default) and all tracked files. See PrereviewState.ShowAllFiles and
// scopedFiles. Lives in the drawer, so no overflow-menu interaction.
func (c *PrereviewController) ToggleFileScope(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ShowAllFiles = !state.ShowAllFiles
	return state, nil
}

// ToggleRawMarkdown flips a Markdown file between the rendered view
// (default) and the raw line view. Closes the overflow menu so the
// effect is immediately visible on mobile.
func (c *PrereviewController) ToggleRawMarkdown(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.RawMarkdown = !state.RawMarkdown
	state.MoreMenuOpen = false
	return state, nil
}

// JumpToComment closes the all-comments view, selects the comment's
// file, and sets ScrollToCommentID so the framework's
// `lvt-fx:scroll="into-view"` directive on the matching inline comment
// scrolls it into view on the next render. Pure declarative wiring —
// no custom app-level JS.
func (c *PrereviewController) JumpToComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("jumpToComment: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("jumpToComment: id %s not found", id)
	}
	cm := state.Comments[idx]
	if cm.File != state.SelectedFile {
		diff, err := c.loadDiffCached(state.Base, cm.File)
		if err != nil {
			return state, fmt.Errorf("load diff: %w", err)
		}
		state.SelectedFile = cm.File
		state.CurrentDiff = diff
	}
	state.ShowAllComments = false
	state.ScrollToCommentID = cm.ID
	return state, nil
}

// DismissBanner hides the DONE confirmation banner. The on-disk DONE marker
// and CSV file are unaffected — this only clears the UI signal, freeing
// header space. Clicking Done again will rewrite the marker and reshow
// the banner.
func (c *PrereviewController) DismissBanner(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.DoneWritten = false
	return state, nil
}

// SelectLine implements two-click range selection. data-line and data-side
// arrive from <button lvt-on:click="selectLine" data-line="N" data-side="new">.
//
//   - First click on a line: anchor = end = N.
//   - Second click on a different line: end = N (range complete).
//   - Third click: reseat as a new anchor.
//
// Side is captured on the first click and locked thereafter so a range
// can't accidentally span sides of the diff.
func (c *PrereviewController) SelectLine(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	n := ctx.GetInt("line")
	if n <= 0 {
		return state, fmt.Errorf("selectLine: missing or invalid 'line'")
	}
	side := ctx.GetString("side")
	if side == "" {
		side = "new"
	}

	switch {
	case state.SelectionAnchor == 0:
		// First click — start a new range.
		state.SelectionAnchor = n
		state.SelectionEnd = n
		state.SelectionSide = side
	case state.SelectionAnchor != 0 && state.SelectionAnchor == state.SelectionEnd:
		// Anchor placed but not yet extended — this click sets the end.
		// Reject cross-side extensions; require explicit ClearSelection first.
		if side != state.SelectionSide {
			state.SelectionAnchor = n
			state.SelectionEnd = n
			state.SelectionSide = side
		} else {
			state.SelectionEnd = n
		}
	default:
		// Range already complete — start over from this line.
		state.SelectionAnchor = n
		state.SelectionEnd = n
		state.SelectionSide = side
	}
	return state, nil
}

// SelectBlock selects a whole rendered-Markdown block in one click.
// A block IS a range, so unlike SelectLine's two-click anchor/extend,
// this sets the full source line span at once (data-from/data-to are
// the block's real source lines). The existing composer/AddComment
// flow then anchors the comment to those lines, so it round-trips
// with the raw view and the CSV unchanged.
func (c *PrereviewController) SelectBlock(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	from := ctx.GetInt("from")
	to := ctx.GetInt("to")
	if from <= 0 || to < from {
		return state, fmt.Errorf("selectBlock: invalid range from=%d to=%d", from, to)
	}
	state.SelectionAnchor = from
	state.SelectionEnd = to
	state.SelectionSide = "new"
	return state, nil
}

// ClearSelection wipes the line selection and any draft. Bound to a
// "Cancel" button next to the composer and to ESC keydown on the body.
func (c *PrereviewController) ClearSelection(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.DraftBody = ""
	state.EditingCommentID = ""
	state.ReanchorCommentID = ""
	return state, nil
}

// SaveDraft updates DraftBody as the user types. Bound to the textarea's
// change event so the draft survives reconnect (state has lvt:"persist").
func (c *PrereviewController) SaveDraft(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.DraftBody = ctx.GetString("body")
	return state, nil
}

// AddComment validates body+selection, appends a Comment, writes the CSV
// atomically, and clears selection + draft for the next round.
func (c *PrereviewController) AddComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	body := strings.TrimSpace(ctx.GetString("body"))
	if body == "" {
		return state, fmt.Errorf("comment body cannot be empty")
	}
	if state.SelectedFile == "" {
		return state, fmt.Errorf("no file selected")
	}
	if state.SelectionAnchor == 0 {
		return state, fmt.Errorf("no line selected")
	}

	from, to := state.SelectionAnchor, state.SelectionEnd
	if from > to {
		from, to = to, from
	}

	// Re-anchor mode: the user picked a NEW location for an outdated
	// comment. Re-point it and re-capture its anchor at the chosen
	// range; this is the sanctioned move path (Edit is hidden for
	// outdated comments). Self-contained: own persist + reset.
	if state.ReanchorCommentID != "" {
		idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == state.ReanchorCommentID })
		if idx >= 0 {
			prev := state.Comments[idx]
			state.Comments[idx].Body = body
			state.Comments[idx].FromLine = from
			state.Comments[idx].ToLine = to
			state.Comments[idx].Side = state.SelectionSide
			state.Comments[idx].Anchor = captureAnchor(state.CurrentDiff, from, to, state.SelectionSide)
			state.Comments[idx].AnchorStatus = anchorOK
			if err := c.persist(state.Comments); err != nil {
				state.Comments[idx] = prev
				return state, fmt.Errorf("persist re-anchor: %w", err)
			}
			state.SelectionAnchor = 0
			state.SelectionEnd = 0
			state.SelectionSide = ""
			state.DraftBody = ""
			state.ReanchorCommentID = ""
			state.EditingCommentID = ""
			state.LastDeletedComment = nil
			state.LastSaved = time.Now().Format("15:04:05")
			state.Files = annotateCommentCounts(state.Files, state.Comments)
			return state, nil
		}
		// Comment vanished (session race) — drop the flag and fall
		// through to the normal add path rather than lose the body.
		state.ReanchorCommentID = ""
	}

	// Edit-mode: state.EditingCommentID was set by EditComment when the
	// user clicked Edit on an existing comment. Update that comment in
	// place rather than appending a new one. ID, Created, and Resolved
	// stay the same; body, line range, and side may change.
	//
	// If the user concurrently deleted the comment we're "editing"
	// (e.g., a session race), the lookup misses and we fall through to
	// the append path — better to surface the change as a new comment
	// than to lose the body the user typed.
	var rollback func()
	if state.EditingCommentID != "" {
		idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == state.EditingCommentID })
		if idx >= 0 {
			prev := state.Comments[idx]
			state.Comments[idx].Body = body
			if prev.AnchorStatus == anchorOutdated {
				// The stored range points at unrelated content (the
				// original is gone). Re-capturing here would silently
				// re-anchor the comment to whatever now sits there and
				// stamp it ok. Only the body changes; the user must use
				// Re-anchor (not Edit) to re-place it. The UI also hides
				// Edit for outdated comments — this is defense in depth.
			} else {
				state.Comments[idx].FromLine = from
				state.Comments[idx].ToLine = to
				state.Comments[idx].Side = state.SelectionSide
				// Re-capture at the (possibly new) range — else a later
				// relocate would drag the edited comment back.
				state.Comments[idx].Anchor = captureAnchor(state.CurrentDiff, from, to, state.SelectionSide)
				state.Comments[idx].AnchorStatus = anchorOK
			}
			rollback = func() { state.Comments[idx] = prev }
		}
	}
	if rollback == nil {
		cm := Comment{
			ID:           newCommentID(),
			File:         state.SelectedFile,
			FromLine:     from,
			ToLine:       to,
			Side:         state.SelectionSide,
			Body:         body,
			Created:      time.Now().UTC(),
			Anchor:       captureAnchor(state.CurrentDiff, from, to, state.SelectionSide),
			AnchorStatus: anchorOK,
		}
		state.Comments = append(state.Comments, cm)
		rollback = func() { state.Comments = state.Comments[:len(state.Comments)-1] }
	}

	if err := c.persist(state.Comments); err != nil {
		// Roll back so memory stays consistent with disk.
		rollback()
		return state, fmt.Errorf("persist comment: %w", err)
	}

	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = ""
	state.DraftBody = ""
	state.EditingCommentID = ""
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	return state, nil
}

// EditComment seeds the composer with an existing comment's body +
// line range so the user can rewrite it. The original comment stays in
// state.Comments — AddComment detects EditingCommentID and updates
// in place rather than appending. This keeps Cancel non-destructive:
// if the user opens Edit and changes their mind, the original survives.
func (c *PrereviewController) EditComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("editComment: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("editComment: id %s not found", id)
	}
	cm := state.Comments[idx]
	state.SelectedFile = cm.File
	// Reload diff for the comment's file in case we're on a different one.
	diff, err := c.loadDiffCached(state.Base, cm.File)
	if err == nil {
		state.CurrentDiff = diff
	}
	state.SelectionAnchor = cm.FromLine
	state.SelectionEnd = cm.ToLine
	state.SelectionSide = cm.Side
	state.DraftBody = cm.Body
	state.EditingCommentID = cm.ID
	state.LastDeletedComment = nil
	// The composer only renders in the diff branch; when Edit is invoked
	// from the all-comments view this drops back into the file so the
	// edit composer is actually visible (no-op when already in the diff).
	state.ShowAllComments = false
	return state, nil
}

// ReanchorComment starts re-placing an outdated comment: it jumps to
// the comment's file and arms ReanchorCommentID, but deliberately does
// NOT pre-seed the (stale) line selection — the user must pick the new
// location. The body is preserved in the composer. The next Save
// (AddComment, ReanchorCommentID branch) re-points the comment and
// re-captures its content anchor. This is the only sanctioned way to
// move an outdated comment; Edit is hidden for outdated comments
// precisely so it can't silently re-anchor against stale content.
func (c *PrereviewController) ReanchorComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("reanchorComment: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("reanchorComment: id %s not found", id)
	}
	cm := state.Comments[idx]
	state.SelectedFile = cm.File
	if diff, err := c.loadDiffCached(state.Base, cm.File); err == nil {
		state.CurrentDiff = diff
	}
	// No pre-seeded selection — the whole point is to choose a new spot.
	state.SelectionAnchor = 0
	state.SelectionEnd = 0
	state.SelectionSide = cm.Side
	state.DraftBody = cm.Body
	state.ReanchorCommentID = cm.ID
	state.EditingCommentID = ""
	state.LastDeletedComment = nil
	state.ShowAllComments = false
	return state, nil
}

// DeleteComment removes the named comment, rewrites the CSV, and stashes
// the deleted comment in state.LastDeletedComment so the user can undo
// for the remainder of the session (or until another mutation).
func (c *PrereviewController) DeleteComment(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("deleteComment: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("deleteComment: id %s not found", id)
	}
	deleted := state.Comments[idx]
	state.Comments = slices.Delete(state.Comments, idx, idx+1)
	if err := c.persist(state.Comments); err != nil {
		return state, fmt.Errorf("persist after delete: %w", err)
	}
	state.LastDeletedComment = &deleted
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// ToggleResolved flips the Resolved flag on the named comment and rewrites
// the CSV. Unlike DeleteComment, this keeps the comment as a historical
// record; the skill should treat resolved comments as "addressed" and
// only act on unresolved ones.
func (c *PrereviewController) ToggleResolved(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	if id == "" {
		return state, fmt.Errorf("toggleResolved: missing id")
	}
	idx := slices.IndexFunc(state.Comments, func(cm Comment) bool { return cm.ID == id })
	if idx < 0 {
		return state, fmt.Errorf("toggleResolved: id %s not found", id)
	}
	state.Comments[idx].Resolved = !state.Comments[idx].Resolved
	if err := c.persist(state.Comments); err != nil {
		// Roll back so disk and memory match.
		state.Comments[idx].Resolved = !state.Comments[idx].Resolved
		return state, fmt.Errorf("persist after toggle resolved: %w", err)
	}
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// UndoDelete restores the most recently deleted comment to state.Comments
// and rewrites the CSV. No-op if LastDeletedComment is nil (the undo
// affordance shouldn't even render in that case, but defending in depth).
func (c *PrereviewController) UndoDelete(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if state.LastDeletedComment == nil {
		return state, nil
	}
	state.Comments = append(state.Comments, *state.LastDeletedComment)
	if err := c.persist(state.Comments); err != nil {
		// Don't clear LastDeletedComment so the user can try again.
		return state, fmt.Errorf("persist after undo: %w", err)
	}
	state.LastDeletedComment = nil
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// HandOff is the skill-mode "I'm finished reviewing" handoff. Writes
// the CSV one more time (defensive — should already be current), then
// writes the DONE marker AFTER the CSV is fsynced + renamed. The skill
// polls for the marker, so writing DONE before the CSV is durable
// would let the skill race and read a half-written file.
//
// Server keeps running afterwards so the user can keep editing.
func (c *PrereviewController) HandOff(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	// The CSV only becomes a contract at handoff. Re-anchor every
	// commented file first so the skill gets accurate line numbers (and
	// an explicit anchor_status=outdated where it cannot be trusted).
	c.relocateAll(&state)
	if err := c.persist(state.Comments); err != nil {
		return state, fmt.Errorf("final csv write: %w", err)
	}
	if err := writeDoneMarker(c.DonePath, c.CSVPath); err != nil {
		return state, fmt.Errorf("write done marker: %w", err)
	}
	state.DoneWritten = true
	state.LastDeletedComment = nil
	state.LastSaved = time.Now().Format("15:04:05")
	return state, nil
}

// Quit gracefully shuts the HTTP server down. The actual shutdown is
// dispatched on a delay so the framework gets to render `Quitting=true`
// back to the client before the WebSocket is torn down — otherwise the
// browser sees a sudden disconnect with no UI feedback.
func (c *PrereviewController) Quit(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.Quitting = true
	if c.ShutdownReq != nil {
		go func() {
			time.Sleep(300 * time.Millisecond)
			select {
			case c.ShutdownReq <- struct{}{}:
			default:
			}
		}()
	}
	return state, nil
}

// persist converts the in-memory comments to CSV Rows and atomically
// rewrites the CSV file.
func (c *PrereviewController) persist(comments []Comment) error {
	if c.CSVWriter == nil {
		return fmt.Errorf("csv writer not configured")
	}
	rows := make([]csv.Row, 0, len(comments))
	for _, cm := range comments {
		rows = append(rows, csv.Row{
			ID:           cm.ID,
			File:         cm.File,
			FromLine:     cm.FromLine,
			ToLine:       cm.ToLine,
			Side:         cm.Side,
			Body:         cm.Body,
			CreatedAt:    cm.Created,
			Resolved:     cm.Resolved,
			Anchor:       cm.Anchor.JSON(),
			AnchorStatus: cm.AnchorStatus,
		})
	}
	return c.CSVWriter.Write(rows)
}

// writeDoneMarker writes csvPath into donePath atomically, so a skill that
// reads donePath gets a complete path string (no truncation race).
func writeDoneMarker(donePath, csvPath string) error {
	dir := filepath.Dir(donePath)
	tmp, err := os.CreateTemp(dir, ".prereview-done-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(csvPath + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, donePath); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

// loadCommentsFromDisk reads the CSV and converts to []Comment. Errors
// are logged and an empty slice returned so a corrupt CSV doesn't break
// the UI — the next write regenerates the file from in-memory state.
func (c *PrereviewController) loadCommentsFromDisk() []Comment {
	rows, err := csv.Read(c.CSVPath)
	if err != nil {
		slog.Warn("loadCommentsFromDisk", "err", err)
		return nil
	}
	out := make([]Comment, 0, len(rows))
	for _, r := range rows {
		out = append(out, Comment{
			ID: r.ID, File: r.File, FromLine: r.FromLine, ToLine: r.ToLine,
			Side: r.Side, Body: r.Body, Created: r.CreatedAt, Resolved: r.Resolved,
			Anchor: parseAnchor(r.Anchor), AnchorStatus: r.AnchorStatus,
		})
	}
	return out
}

// fileInList reports whether path appears among entries.
// uniqueStrings returns s with duplicates removed, preserving first-seen
// order. Used to dedupe base-picker choices (a branch can coincide with
// a HEAD~N preset, or a local and remote branch share a short name).
func uniqueStrings(s []string) []string {
	seen := make(map[string]struct{}, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func fileInList(entries []gitdiff.FileEntry, path string) bool {
	for _, e := range entries {
		if e.Path == path {
			return true
		}
	}
	return false
}

// annotateCommentCounts fills FileEntry.CommentCount from the comments slice.
// Called by Mount each refresh so the left-pane badges stay in sync.
func annotateCommentCounts(files []gitdiff.FileEntry, comments []Comment) []gitdiff.FileEntry {
	counts := map[string]int{}
	for _, c := range comments {
		counts[c.File]++
	}
	for i := range files {
		files[i].CommentCount = counts[files[i].Path]
	}
	return files
}
