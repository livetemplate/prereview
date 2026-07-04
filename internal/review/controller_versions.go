package review

// controller_versions.go wires the pure VersionStore (versions.go) into the
// controller: it computes the snapshot SCOPE from the live file list and decides
// WHEN to checkpoint. The store itself knows nothing about git or the controller.
// UI actions (ViewVersion / DiffVersions / RestoreVersion) are added in later
// phases; this file carries the Phase-1.1 wiring only.

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/gitdiff"
)

// PausedMarkerName is the .prereview/ marker that records "the agent is paused".
// Introduced by rollback (#90 Phase 1.3): a restore force-pauses so the agent
// stops applying while the reviewer re-steers, preventing desync with the LLM's
// own edit stack. M2's continuous-drain reads this to gate emission; in M1 it
// drives the paused banner. Reset on launch by openStore, like the other markers.
const PausedMarkerName = "paused"

// VersionListItem is one row in the per-file Versions panel — a presentation
// view over the store's FileHistory, newest first.
type VersionListItem struct {
	Seq     int
	Label   string // human trigger label: "Original" / "Agent edit" / "Rolled back"
	When    string // local time-of-day the version was captured
	Current bool   // newest recorded version (≈ what's on disk now)
	Viewing bool   // currently open in the read-only version view
	Deleted bool   // tombstone: the file was absent at this version
}

// versionLabel maps a store trigger to a human label for the timeline.
func versionLabel(trigger string) string {
	switch trigger {
	case VersionTriggerBaseline:
		return "Original"
	case VersionTriggerLLMDone:
		return "Agent edit"
	case VersionTriggerRollback:
		return "Rolled back"
	default:
		return "Version"
	}
}

// versionScope returns the files to snapshot: the diff's CHANGED-file set (git
// mode) or every file (no-git, where all files read as added), as FileRefs keyed
// by repo-relative path. Snapshotting only the changed set — not the whole
// tracked tree — bounds each checkpoint's read+hash cost to the size of the
// review, and it's exactly the set the LLM edits. `gitdiff.ListFiles` returns
// unchanged tracked files with Status=="" (a plain, un-badged row); those are
// skipped here. A file the LLM newly touches gains a non-empty status and so
// enters scope on the next checkpoint.
//
// It also filters .prereview/ so the version store never snapshots its own blobs
// (git mode lists it as untracked unless the user gitignored it; ListFilesNoGit
// already skips dotdirs). Git mode scopes against c.Base (the CLI base), not a
// UI-changed base — the set stays stable across UI base changes, an accepted
// trade-off. External mode and a nil store yield no scope.
func (c *PrereviewController) versionScope() []FileRef {
	if c.ExternalMode || c.Versions == nil {
		return nil
	}
	var files []gitdiff.FileEntry
	if c.NoGit {
		files, _ = gitdiff.ListFilesNoGit(c.RepoPath, c.SingleFile)
	} else {
		files, _ = gitdiff.ListFiles(c.RepoPath, c.Base)
	}
	refs := make([]FileRef, 0, len(files))
	for _, f := range files {
		if f.Path == "" || f.Status == "" || strings.HasPrefix(f.Path, ".prereview/") {
			continue
		}
		refs = append(refs, FileRef{Path: f.Path, AbsPath: filepath.Join(c.RepoPath, f.Path)})
	}
	return refs
}

// CheckpointBaseline records the v0 snapshot of the review scope. Exported for
// the launch wiring (server.go) to call once at startup, before the agent edits
// anything. Idempotent-ish: a second call with an unchanged scope is skipped by
// the store, so it never duplicates the baseline.
func (c *PrereviewController) CheckpointBaseline() {
	c.checkpointVersions(VersionTriggerBaseline)
}

// checkpointVersions snapshots the current scope under trigger. It is a safety
// net, never on the critical path: a failure is logged and swallowed so a
// versioning hiccup never breaks a review. No-op when the store is absent.
func (c *PrereviewController) checkpointVersions(trigger string) {
	if c.Versions == nil {
		return
	}
	if _, _, err := c.Versions.Checkpoint(c.versionScope(), trigger); err != nil {
		slog.Warn("version checkpoint failed", "trigger", trigger, "err", err)
	}
}

// currentLLMState reads the agent's current status state ("working"/"done"/""),
// treating a missing or unreadable file as idle (""). Used by the watcher to
// detect the working→done transition that triggers an llm-done checkpoint.
func (c *PrereviewController) currentLLMState() string {
	s, err := readLLMStatus(c.statusPath())
	if err != nil {
		return ""
	}
	return s.State
}

// applyVersionList populates state.Versions with the selected file's timeline
// (newest first) for the Versions panel. Cheap by-path read of the store's
// history; called from Mount and every version action so the panel stays live.
func (c *PrereviewController) applyVersionList(state *PrereviewState) {
	state.Versions = nil
	if c.Versions == nil || state.SelectedFile == "" {
		return
	}
	hist, err := c.Versions.FileHistory(state.SelectedFile)
	if err != nil {
		slog.Warn("load file version history", "file", state.SelectedFile, "err", err)
		return
	}
	items := make([]VersionListItem, 0, len(hist))
	for i := len(hist) - 1; i >= 0; i-- { // newest first
		h := hist[i]
		items = append(items, VersionListItem{
			Seq:     h.Seq,
			Label:   versionLabel(h.Trigger),
			When:    h.TS.Local().Format("15:04:05"),
			Current: i == len(hist)-1,
			Viewing: state.ViewingVersion && state.VersionViewSeq == h.Seq,
			Deleted: h.SHA == "",
		})
	}
	state.Versions = items
}

// reloadLiveDiff rebuilds the live (base..working) diff for the selected file and
// clears version-view mode. The single path back to "current" from a historical
// view, shared by ExitVersionView and RestoreVersion. loadDiffCached invalidates
// on mtime, so a just-restored file reloads fresh.
func (c *PrereviewController) reloadLiveDiff(state *PrereviewState) {
	state.ViewingVersion = false
	state.VersionViewSeq = 0
	state.VersionViewDiff = false
	if state.SelectedFile == "" {
		state.CurrentDiff = nil
		c.applyVersionList(state)
		return
	}
	diff, err := c.loadDiffCached(state.Base, state.SelectedFile)
	if err != nil {
		slog.Warn("reload live diff", "file", state.SelectedFile, "err", err)
		state.CurrentDiff = nil
	} else {
		state.CurrentDiff = diff
		c.relocateSelected(state)
	}
	c.applyVersionList(state)
}

// ViewVersion opens a historical version of the selected file read-only in the
// right pane (the store hands back the exact bytes; RenderBytesAsFile turns them
// into the same *FileDiff the live pane renders). It's a view MODE, not a file
// write — nothing on disk changes. Selection/compose affordances are cleared
// because historical line coords don't match disk (see the template's
// !ViewingVersion gate).
func (c *PrereviewController) ViewVersion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.Versions == nil || state.SelectedFile == "" {
		return state, nil
	}
	seq := ctx.GetInt("seq")
	data, err := c.Versions.Restore(state.SelectedFile, seq)
	switch {
	case errors.Is(err, ErrVersionTombstone):
		state.CurrentDiff = &gitdiff.FileDiff{Path: state.SelectedFile, Note: "file did not exist at this version"}
	case err != nil:
		return state, fmt.Errorf("view version %d: %w", seq, err)
	default:
		state.CurrentDiff = gitdiff.RenderBytesAsFile(state.SelectedFile, data)
	}
	state.ViewingVersion = true
	state.VersionViewDiff = false
	state.VersionViewSeq = seq
	state.SelectionAnchor, state.SelectionEnd = 0, 0
	state.CommentMode = ""
	c.applyVersionList(&state)
	return state, nil
}

// DiffVersion shows a prior version diffed against the CURRENT working-tree file
// ("Diff vs current") — a read-only view mode like ViewVersion, but the pane
// renders the line diff (via gitdiff.DiffContents, a pure-Go Myers diff over the
// two blobs — no git). A tombstone (the file didn't exist at that version) diffs
// as an all-addition against nothing. No disk change.
func (c *PrereviewController) DiffVersion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.Versions == nil || state.SelectedFile == "" {
		return state, nil
	}
	seq := ctx.GetInt("seq")
	oldData, err := c.Versions.Restore(state.SelectedFile, seq)
	if err != nil && !errors.Is(err, ErrVersionTombstone) {
		return state, fmt.Errorf("diff version %d: %w", seq, err)
	}
	// Tombstone ⇒ oldData nil ⇒ everything in current reads as an addition.
	curData, _ := os.ReadFile(filepath.Join(c.RepoPath, state.SelectedFile))
	state.CurrentDiff = gitdiff.DiffContents(state.SelectedFile, oldData, curData)
	state.ViewingVersion = true
	state.VersionViewDiff = true
	state.VersionViewSeq = seq
	state.SelectionAnchor, state.SelectionEnd = 0, 0
	state.CommentMode = ""
	c.applyVersionList(&state)
	return state, nil
}

// ExitVersionView leaves the read-only historical view and returns to the live
// diff. No disk change.
func (c *PrereviewController) ExitVersionView(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.reloadLiveDiff(&state)
	return state, nil
}

// RestoreVersion rolls the selected file back to a prior version — the payoff of
// the whole milestone. It performs the anti-desync sequence in order: (1)
// force-pause the agent so it stops applying while the reviewer re-steers (this
// is what prevents desync with the LLM's own /rewind checkpoint stack), (2) write
// the version's bytes to disk (or delete the file for a tombstone), (3) record a
// new rollback checkpoint so history stays append-only (never rewritten), and
// (4) return to the live diff + nudge a refresh. The rollback is itself a new
// version, so it too is undoable.
func (c *PrereviewController) RestoreVersion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if c.Versions == nil || state.SelectedFile == "" {
		return state, nil
	}
	seq := ctx.GetInt("seq")
	data, err := c.Versions.Restore(state.SelectedFile, seq)
	if err != nil && !errors.Is(err, ErrVersionTombstone) {
		return state, fmt.Errorf("restore version %d: %w", seq, err)
	}

	// (1) Force-pause BEFORE touching disk.
	c.setAgentPaused(true)
	state.AgentPaused = true

	// (2) Apply to disk: write the restored bytes, or delete for a tombstone.
	full := filepath.Join(c.RepoPath, state.SelectedFile)
	if errors.Is(err, ErrVersionTombstone) {
		if rerr := os.Remove(full); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return state, fmt.Errorf("remove restored file: %w", rerr)
		}
	} else if werr := os.WriteFile(full, data, 0o644); werr != nil {
		return state, fmt.Errorf("write restored file: %w", werr)
	}

	// (3) Record the rollback as a new, append-only version.
	c.checkpointVersions(VersionTriggerRollback)

	// (4) Back to the live diff (now showing the restored content) + refresh nudge.
	c.reloadLiveDiff(&state)
	state.PendingRefresh = true
	return state, nil
}

// ResumeAgent clears the rollback-induced pause. In M1 there is no continuous
// drain to release, so this just removes the marker + banner; M2's emitter also
// re-emits the latest snapshot on resume.
func (c *PrereviewController) ResumeAgent(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.setAgentPaused(false)
	state.AgentPaused = false
	return state, nil
}

// pausedMarkerPath is the .prereview/paused marker for this session's store.
func (c *PrereviewController) pausedMarkerPath() string {
	return filepath.Join(filepath.Dir(c.CSVPath), PausedMarkerName)
}

// applyPaused refreshes state.AgentPaused from the marker file (present ⇒ paused).
func (c *PrereviewController) applyPaused(state *PrereviewState) {
	_, err := os.Stat(c.pausedMarkerPath())
	state.AgentPaused = err == nil
}

// setAgentPaused writes or removes the paused marker.
func (c *PrereviewController) setAgentPaused(paused bool) {
	if paused {
		if err := os.WriteFile(c.pausedMarkerPath(), []byte("1\n"), 0o644); err != nil {
			slog.Warn("write paused marker", "err", err)
		}
		return
	}
	if err := os.Remove(c.pausedMarkerPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("remove paused marker", "err", err)
	}
}
