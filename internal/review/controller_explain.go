package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/livetemplate/prereview/gitdiff"
)

// changedIndexCache holds the last changed-line index and the key it was built
// for. Building one runs `git diff -U0` over the repo (~150ms on a 165-file
// change), so only a change to the key rebuilds it.
type changedIndexCache struct {
	mu  sync.Mutex
	key string
	idx *gitdiff.ChangedIndex
}

// changedFiles is the review's changed files: what the index covers. It is the
// data, not the drawer, so the "show all files" toggle doesn't change it.
func (s PrereviewState) changedFiles() []gitdiff.FileEntry {
	var out []gitdiff.FileEntry
	for _, f := range s.Files {
		if f.Status != "" && s.inScope(f.Path) {
			out = append(out, f)
		}
	}
	return out
}

// changedIndex returns the index of the changed files, rebuilt only when the
// base's commit or a changed file's size or mtime moved.
func (c *PrereviewController) changedIndex(state *PrereviewState) *gitdiff.ChangedIndex {
	files := state.changedFiles()
	base := c.effectiveBase(state)
	key := c.changedIndexKey(base, files)

	c.changedIdx.mu.Lock()
	defer c.changedIdx.mu.Unlock()
	if c.changedIdx.idx != nil && c.changedIdx.key == key {
		return c.changedIdx.idx
	}
	var idx *gitdiff.ChangedIndex
	if c.NoGit {
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = f.Path
		}
		idx = gitdiff.LoadChangedIndexNoGit(c.RepoPath, paths)
	} else {
		var err error
		if idx, err = gitdiff.LoadChangedIndex(c.RepoPath, base, files); err != nil {
			slog.Warn("explain: changed index", "base", base, "err", err)
			return nil
		}
	}
	c.changedIdx.key, c.changedIdx.idx = key, idx
	return idx
}

func (c *PrereviewController) changedIndexKey(base string, files []gitdiff.FileEntry) string {
	h := sha256.New()
	if !c.NoGit {
		fmt.Fprintf(h, "%s\x00%s\x00", base, gitdiff.ResolveRef(c.RepoPath, base))
	}
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", f.Path, f.Status, statusFingerprint(filepath.Join(c.RepoPath, f.Path)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// applyExplain loads the agent's explanations into state, grounds every anchor
// against the live index, and marks the ones written before the latest edit.
// Called from Mount (which RefreshDiff reuses) and, when explain.jsonl moved,
// from the watcher reload.
func (c *PrereviewController) applyExplain(state *PrereviewState) {
	state.ExplainSeen = statusFingerprint(ExplainPath(c.CSVPath))
	state.Explanations = LoadExplanations(c.CSVPath)
	if len(state.Explanations) == 0 {
		return
	}
	idx := c.changedIndex(state)
	if idx == nil {
		return
	}
	keys := make([]string, len(state.Explanations))
	for i, e := range state.Explanations {
		keys[i] = e.seenKey()
	}
	seen := c.stampFirstSeen(keys, idx.Fingerprint)

	listed := map[string]bool{}
	for _, f := range state.Files {
		if state.inScope(f.Path) {
			listed[f.Path] = true
		}
	}
	unchanged := map[string]*gitdiff.FileChanges{} // read each unchanged file once
	lookup := func(path string) *gitdiff.FileChanges {
		if fc := idx.Files[path]; fc != nil {
			return fc
		}
		if !listed[path] {
			return nil
		}
		if _, ok := unchanged[path]; !ok {
			unchanged[path] = gitdiff.UnchangedFile(c.RepoPath, path)
		}
		return unchanged[path]
	}
	for i := range state.Explanations {
		e := &state.Explanations[i]
		e.Stale = seen[e.seenKey()] != idx.Fingerprint
		for j := range e.Steps {
			for k := range e.Steps[j].Anchors {
				a := &e.Steps[j].Anchors[k]
				a.Status = groundRange(lookup(a.File), a.Kind, a.Side, a.FromLine, a.ToLine)
			}
		}
	}
}

// groundRange reports whether a file entry has what an anchor cites: the whole
// file, or both ends of a line range on one side. A nil entry (a path outside the
// review) is never grounded. It takes plain fields so the A′ whole-change quiz
// can ground its questions the same way.
func groundRange(fc *gitdiff.FileChanges, kind, side string, from, to int) string {
	if fc != nil && (kind == AnchorKindFile || (fc.HasLine(side, from) && fc.HasLine(side, to))) {
		return anchorOK
	}
	return anchorUngrounded
}
