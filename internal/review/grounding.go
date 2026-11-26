package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/livetemplate/prereview/gitdiff"
)

// grounding.go checks that what the agent cites exists: walkthrough anchors
// (explain.go) and quiz questions (quiz.go) against one index of the change, and
// impact anchors against the working tree. A pass reads each file once.

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

// fileRows is a working-tree file's lines, ok=false when it can't be cited by line.
type fileRows struct {
	rows []string
	ok   bool
}

// citable resolves the files an anchor may cite, for one grounding pass: a
// changed file from the change index, a listed unchanged one from disk, read once
// (walkthrough anchors, quiz questions), and any in-repo file for impact (rows).
type citable struct {
	repo   string
	idx    *gitdiff.ChangedIndex
	listed map[string]bool
	read   map[string]fileRows
}

func (c *PrereviewController) citableFiles(state *PrereviewState, idx *gitdiff.ChangedIndex) *citable {
	listed := map[string]bool{}
	for _, f := range state.Files {
		if state.inScope(f.Path) {
			listed[f.Path] = true
		}
	}
	return &citable{repo: c.RepoPath, idx: idx, listed: listed, read: map[string]fileRows{}}
}

// rows is path's working-tree lines, read once.
func (f *citable) rows(path string) fileRows {
	r, done := f.read[path]
	if !done {
		r.rows, r.ok = gitdiff.FileLines(f.repo, path)
		f.read[path] = r
	}
	return r
}

// changes is what an anchor in path can cite: the file's entry in the index, or a
// listed unchanged file's line count; nil for a path outside the review.
func (f *citable) changes(path string) *gitdiff.FileChanges {
	if fc := f.idx.Files[path]; fc != nil {
		return fc
	}
	if !f.listed[path] {
		return nil
	}
	r := f.rows(path)
	if !r.ok {
		return &gitdiff.FileChanges{Opaque: true}
	}
	return &gitdiff.FileChanges{OldLen: len(r.rows), NewLen: len(r.rows)}
}

// lazyCitable is one grounding pass's files, built on first use: a review with
// nothing to ground never computes the index (a git call and a stat per file),
// and a pass that grounds the quiz and the walkthrough computes it once. Nil
// before the file list is loaded, or when the index fails.
func (c *PrereviewController) lazyCitable(state *PrereviewState) func() *citable {
	var files *citable
	built := false
	return func() *citable {
		if !built && len(state.Files) > 0 {
			built = true
			if idx := c.changedIndex(state); idx != nil {
				files = c.citableFiles(state, idx)
			}
		}
		return files
	}
}

// groundUnderstand reloads the walkthrough and grounds it and the quiz in one
// pass, and reads the change's moved blocks from the same index (#115 B2).
// Mount and opening the Understand page call it once the file list is in.
func (c *PrereviewController) groundUnderstand(state *PrereviewState) {
	files := c.lazyCitable(state)
	c.groundQuizzes(state, files)
	c.groundQuestions(state, files)
	c.applyExplainWith(state, files)
	state.Moves = nil
	if f := files(); f != nil {
		state.Moves = f.idx.Moves
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

// groundImpact reports whether an impact anchor resolves in its file's working
// tree lines: the range lies within them and, when set, Contains occurs in it.
// It reads any regular file inside the repo, listed in the review or not: impact
// points outside the diff by design.
func groundImpact(rows []string, a ImpactAnchor) string {
	if a.FromLine < 1 || a.ToLine < a.FromLine || a.ToLine > len(rows) {
		return anchorUngrounded
	}
	if a.Contains != "" && !strings.Contains(strings.Join(rows[a.FromLine-1:a.ToLine], "\n"), a.Contains) {
		return anchorUngrounded
	}
	return anchorOK
}
