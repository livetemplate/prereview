package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/csv"
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
	state.NotExplained = nil
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
	state.NotExplained = notExplained(idx, state.CurrentExplanation().Steps)
}

// notExplained is every changed hunk no grounded anchor of steps covers, as
// anchors ready to walk, in path then line order. A hunk counts as explained when
// an anchor overlaps it on its side (a whole-file anchor doesn't: citing a file
// is not explaining each of its changes); an opaque file (binary, over the render
// cap) has no lines, so a whole-file anchor is what explains it. Derived from the
// diff, never from the agent: it is the check on what the walkthrough left out.
func notExplained(idx *gitdiff.ChangedIndex, steps []Step) []Anchor {
	cited := map[string][]Anchor{}
	for _, st := range steps {
		for _, a := range st.Anchors {
			if !a.Ungrounded() {
				cited[a.File] = append(cited[a.File], a)
			}
		}
	}
	var out []Anchor
	for _, p := range slices.Sorted(maps.Keys(idx.Files)) {
		fc, as := idx.Files[p], cited[p]
		if fc.Opaque {
			if !slices.ContainsFunc(as, func(a Anchor) bool { return a.Kind == AnchorKindFile }) {
				out = append(out, Anchor{File: p, Kind: AnchorKindFile, Side: "new", Status: anchorOK})
			}
			continue
		}
		for _, h := range fc.Hunks {
			if slices.ContainsFunc(as, func(a Anchor) bool { return a.Kind == AnchorKindLine && h.Covers(a.Side, a.FromLine, a.ToLine) }) {
				continue
			}
			side, from, to := h.Range()
			out = append(out, Anchor{File: p, Kind: AnchorKindLine, Side: side, FromLine: from, ToLine: to, Status: anchorOK})
		}
	}
	return out
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

// The Understand page's tabs. "" picks one from what there is (UnderstandOnQuiz).
const (
	understandTabWalkthrough = "walkthrough"
	understandTabQuiz        = "quiz"
)

// understandTab is tab when it names a tab, else "" (the default).
func understandTab(tab string) string {
	if tab == understandTabWalkthrough || tab == understandTabQuiz {
		return tab
	}
	return ""
}

// ToggleUnderstand opens or closes the Understand page (#115), the "z" shortcut.
// An optional "tab" opens it on that tab ("quiz" from the stepper's Overview).
// Closes the overflow menu so the page is visible at once; not persisted, like
// All comments.
func (c *PrereviewController) ToggleUnderstand(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	toggleFullView(&state, &state.ShowUnderstand)
	state.MoreMenuOpen = false
	if state.ShowUnderstand {
		state.UnderstandTab = understandTab(ctx.GetString("tab"))
		// Ground again on open: the agent may have edited the files since load
		// (an edit is only reloaded once settled, #198).
		c.groundQuizzes(&state)
		c.applyExplain(&state)
	}
	return state, nil
}

// SetUnderstandTab switches the Understand page's tab.
func (c *PrereviewController) SetUnderstandTab(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.UnderstandTab = understandTab(ctx.GetString("tab"))
	return state, nil
}

// RequestExplain queues "Explain this change": a request row (request=explain)
// on the open file whose body is the walkthrough prompt plus the scope, since the
// walkthrough covers the whole change, not the file the request is filed on. It
// files on the open file because every reader of a comment assumes one (the same
// as Quiz me and Run judges).
func (c *PrereviewController) RequestExplain(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if state.SelectedFile == "" {
		return state, fmt.Errorf("requestExplain: no file selected")
	}
	body := promptBody(state.ExplainPrompts, ctx.GetString("slug"))
	if body == "" {
		return state, nil // no prompts, or a stale slug from an old render
	}
	n := len(state.changedFiles())
	if c.NoGit {
		body += fmt.Sprintf("\n\nScope: the whole review, %d file(s), not only this one.", n)
	} else {
		body += fmt.Sprintf("\n\nScope: the whole change, %d changed file(s) against `%s`, not only this one.", n, c.effectiveBase(&state))
	}
	state, err := c.queueRequest(state, csv.RequestExplain, body)
	if err != nil {
		return state, err
	}
	state.Flash = "Walkthrough requested — the agent will answer shortly"
	return state, nil
}

// JumpToAnchor opens the file a walkthrough step cites, at the cited line. The
// file comes with the action, never from SelectedFile: a walkthrough spans files.
func (c *PrereviewController) JumpToAnchor(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	file := ctx.GetString("file")
	if file == "" {
		return state, fmt.Errorf("jumpToAnchor: missing file")
	}
	line, _ := strconv.Atoi(ctx.GetString("line"))
	return c.jumpToLine(state, file, ctx.GetString("side"), line)
}

// jumpToLine opens file and parks the cursor on line (on side), closing any
// full-page view. The raw view is revealed, as for a search hit, so the row is in
// the DOM even inside a fold or a rendered-Markdown file. line 0 just opens the
// file. Shared by every "go read the code this cites" link.
func (c *PrereviewController) jumpToLine(state PrereviewState, file, side string, line int) (PrereviewState, error) {
	state, err := c.selectFileForJump(state, file)
	if err != nil {
		return state, err
	}
	state.CursorKey, state.RevealFile = "", ""
	if l, ok := findDiffLine(state.CurrentDiff, side, line); ok {
		state.CursorKey, state.RevealFile = lineCursorKey(l), file
	}
	return state, nil
}

// StartWalkthrough begins walking the current walkthrough (#115 A3) at a step
// ("step", 1-based; the first when absent): it shows that step's first stop and
// opens its file there, closing the Understand page.
func (c *PrereviewController) StartWalkthrough(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	e := state.CurrentExplanation()
	if e == nil {
		return state, fmt.Errorf("startWalkthrough: no walkthrough")
	}
	steps := state.walkSteps()
	stops := walkStopsOf(steps)
	step, _ := strconv.Atoi(ctx.GetString("step"))
	i := max(slices.IndexFunc(stops, func(w walkStop) bool { return w.step == step-1 }), 0)
	state.WalkExplanation = e.ID
	return c.showWalkStop(state, steps, stops[i])
}

// WalkNext / WalkPrev move to the next or previous stop; at either end they stay.
func (c *PrereviewController) WalkNext(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.stepWalk(state, +1)
}

func (c *PrereviewController) WalkPrev(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	return c.stepWalk(state, -1)
}

func (c *PrereviewController) stepWalk(state PrereviewState, delta int) (PrereviewState, error) {
	steps, stops, i, ok := state.walkPos()
	if !ok {
		state.WalkExplanation = ""
		return state, nil
	}
	return c.showWalkStop(state, steps, stops[min(max(i+delta, 0), len(stops)-1)])
}

// EndWalkthrough puts the step bar away.
func (c *PrereviewController) EndWalkthrough(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.WalkExplanation = ""
	return state, nil
}

// showWalkStop makes at the current stop and opens its file at its line. A stop
// with nothing to jump to (no anchor resolves) stays on the file being read and
// only closes any page over it; the bar says why.
func (c *PrereviewController) showWalkStop(state PrereviewState, steps []Step, at walkStop) (PrereviewState, error) {
	state.WalkStep, state.WalkAnchor = at.step, at.anchor
	if at.anchor < 0 {
		closeFullViews(&state)
		return state, nil
	}
	w := walkAnchor(steps[at.step].Anchors[at.anchor])
	return c.jumpToLine(state, w.File, w.Side, w.Line)
}

// ToggleDrawerOrder switches the file drawer between alphabetical and
// walkthrough order.
func (c *PrereviewController) ToggleDrawerOrder(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.DrawerWalkOrder = !state.DrawerWalkOrder
	return state, nil
}
