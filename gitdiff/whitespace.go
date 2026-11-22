package gitdiff

import (
	"strings"
	"unicode"
)

// maxPairCells caps the LCS table PairWhitespace builds for one run of changed
// rows (deleted × added). A larger run is left as it is: it is a rewrite, not a
// re-indent, and the table would cost more than it saves.
const maxPairCells = 1 << 18

// PairWhitespace shows lines that changed only in whitespace as unchanged: in
// each run of changed rows, a deleted and an added line whose text matches with
// all whitespace removed become one "ctx" row, numbered from both (OldNum from
// the deleted row, NewNum, Content and HighlightedContent from the added one).
// Pairs come from a longest common subsequence, so reordered lines don't pair. A
// row keep reports true never pairs; keep may be nil. It is a view transform:
// the diff it is given is not changed.
func PairWhitespace(lines []DiffLine, keep func(DiffLine) bool) []DiffLine {
	out := make([]DiffLine, 0, len(lines))
	for i := 0; i < len(lines); {
		if lines[i].Kind != "del" && lines[i].Kind != "add" {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && (lines[j].Kind == "del" || lines[j].Kind == "add") {
			j++
		}
		out = append(out, pairRun(lines[i:j], keep)...)
		i = j
	}
	return out
}

// pairRun pairs one run of changed rows. Deleted and added rows keep their own
// order; a pair is emitted where both reach it, so old and new numbers both stay
// ascending.
func pairRun(run []DiffLine, keep func(DiffLine) bool) []DiffLine {
	var dels, adds []DiffLine
	for _, l := range run {
		if l.Kind == "del" {
			dels = append(dels, l)
		} else {
			adds = append(adds, l)
		}
	}
	if len(dels) == 0 || len(adds) == 0 || len(dels)*len(adds) > maxPairCells {
		return run
	}
	norm := func(l DiffLine) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, l.Content)
	}
	dn, an := make([]string, len(dels)), make([]string, len(adds))
	dk, ak := make([]bool, len(dels)), make([]bool, len(adds))
	for i, l := range dels {
		dn[i], dk[i] = norm(l), keep != nil && keep(l)
	}
	for i, l := range adds {
		an[i], ak[i] = norm(l), keep != nil && keep(l)
	}
	match := func(i, j int) bool { return !dk[i] && !ak[j] && dn[i] == an[j] }
	// lcs[i][j] = longest common subsequence of dels[i:] and adds[j:].
	lcs := make([][]int, len(dels)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(adds)+1)
	}
	for i := len(dels) - 1; i >= 0; i-- {
		for j := len(adds) - 1; j >= 0; j-- {
			switch {
			case match(i, j):
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	if lcs[0][0] == 0 {
		return run
	}
	out := make([]DiffLine, 0, len(run))
	i, j := 0, 0
	for i < len(dels) && j < len(adds) {
		switch {
		case match(i, j) && lcs[i][j] == lcs[i+1][j+1]+1:
			a := adds[j]
			out = append(out, DiffLine{Kind: "ctx", OldNum: dels[i].OldNum, NewNum: a.NewNum, Content: a.Content, HighlightedContent: a.HighlightedContent})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, dels[i])
			i++
		default:
			out = append(out, adds[j])
			j++
		}
	}
	out = append(out, dels[i:]...)
	return append(out, adds[j:]...)
}
