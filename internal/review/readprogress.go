package review

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/livetemplate/livetemplate"
)

// readprogress.go tracks how far the reviewer has scrolled/read each file (#128).
// The client's lvt-fx:viewport-report directive reports the topmost and bottommost
// visible line keys as the reviewer scrolls; the server keeps a per-file high-water
// mark (the furthest new-side line seen → the "reviewed" visual) and the last
// topmost line (the scroll-restore target when the file is re-opened). All of it is
// session view-state — nothing is written to disk and nothing is emitted to the
// agent; reading is a private, in-flight signal.
//
// KNOWN LIMITATION (accepted, high-water-mark model): jumping to the bottom via a
// heading/TOC link marks everything above as "read" even if it was skimmed past.
// Faithful "only what actually entered the viewport" would need union-of-ranges
// state; the high-water mark matches linear top-to-bottom reading, which is the
// common case.

// keyLine extracts the trailing line number from a viewport item's data-key,
// handling BOTH view formats: a code line "L<old>-<new>" → <new>, and a
// rendered-markdown block "MB-<start>-<end>" → <end>. Both encode "this element's
// bottom source line", which is the read high-water position. 0 if unparseable.
// Read progress is tracked by source-line position — stable across a diff refresh
// (unlike display ordinals) and shared by the code and preview views.
func keyLine(key string) int {
	dash := strings.LastIndexByte(key, '-')
	if dash < 0 {
		return 0
	}
	n, err := strconv.Atoi(key[dash+1:])
	if err != nil {
		return 0
	}
	return n
}

// ReportViewport records the client's read-progress report for the selected file:
// bottomKey advances the high-water mark (furthest new-side line seen), topKey
// becomes the scroll-restore target for the next time this file is opened. Both are
// per-file. A report with no selected file, or with unparseable keys, is a no-op.
// Never emits or persists to disk — pure session view-state.
func (c *PrereviewController) ReportViewport(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	file := state.SelectedFile
	if file == "" {
		return state, nil
	}
	if n := keyLine(ctx.GetString("bottomKey")); n > 0 {
		if state.ReadThrough == nil {
			state.ReadThrough = map[string]int{}
		}
		if n > state.ReadThrough[file] {
			state.ReadThrough[file] = n
		}
	}
	if top := ctx.GetString("topKey"); top != "" {
		if state.LastReadTopKey == nil {
			state.LastReadTopKey = map[string]string{}
		}
		state.LastReadTopKey[file] = top
	}
	// The reviewer has scrolled, so any pending one-shot restore is now consumed —
	// clear it so a later re-render can't yank them back to the restore point.
	state.ScrollToReadKey = ""
	return state, nil
}

// ReadFrontierKey returns the data-key of the element at the read frontier — the
// furthest-read line/block — for the CURRENT view (#128), so a "resume reading"
// jump can scroll straight to where the reviewer left off. It matches the exact
// key the template stamps on that element (a code line "L<old>-<new>" or a
// rendered-markdown block "MB-<start>-<end>"), so lvt-fx:scroll finds it. Empty
// when nothing's read or no diff is loaded. Zero-arg.
func (s PrereviewState) ReadFrontierKey() string {
	if len(s.ReadThrough) == 0 || s.SelectedFile == "" || s.CurrentDiff == nil {
		return ""
	}
	mark := s.ReadThrough[s.SelectedFile]
	if mark == 0 {
		return ""
	}
	// Preview view: the rendered-markdown block whose source span holds the mark.
	if s.ShowRenderedMarkdown() {
		for _, b := range s.CurrentDiff.MarkdownBlocks {
			if mark >= b.StartLine && mark <= b.EndLine {
				return markdownBlockKey(b.StartLine, b.EndLine)
			}
		}
		return ""
	}
	// Code view: the line whose new-side number IS the mark, else the first line
	// at/after it (the exact line may have been folded or is a pure deletion).
	best, bestNum := "", 0
	for _, ln := range s.CurrentDiff.Lines {
		if ln.NewNum == mark {
			return fmt.Sprintf("L%d-%d", ln.OldNum, ln.NewNum)
		}
		if ln.NewNum > mark && (bestNum == 0 || ln.NewNum < bestNum) {
			best, bestNum = fmt.Sprintf("L%d-%d", ln.OldNum, ln.NewNum), ln.NewNum
		}
	}
	return best
}

// HasReadFrontier reports whether there's a meaningful "resume reading" jump to
// offer — some progress, but not the whole file read. Gates the Resume affordance.
// Zero-arg.
func (s PrereviewState) HasReadFrontier() bool {
	p := s.ReadPercent()
	return p > 0 && p < 100
}

// JumpToReadFrontier is the "resume reading" action: a one-shot scroll to the
// furthest-read element, so the reviewer lands back where they left off.
func (c *PrereviewController) JumpToReadFrontier(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ScrollToReadKey = state.ReadFrontierKey()
	return state, nil
}
