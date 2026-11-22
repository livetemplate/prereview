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
	"strings"
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
	state.NotExplained, state.Mechanical = nil, nil
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

	for i := range state.Explanations {
		e := &state.Explanations[i]
		e.Stale = seen[e.seenKey()] != idx.Fingerprint
	}

	// Only the newest explanation is shown, so only it is grounded.
	listed := map[string]bool{}
	for _, f := range state.Files {
		if state.inScope(f.Path) {
			listed[f.Path] = true
		}
	}
	type fileRows struct {
		rows []string
		ok   bool
	}
	read := map[string]fileRows{} // each file outside the index, read once
	rowsOf := func(path string) fileRows {
		r, done := read[path]
		if !done {
			r.rows, r.ok = gitdiff.FileLines(c.RepoPath, path)
			read[path] = r
		}
		return r
	}
	// lookup is the file a step or diagram anchor may cite: changed, or listed.
	lookup := func(path string) *gitdiff.FileChanges {
		if fc := idx.Files[path]; fc != nil {
			return fc
		}
		if !listed[path] {
			return nil
		}
		r := rowsOf(path)
		if !r.ok {
			return &gitdiff.FileChanges{Opaque: true}
		}
		return &gitdiff.FileChanges{OldLen: len(r.rows), NewLen: len(r.rows)}
	}
	ground := func(as []Anchor) {
		for k := range as {
			a := &as[k]
			a.Status = groundRange(lookup(a.File), a.Kind, a.Side, a.FromLine, a.ToLine)
		}
	}
	groundImpacts := func(as []ImpactAnchor) {
		for k := range as {
			as[k].Status = groundImpact(rowsOf(as[k].File).rows, as[k])
		}
	}
	e := state.CurrentExplanation()
	for j := range e.Steps {
		ground(e.Steps[j].Anchors)
		groundImpacts(e.Steps[j].Impact)
	}
	for j := range e.Diagrams {
		ground(e.Diagrams[j].Anchors)
	}
	state.NotExplained = notExplained(idx, e.Steps)
	if !e.Stale {
		state.Mechanical = mechanicalHunks(idx, *e)
	}
}

// MechHunk is a changed hunk that folds: its key among the file's hunks
// ("H<old start>-<new start>") and the title of the mechanical concern that
// explains it, both fixed when the folds are derived.
type MechHunk struct {
	Hunk         gitdiff.Hunk
	Key, Concern string // Concern is the concern's id
	Title        string
}

// mechanicalHunks is each file's changed hunks that fold: at least one grounded
// line anchor covers the hunk, and every one that does belongs to a step in a
// mechanical concern. Derived from the anchors, so the agent can't fold code it
// didn't cite, and a hunk no step cites (Not explained) never folds.
func mechanicalHunks(idx *gitdiff.ChangedIndex, e Explanation) map[string][]MechHunk {
	mech := map[string]string{} // mechanical concern id -> title
	for _, c := range e.Concerns {
		if c.Mechanical {
			mech[c.ID] = c.Title
		}
	}
	if len(mech) == 0 {
		return nil
	}
	var out map[string][]MechHunk
	for p, cs := range citesByFile(e.Steps) {
		fc := idx.Files[p]
		if fc == nil || fc.Opaque {
			continue
		}
		for _, h := range fc.Hunks {
			concern, all := "", true
			for _, c := range cs {
				if !c.covers(h) {
					continue
				}
				if _, ok := mech[c.Concern]; !ok {
					all = false
					break
				}
				if concern == "" {
					concern = c.Concern
				}
			}
			if all && concern != "" {
				if out == nil {
					out = map[string][]MechHunk{}
				}
				out[p] = append(out[p], MechHunk{Hunk: h, Key: fmt.Sprintf("H%d-%d", h.OldStart, h.NewStart), Concern: concern, Title: mech[concern]})
			}
		}
	}
	return out
}

func (c *PrereviewController) RevealMechanical(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	if hunk := ctx.GetString("hunk"); hunk != "" {
		state.MechRevealed = withKey(state.MechRevealed, mechRevealKey(state.SelectedFile, hunk))
	}
	return state, nil
}

// mechRevealKey names an opened fold in MechRevealed.
func mechRevealKey(file, hunk string) string { return file + "\x00" + hunk }

// openFoldAt opens the mechanical fold holding row l of the open file, if any:
// a jump that lands on a folded line must leave it readable after its reveal.
func openFoldAt(state *PrereviewState, l gitdiff.DiffLine) {
	if m := state.mechHunkOf(l); m != nil {
		state.MechRevealed = withKey(state.MechRevealed, mechRevealKey(state.SelectedFile, m.Key))
	}
}

// withKey sets k in m, allocating m when nil.
func withKey(m map[string]bool, k string) map[string]bool {
	if m == nil {
		m = map[string]bool{}
	}
	m[k] = true
	return m
}

// ToggleFoldMechanical turns every mechanical fold off or back on: a durable
// view pref, so hidden code is one switch away from never hiding.
func (c *PrereviewController) ToggleFoldMechanical(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.UnfoldMechanical = !state.UnfoldMechanical
	state.MoreMenuOpen = false
	c.savePrefs(state)
	return state, nil
}

// notExplained is every changed hunk no grounded anchor of steps covers, as
// anchors ready to walk, in path then line order. A hunk counts as explained when
// an anchor overlaps it on its side (a whole-file anchor doesn't: citing a file
// is not explaining each of its changes); an opaque file (binary, over the render
// cap) has no lines, so a whole-file anchor is what explains it. Derived from the
// diff, never from the agent: it is the check on what the walkthrough left out.
func notExplained(idx *gitdiff.ChangedIndex, steps []Step) []Anchor {
	cited := citesByFile(steps)
	var out []Anchor
	for _, p := range slices.Sorted(maps.Keys(idx.Files)) {
		fc, cs := idx.Files[p], cited[p]
		if fc.Opaque {
			if !slices.ContainsFunc(cs, func(c cite) bool { return c.Kind == AnchorKindFile }) {
				out = append(out, Anchor{File: p, Kind: AnchorKindFile, Side: "new", Status: anchorOK})
			}
			continue
		}
		for _, h := range fc.Hunks {
			if slices.ContainsFunc(cs, func(c cite) bool { return c.covers(h) }) {
				continue
			}
			side, from, to := h.Range()
			out = append(out, Anchor{File: p, Kind: AnchorKindLine, Side: side, FromLine: from, ToLine: to, Status: anchorOK})
		}
	}
	return out
}

// cite is a grounded step anchor and the concern of the step that cites it.
type cite struct {
	Anchor
	Concern string
}

// covers reports that the cite explains hunk h: a line anchor overlapping it on
// its side. A whole-file anchor explains no hunk (citing a file is not
// explaining each of its changes).
func (c cite) covers(h gitdiff.Hunk) bool {
	return c.Kind == AnchorKindLine && h.Covers(c.Side, c.FromLine, c.ToLine)
}

// citesByFile is every grounded anchor of steps, by file: what coverage and
// the mechanical folds both read.
func citesByFile(steps []Step) map[string][]cite {
	out := map[string][]cite{}
	for _, st := range steps {
		for _, a := range st.Anchors {
			if !a.Ungrounded() {
				out[a.File] = append(out[a.File], cite{a, st.Concern})
			}
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
		openFoldAt(&state, l)
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
