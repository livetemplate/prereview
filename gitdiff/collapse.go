package gitdiff

import "strconv"

// CollapseToHunks turns a full-file line list (every line tagged
// add/del/ctx, as LoadDiff produces with -U999999) into a true diff
// view: only changed lines plus `ctx` unchanged lines on each side of
// a change. Each maximal run of dropped unchanged lines is replaced by
// a single synthetic fold line:
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
func CollapseToHunks(lines []DiffLine, ctx int) []DiffLine {
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

	keep := make([]bool, len(lines))
	for i := range lines {
		if lines[i].Kind != "add" && lines[i].Kind != "del" {
			continue
		}
		lo := max(i-ctx, 0)
		hi := min(i+ctx, len(lines)-1)
		for j := lo; j <= hi; j++ {
			keep[j] = true
		}
	}

	out := make([]DiffLine, 0, len(lines))
	for i := 0; i < len(lines); {
		if keep[i] {
			out = append(out, lines[i])
			i++
			continue
		}
		// Maximal run of dropped lines -> one fold marker.
		start := i
		for i < len(lines) && !keep[i] {
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
