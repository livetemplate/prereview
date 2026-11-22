package gitdiff

import "strconv"

// CollapseToHunks turns a full-file line list (every line tagged
// add/del/ctx, as LoadDiff produces with -U999999) into a true diff
// view: only changed lines plus `ctx` unchanged lines on each side of
// a change. A line for which keep reports true is treated like a change
// (kept, with the same context) so a caller can hold an unchanged line
// out of the folds, e.g. one carrying an annotation card; keep may be
// nil. Each maximal run of dropped unchanged lines is replaced by a
// single synthetic fold line:
//
//	Kind == "fold", Content == "<N>"  (N = number of skipped lines)
//
// The fold line's OldNum/NewNum are set to the first skipped line's
// numbers so the template can derive a stable, collision-free
// data-key (gaps never overlap).
//
// If the input contains no add/del line (an unchanged file — shown via
// the all-files scope toggle or the clean-tree fallback) there is no
// diff to collapse, so the input is returned unchanged. Callers treat
// that as "show the whole file".
func CollapseToHunks(lines []DiffLine, ctx int, keep func(DiffLine) bool) []DiffLine {
	ctx = max(ctx, 0)
	changed := false
	for i := range lines {
		if lines[i].Kind == "add" || lines[i].Kind == "del" {
			changed = true
			break
		}
	}
	if !changed {
		return lines
	}

	kept := make([]bool, len(lines))
	for i := range lines {
		changed := lines[i].Kind == "add" || lines[i].Kind == "del"
		if !changed && (keep == nil || !keep(lines[i])) {
			continue
		}
		lo := max(i-ctx, 0)
		hi := min(i+ctx, len(lines)-1)
		for j := lo; j <= hi; j++ {
			kept[j] = true
		}
	}

	out := make([]DiffLine, 0, len(lines))
	for i := 0; i < len(lines); {
		if kept[i] {
			out = append(out, lines[i])
			i++
			continue
		}
		// Maximal run of dropped lines -> one fold marker.
		start := i
		for i < len(lines) && !kept[i] {
			i++
		}
		out = append(out, DiffLine{
			Kind:    "fold",
			OldNum:  lines[start].OldNum,
			NewNum:  lines[start].NewNum,
			Content: strconv.Itoa(i - start),
		})
	}
	return out
}

// Folded reports a synthetic row that stands for hidden lines (a CollapseToHunks
// "fold" or a FoldRanges "mech"), not a line of the file.
func (l DiffLine) Folded() bool { return l.Kind == "fold" || l.Kind == "mech" }

// FoldRanges folds changed rows out of the way: each maximal run of rows that
// group names with the same non-empty key becomes one synthetic row,
//
//	Kind == "mech", Content == "<N>", Group == the key
//
// numbered like the run's first row (so its data-key, M<old>-<new>, is stable and
// apart from CollapseToHunks' F<old>-<new>). A row keep reports true is never
// folded and splits its run; keep may be nil. Every other row passes through.
func FoldRanges(lines []DiffLine, group func(DiffLine) string, keep func(DiffLine) bool) []DiffLine {
	// Each row's group, once ("" = not folded, kept rows included).
	groups := make([]string, len(lines))
	for i, l := range lines {
		if g := group(l); g != "" && (keep == nil || !keep(l)) {
			groups[i] = g
		}
	}
	out := make([]DiffLine, 0, len(lines))
	for i := 0; i < len(lines); {
		g := groups[i]
		if g == "" {
			out = append(out, lines[i])
			i++
			continue
		}
		start := i
		for i < len(lines) && groups[i] == g {
			i++
		}
		out = append(out, DiffLine{
			Kind:    "mech",
			OldNum:  lines[start].OldNum,
			NewNum:  lines[start].NewNum,
			Content: strconv.Itoa(i - start),
			Group:   g,
		})
	}
	return out
}
