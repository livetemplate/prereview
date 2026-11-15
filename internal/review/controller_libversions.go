package review

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"github.com/livetemplate/livetemplate"

	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/bench"
	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/skills"
	"github.com/livetemplate/prereview/internal/library"
)

// Library versions (checks M3): every edit to a rule file in your checks folder
// is checkpointed into the library's own store — <library>/.prereview, the same
// store `prereview <library>` (Review in prereview) uses — so the Checks tab shows
// a rule's history with diffs and Restore, and the library review shows the same
// timeline. Every running server watches the folder and checkpoints into it; the
// store's cross-process lock and SHA dedupe make that one version per edit.

// LibraryStoreDir is the library's review store.
func LibraryStoreDir(libraryDir string) string { return filepath.Join(libraryDir, library.StoreDir) }

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
// ("checks/filler.yml"), the same key the library review uses for it. "" for a
// built-in (no file) or a checks folder moved outside the library.
func (c *PrereviewController) libraryKey(rulePath string) string {
	if rulePath == "" || c.LibraryDir == "" {
		return ""
	}
	rel, err := filepath.Rel(c.LibraryDir, rulePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// checkpointLibrary records the library's versioned files as they are now — your
// rule files and the generated skills' sources — with changelog (a no-op when
// nothing changed since the last version).
func (c *PrereviewController) checkpointLibrary(trigger, changelog string) {
	vs := c.libVersionStore()
	if vs == nil {
		return
	}
	refs := c.libraryScope()
	if len(refs) == 0 {
		return
	}
	if cps, err := vs.Checkpoints(); err == nil && len(cps) == 0 {
		trigger = VersionTriggerBaseline
	}
	if _, _, err := vs.Checkpoint(refs, trigger, changelog); err != nil {
		slog.Warn("library checkpoint", "err", err)
	}
}

// libraryScope is every file the library versions: your rule files (the loaded
// user rules) and each generated skill's source (skills/<id>/SKILL.md).
func (c *PrereviewController) libraryScope() []FileRef {
	r := c.runner()
	r.mu.Lock()
	var refs []FileRef
	for _, rule := range r.lib.Rules {
		if k := c.libraryKey(rule.Path); k != "" && rule.Source == checks.SourceUser {
			refs = append(refs, FileRef{Path: k, AbsPath: rule.Path})
		}
	}
	r.mu.Unlock()
	srcs, _ := filepath.Glob(skills.Env{LibDir: c.LibraryDir}.SourcePath("*"))
	defs, _ := filepath.Glob(filepath.Join(bench.Dir(c.LibraryDir), "*", bench.DefFile))
	srcs = append(srcs, defs...)
	for _, p := range srcs {
		if k := c.libraryKey(p); k != "" {
			refs = append(refs, FileRef{Path: k, AbsPath: p})
		}
	}
	return refs
}

// HistoryView is a library file's versions as the history partial shows them:
// Restore posts Action with ID and the version's seq.
type HistoryView struct {
	ID, Action string
	Versions   []RuleVersion
}

// RuleVersion is one point in a library file's history (a rule or a skill).
type RuleVersion struct {
	Seq     int
	When    string
	Note    string // the version's changelog ("Accepted “…”"), when it has one
	Current bool
	Stat    string        // "+2 −1" against the version before it
	Diff    []RuleDiffRow // its added and removed lines
	More    int           // changed lines not listed
}

// RuleDiffRow is one changed line of a version.
type RuleDiffRow struct {
	Kind, Text string // add | del
}

// maxRuleDiffRows bounds a version's listed lines; maxRuleVersions the versions
// listed (newest first).
const (
	maxRuleDiffRows = 30
	maxRuleVersions = 20
)

// applyRuleHistory loads the expanded rule's history while the Checks tab shows
// it (newest first). The versions are built once per timeline change and shared
// by every tab (ruleHistory); reapplying checks reruns this on every broadcast.
func (c *PrereviewController) applyRuleHistory(state *PrereviewState) {
	state.CheckHistory = nil
	if !state.ShowLibrary || !state.LibraryOnChecks() || state.ExpandedCheck == "" {
		return
	}
	if key := c.libraryKey(c.rulePath(state.ExpandedCheck)); key != "" {
		state.CheckHistory = c.ruleHistory(key)
	}
}

// ruleHistory is key's versions, cached by the library timeline's fingerprint.
func (c *PrereviewController) ruleHistory(key string) []RuleVersion {
	vs := c.libVersionStore()
	if vs == nil {
		return nil
	}
	fp := statusFingerprint(vs.timelinePath())
	c.libHistMu.Lock()
	defer c.libHistMu.Unlock()
	if c.libHistFP != fp {
		c.libHist, c.libHistFP = map[string][]RuleVersion{}, fp
	}
	if h, ok := c.libHist[key]; ok {
		return h
	}
	hist, err := vs.FileHistory(key)
	if err != nil {
		return nil
	}
	first := max(0, len(hist)-maxRuleVersions-1) // one older version to diff the oldest listed against
	var prev []byte
	var out []RuleVersion
	for i := first; i < len(hist); i++ {
		h := hist[i]
		var cur []byte
		if h.SHA != "" { // "" is a tombstone: the file was deleted then
			cur, _ = vs.Blob(h.SHA)
		}
		if i > first || first == 0 {
			v := RuleVersion{Seq: h.Seq, When: h.TS.Local().Format(timeLayout), Note: h.Changelog, Current: i == len(hist)-1}
			if i > 0 {
				v.Stat, v.Diff, v.More = ruleDiff(key, prev, cur)
				if h.SHA == "" {
					v.Stat = "deleted"
				}
			}
			out = append(out, v)
		}
		prev = cur
	}
	slices.Reverse(out)
	c.libHist[key] = out
	return out
}

// ruleDiff is one version's change: "+a −d", its changed lines (bounded) and how
// many weren't listed.
func ruleDiff(key string, prev, cur []byte) (stat string, rows []RuleDiffRow, more int) {
	var add, del int
	for _, l := range gitdiff.DiffContentsNoHighlight(key, prev, cur).Lines {
		switch l.Kind {
		case "add":
			add++
		case "del":
			del++
		default:
			continue
		}
		if len(rows) < maxRuleDiffRows {
			rows = append(rows, RuleDiffRow{Kind: l.Kind, Text: l.Content})
		} else {
			more++
		}
	}
	return fmt.Sprintf("+%d −%d", add, del), rows, more
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
	if err := writeFileAtomicVersions(path, data); err != nil {
		return state, fmt.Errorf("restore: %w", err)
	}
	if c.reloadChecksIfChanged() {
		c.checkpointLibrary(VersionTriggerRollback, "")
		go c.broadcast("ChecksUpdated") // other tabs; the watcher won't see it again
	}
	c.reapplyChecks(&state)
	state.Flash = "Restored " + filepath.Base(path)
	return state, nil
}
