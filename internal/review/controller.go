package review

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
)

// PrereviewController holds singleton dependencies. State is cloned per
// session by the framework — never store per-user data on the controller.
type PrereviewController struct {
	// RepoPath, Base, CSVPath are set once by main.go and are read-only.
	// CSVWriter is a goroutine-safe serializer over CSVPath.
	RepoPath string
	Base     string
	CSVPath  string

	// UIPrefsPath is the per-user view-prefs file (see uiprefs.go): the durable
	// home for theme/mode/focus/file-view/raw/show-resolved so they survive a
	// server relaunch, not just a reload. Set once by main.go; "" disables
	// durable prefs (session-only) and is never an error. NOT per-repo — these
	// prefs mean the same thing in every repo.
	UIPrefsPath string

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

	// AgentMode is true under `prereview --agent`: prereview streams the review
	// queue as JSON events (stdout + .prereview/events.jsonl) and the UI shows
	// the Queue (Pause/Resume) + "End session" (which emits the terminating
	// session_end event) instead of "Quit". Set once by main.go.
	AgentMode bool

	// Emitter is the agent-mode JSON event log writer (stdout +
	// .prereview/events.jsonl). Non-nil only in agent mode; the emit path and
	// EndSession guard on nil so non-agent sessions emit nothing.
	Emitter *EventStream

	// Versions is the artifact version store (#90): content-addressed snapshots
	// of the reviewed files as the LLM edits them on disk, enabling view / diff /
	// rollback of uncommitted work. Non-nil only in repo / single-file mode
	// (external mode has no file scope). Every caller nil-guards it, so a failed
	// init just disables versioning rather than breaking the review.
	Versions *VersionStore

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

	// sessionMu guards session. The controller is a singleton shared by all
	// sessions, so OnConnect (any connection) writes it while the llm-status
	// watcher goroutine reads it.
	sessionMu sync.Mutex
	// session is the most recent connected group's Session handle, captured in
	// OnConnect. The watcher uses it to fan llm-status changes out to every open
	// tab. This deliberately bends the "never store per-user data on the
	// controller" rule above: a Session is group-scoped, and #78 targets multi-tab
	// of ONE browser (= one groupID), so a single latest-wins handle is correct
	// for the whole session — the handle re-resolves live connections at
	// TriggerAction time, so within one browser latest-wins is a no-op. The only
	// consequence is multi-*browser* (two groupIDs): the earlier browser stops
	// getting live fan-out until it reconnects — acceptable for a single-user
	// local tool, out of scope for #78. There is no OnDisconnect: the watcher
	// treats ErrSessionDisconnected (no tabs) as a skip, so nothing needs
	// clearing.
	session livetemplate.Session

	// Continuous emission (#119). A single debounced timer coalesces a burst of
	// mutations into one snapshot emit (see controller_emit.go). emitMu guards the
	// timer. inEmit is set while emitSnapshot runs so its own self-heal persist
	// can't reschedule (avoiding a feedback loop). emitDisabled is set at session
	// end so no snapshot fires after session_end (which the skill treats as
	// terminal).
	emitMu       sync.Mutex
	emitTimer    *time.Timer
	inEmit       atomic.Bool
	emitDisabled atomic.Bool
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
	return c.loadDiffFresh(base, path)
}

// effectiveBase returns the base ref to diff against: the state's base (the
// user's picker choice) when set, else the controller's CLI base. The fallback
// is load-bearing for re-anchoring in the emit/hand-off path: that state can
// arrive with an empty Base (a transient snapshot state, or a session that never
// hydrated it), and `git diff ” -- file` fails with "bad revision", which would
// silently skip re-anchoring and leak an edited-away comment/suggestion into the
// snapshot (the real #121). NoGit mode ignores the base entirely.
func (c *PrereviewController) effectiveBase(state *PrereviewState) string {
	if state.Base != "" {
		return state.Base
	}
	return c.Base
}

// loadDiffFresh loads the diff BYPASSING the mtime cache — it always re-reads
// the working-tree file and re-runs git. Used by the re-anchoring done at
// emit/hand-off (relocateAll / relocateSuggestionsAll), where correctness beats
// the cache: two edits within a single filesystem mtime tick would otherwise
// leave loadDiffCached serving a stale diff, so a suggestion/comment whose
// target was just edited away would fail to re-anchor `outdated` and would keep
// leaking into the snapshot (#121). It refreshes the cache so later cached
// reads are correct.
func (c *PrereviewController) loadDiffFresh(base, path string) (*gitdiff.FileDiff, error) {
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
	c.diffCache.Store(base+"\t"+path, cachedDiff{diff: diff, mtime: fileMtime(filepath.Join(c.RepoPath, path))})
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
	base := c.effectiveBase(state)
	seen := map[string]bool{}
	anyChanged := false
	for _, cm := range state.Comments {
		if cm.Resolved || cm.Anchor.Empty() || seen[cm.File] {
			continue
		}
		seen[cm.File] = true
		// Fresh (uncached) load: re-anchoring must see the file's CURRENT content,
		// not a stale mtime-cached diff, or an edited-away comment fails to drift
		// outdated (same class as #121).
		diff, err := c.loadDiffFresh(base, cm.File)
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

	// Refresh the agent's inbound status (.prereview/llm-status.json) on every
	// connect so a newly-opened or reconnecting tab renders current status
	// without waiting for the next watcher tick. Covers repo and external mode
	// (both short-circuit below).
	c.applyLLMStatus(&state)

	// Overlay the durable per-user view prefs (theme/mode/focus/file-view/raw/
	// show-resolved). These are no longer lvt:"persist", so the session store
	// carries them as zero on a reconnect — disk is the single source of truth,
	// loaded here on every connect (like applyLLMStatus). Applied before the
	// external short-circuit so themed external-mode sessions pick up the scheme.
	c.applyUIPrefs(&state)

	// Overlay the agent's per-comment "worked on" markers (.prereview/
	// processed.jsonl) onto the just-loaded comments — before the external
	// short-circuit so region comments get badged too. Cheap by-ID flag flip.
	c.applyProcessed(&state)

	// Load the LLM's proposed edits (.prereview/suggestions.jsonl, #98) so they
	// render inline as suggestion boxes. Their drift anchors are derived from the
	// original text here; actual re-location against the selected file's live diff
	// happens after the diff loads (relocateSuggestionsSelected below).
	c.applySuggestions(&state)

	// Load the reviewer's recorded decisions on those suggestions (#98 Phase 2)
	// from the server-owned .prereview/suggestion-decisions.jsonl. Overlaid onto
	// suggestions (by ID + content fingerprint) at render time in
	// DecisionsBySuggestion, so a revised suggestion drops its stale verdict.
	c.applyDecisions(&state)

	// Load the reviewer's hidden-from-view set (server-owned
	// .prereview/hidden-suggestions.jsonl). Filtered out of every render surface
	// in visibleSuggestions, fingerprint-gated so a revised suggestion un-hides.
	c.applyHidden(&state)

	// AgentMode is mirror-only: refresh from the controller every connect so a
	// binary launched with --agent renders the right button even after a
	// session-storage reconnect. Mirrored BEFORE the external short-circuit
	// below, so the "End session" button renders in both repo and external
	// agent sessions.
	state.AgentMode = c.AgentMode

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
	// Same for the selected file's suggestions (#98): a suggestion whose target
	// text was edited away renders `outdated`; one whose text moved follows it.
	c.relocateSuggestionsSelected(&state)
	// Artifact versioning (#90): populate the selected file's version timeline
	// and the paused state. Mount always shows the LIVE diff (ViewingVersion
	// defaults false and isn't persisted), so a reconnect naturally leaves any
	// historical view and lands back on current.
	c.applyVersionList(&state)
	c.applyPaused(&state)
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
			Hidden:       cm.Hidden,
			Draft:        cm.Draft,
		})
	}
	if err := c.CSVWriter.Write(rows); err != nil {
		return err
	}
	// Continuous enqueue (#119): a persisted comment change (re)arms the debounced
	// snapshot emit. inEmit-guarded, so the self-heal persist inside emitSnapshot
	// doesn't re-arm and spin; no-op in non-stream mode.
	c.scheduleEmit()
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
	return commentsFromRows(rows)
}

// commentsFromRows is the shared CSV-row → Comment projection used by both the
// live server (loadCommentsFromDisk) and the `prereview comments` subcommand
// (LoadComments), so the two never drift on how a row maps to a comment.
func commentsFromRows(rows []csv.Row) []Comment {
	out := make([]Comment, 0, len(rows))
	for _, r := range rows {
		out = append(out, Comment{
			ID: r.ID, File: r.File, FromLine: r.FromLine, ToLine: r.ToLine,
			Side: r.Side, Body: r.Body, Created: r.CreatedAt, Resolved: r.Resolved,
			Anchor: parseAnchor(r.Anchor), AnchorStatus: r.AnchorStatus,
			Kind: r.Kind, Area: parseArea(r.Area), URL: r.URL,
			FromCol: r.FromCol, ToCol: r.ToCol, Hidden: r.Hidden, Draft: r.Draft,
		})
	}
	return out
}

// LoadComments reads the review's comments.csv at csvPath and returns the
// comments as StreamComment — the SAME JSON shape the live handoff snapshot
// emits, so the `prereview comments` reader and the stream never diverge. With
// all=false it returns only the actionable set (unresolved, non-outdated,
// non-draft — what the agent should act on); with all=true, every comment. The
// returned slice is always non-nil (JSON-encodes as `[]`, never `null`).
func LoadComments(csvPath string, all bool) ([]StreamComment, error) {
	rows, err := csv.Read(csvPath)
	if err != nil {
		return nil, err
	}
	comments := commentsFromRows(rows)
	if !all {
		return actionableComments(comments), nil
	}
	out := make([]StreamComment, 0, len(comments))
	for _, cm := range comments {
		out = append(out, toStreamComment(cm))
	}
	return out, nil
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
