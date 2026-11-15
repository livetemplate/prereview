package review

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/livetemplate"

	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/checks"
)

// Library versions (checks M3): every edit to a rule file in your checks folder
// is checkpointed into the library's own store — <library>/.prereview, the same
// store `prereview <library>` (Review in prereview) uses — so the Checks tab shows
// a rule's history with diffs and Restore, and the library review shows the same
// timeline. Every running server watches the folder and checkpoints into it; the
// store's cross-process lock and SHA dedupe make that one version per edit.

// LibraryStoreDir is the library's review store.
func LibraryStoreDir(libraryDir string) string { return filepath.Join(libraryDir, ".prereview") }

// libVersionStore opens the library's version store once; nil when there's no
// library or it can't be opened (versions are a safety net, never a blocker).
func (c *PrereviewController) libVersionStore() *VersionStore {
	c.libVersionsOnce.Do(func() {
		if c.LibraryDir == "" || c.ChecksDir == "" {
			return
		}
		vs, err := NewVersionStore(filepath.Join(LibraryStoreDir(c.LibraryDir), "versions"))
		if err != nil {
			slog.Warn("library version store", "err", err)
			return
		}
		c.libVersions = vs
	})
	return c.libVersions
}

// libraryKey is a rule file's timeline key: its path in the library folder
// ("checks/filler.yml"), so the library review sees the same file. "" for a file
// outside the checks folder (a built-in has no file).
func (c *PrereviewController) libraryKey(rulePath string) string {
	if rulePath == "" || c.ChecksDir == "" {
		return ""
	}
	rel, err := filepath.Rel(c.ChecksDir, rulePath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return "checks/" + filepath.ToSlash(rel)
}

// checkpointLibrary records the checks folder's rule files as they are now (a
// no-op when nothing changed since the last version).
func (c *PrereviewController) checkpointLibrary(trigger string) {
	vs := c.libVersionStore()
	if vs == nil {
		return
	}
	r := c.runner()
	r.mu.Lock()
	var refs []FileRef
	for _, rule := range r.lib.Rules {
		if k := c.libraryKey(rule.Path); k != "" && rule.Source == checks.SourceUser {
			refs = append(refs, FileRef{Path: k, AbsPath: rule.Path})
		}
	}
	r.mu.Unlock()
	if len(refs) == 0 {
		return
	}
	if cps, err := vs.Checkpoints(); err == nil && len(cps) == 0 {
		trigger = VersionTriggerBaseline
	}
	if _, _, err := vs.Checkpoint(refs, trigger, ""); err != nil {
		slog.Warn("library checkpoint", "err", err)
	}
}

// RuleVersion is one point in a rule file's history on the Checks tab.
type RuleVersion struct {
	Seq     int
	When    string
	Current bool
	Stat    string        // "+2 −1" against the version before it
	Diff    []RuleDiffRow // its added and removed lines
	More    int           // changed lines not listed
}

// RuleDiffRow is one changed line of a version.
type RuleDiffRow struct {
	Kind, Text string // add | del
}

// maxRuleDiffRows bounds a version's listed lines.
const maxRuleDiffRows = 30

// applyRuleHistory loads the expanded rule's history while the Checks tab shows
// it (newest first).
func (c *PrereviewController) applyRuleHistory(state *PrereviewState) {
	state.CheckHistory = nil
	if !state.ShowLibrary || !state.LibraryOnChecks() || state.ExpandedCheck == "" {
		return
	}
	vs := c.libVersionStore()
	key := c.libraryKey(c.rulePath(state.ExpandedCheck))
	if vs == nil || key == "" {
		return
	}
	hist, err := vs.FileHistory(key)
	if err != nil || len(hist) == 0 {
		return
	}
	var prev []byte
	out := make([]RuleVersion, 0, len(hist))
	for i, h := range hist {
		cur, _ := vs.Blob(h.SHA)
		v := RuleVersion{Seq: h.Seq, When: h.TS.Local().Format("2 Jan 2006 15:04"), Current: i == len(hist)-1}
		if i > 0 {
			var add, del int
			for _, l := range gitdiff.DiffContents(key, prev, cur).Lines {
				if l.Kind != "add" && l.Kind != "del" {
					continue
				}
				if l.Kind == "add" {
					add++
				} else {
					del++
				}
				if len(v.Diff) < maxRuleDiffRows {
					v.Diff = append(v.Diff, RuleDiffRow{Kind: l.Kind, Text: l.Content})
				} else {
					v.More++
				}
			}
			v.Stat = fmt.Sprintf("+%d −%d", add, del)
		}
		out = append(out, v)
		prev = cur
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	state.CheckHistory = out
}

// rulePath is a loaded rule's file ("" for a built-in or an unknown id).
func (c *PrereviewController) rulePath(id string) string {
	r := c.runner()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rule := range r.lib.Rules {
		if rule.ID == id {
			return rule.Path
		}
	}
	return ""
}

// RestoreRuleVersion writes a rule file back to an earlier version (itself a new
// version, so the restore can be undone the same way) and reloads the checks.
func (c *PrereviewController) RestoreRuleVersion(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	vs := c.libVersionStore()
	path := c.rulePath(ctx.GetString("id"))
	key := c.libraryKey(path)
	if vs == nil || key == "" {
		return state, errors.New("restore: no history for that check")
	}
	data, err := vs.Restore(key, ctx.GetInt("seq"))
	if err != nil {
		return state, fmt.Errorf("restore: %w", err)
	}
	if err := writeFileAtomic(path, data); err != nil {
		return state, fmt.Errorf("restore: %w", err)
	}
	if c.reloadChecksIfChanged() {
		c.checkpointLibrary(VersionTriggerRollback)
		go c.broadcast("ChecksUpdated") // other tabs; the watcher won't see it again
	}
	c.reapplyChecks(&state)
	state.Flash = "Restored " + filepath.Base(path)
	return state, nil
}

// writeFileAtomic replaces path's content via a temp file + rename beside it.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		_ = tmp.Chmod(fi.Mode().Perm()) // keep the file's mode (CreateTemp makes it 0600)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
