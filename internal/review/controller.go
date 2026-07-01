package review

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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

	// NoGit is true when the path was a single file or a directory with no
	// .git: the file list and per-file diff are synthesized from the
	// filesystem (gitdiff.ListFilesNoGit / LoadDiffNoGit) instead of git,
	// the base picker is suppressed, and Base is unused. SingleFile, when
	// non-empty (single-file mode), is the only reviewable file —
	// RepoPath is its parent directory.
	NoGit      bool
	SingleFile string

	// ExternalMode is true under `prereview --external <url>`: the session
	// fronts a live local website through the reverse proxy instead of a
	// repo. ProxyBaseURL is the second-origin URL the UI iframes; TargetURL
	// is the upstream shown to the user. Set once by main.go.
	ExternalMode bool
	ProxyBaseURL string
	TargetURL    string

	// Version is the build version (main.version; "dev" for source
	// builds) surfaced into state for the footer.
	Version   string
	CSVWriter *csv.Writer

	// SkillMode is true when prereview is launched via `--skill` (the
	// Claude skill sets this). It selects the top-bar button label:
	// "Hand off → Claude" vs "Quit". --stream implies SkillMode.
	SkillMode bool

	// StreamMode is true under `prereview --stream`: each Hand off emits a
	// JSON handoff event and the UI offers an "End session" button that emits
	// the terminating session_end event. Set once by main.go.
	StreamMode bool

	// Emitter is the stream-mode JSON event log writer (stdout +
	// .prereview/events.jsonl). Non-nil only in stream mode; HandOff /
	// EndSession guard on nil so non-stream sessions emit nothing.
	Emitter *EventStream

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
	// StreamMode mirrors the same way and BEFORE the external short-circuit
	// below, so the "End session" button renders in both repo and external
	// stream sessions.
	state.StreamMode = c.StreamMode

	// External (proxy) mode short-circuits the entire git/file-list path:
	// there is no repo to diff. Mirror the proxy identity from the controller
	// (source of truth) and return — the template renders the framed live
	// site + URL-grouped annotations instead of a file tree.
	if c.ExternalMode {
		state.ExternalMode = true
		state.ProxyBaseURL = c.ProxyBaseURL
		state.TargetURL = c.TargetURL
		state.Version = c.Version
		state.CSVPath = c.CSVPath
		return state, nil
	}

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
			Kind:         cm.Kind,
			Area:         cm.Area.JSON(),
			URL:          cm.URL,
			FromCol:      cm.FromCol,
			ToCol:        cm.ToCol,
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
			Kind: r.Kind, Area: parseArea(r.Area), URL: r.URL,
			FromCol: r.FromCol, ToCol: r.ToCol,
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
