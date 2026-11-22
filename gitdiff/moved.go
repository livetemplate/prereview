package gitdiff

import (
	"maps"
	"slices"
	"strings"
	"unicode"
)

// Move is one end of a moved block (#115 B2): lines deleted in one place and
// added in another, the same text once each line is trimmed. Side "old" is the
// deleted end, "new" the added one; Other is the first line of the other end.
type Move struct {
	File     string
	Side     string
	From, To int
	Other    MoveEnd
}

// MoveEnd is where a moved block's other end starts.
type MoveEnd struct {
	File string
	Line int
}

// A moved block is at least minMoveLines lines holding at least minMoveAlnum
// letters and digits: git's --color-moved bar ("at least 20 alphanumeric
// characters"), plus a line floor so a lone closing brace and a blank line never
// count. maxMoveCandidates skips a line text that recurs this often in the added
// lines (a "}" or "return nil"): a block starting there is found from its next
// line instead, which keeps a pass near linear.
//
// inPlaceLines: in one file, a block whose added copy starts within this many
// lines of where its deleted copy would now sit is re-indented in place, not
// moved (git can split a re-indent into several hunks).
const (
	minMoveLines      = 3
	minMoveAlnum      = 20
	maxMoveCandidates = 50
	inPlaceLines      = 8
)

// addedLine is one added line's place: which file and hunk, and its index in the
// hunk's New.
type addedLine struct {
	file string
	hunk int
	i    int
}

// detectMoves finds moved blocks across files, greedily, as git does: each run
// of deleted lines, in file and hunk order, takes the longest unused run of added
// lines with the same trimmed text, from any file. A block that stays in place
// (inPlaceLines) is a whitespace change, not a move.
func detectMoves(files map[string]*FileChanges) []Move {
	paths := slices.Sorted(maps.Keys(files))
	byText := map[string][]addedLine{}
	for _, p := range paths {
		for h, hk := range files[p].Hunks {
			for i, l := range hk.New {
				if t := strings.TrimSpace(l); t != "" {
					byText[t] = append(byText[t], addedLine{p, h, i})
				}
			}
		}
	}
	used := map[addedLine]bool{}
	var out []Move
	for _, p := range paths {
		for h, hk := range files[p].Hunks {
			for i := 0; i < len(hk.Old); {
				best, n := bestMove(files, byText, used, p, h, hk.Old, i)
				if n < minMoveLines || alnum(hk.Old[i:i+n]) < minMoveAlnum {
					i++
					continue
				}
				dst := files[best.file].Hunks[best.hunk]
				for k := range n {
					used[addedLine{best.file, best.hunk, best.i + k}] = true
				}
				from, to := hk.OldStart+i, dst.NewStart+best.i
				out = append(out,
					Move{File: p, Side: "old", From: from, To: from + n - 1, Other: MoveEnd{best.file, to}},
					Move{File: best.file, Side: "new", From: to, To: to + n - 1, Other: MoveEnd{p, from}})
				i += n
			}
		}
	}
	return out
}

// bestMove is the longest unused run of added lines matching old[i:], not in
// place, and its length.
func bestMove(files map[string]*FileChanges, byText map[string][]addedLine, used map[addedLine]bool, file string, hunk int, old []string, i int) (addedLine, int) {
	var best addedLine
	n := 0
	cands := byText[strings.TrimSpace(old[i])]
	if len(cands) > maxMoveCandidates {
		return best, 0
	}
	// Where the deleted line would sit in the new file: its hunk's new start.
	here := files[file].Hunks[hunk].NewStart + i
	for _, c := range cands {
		if c.file == file && abs(files[c.file].Hunks[c.hunk].NewStart+c.i-here) <= inPlaceLines {
			continue
		}
		added := files[c.file].Hunks[c.hunk].New
		k := 0
		for i+k < len(old) && c.i+k < len(added) && !used[addedLine{c.file, c.hunk, c.i + k}] &&
			strings.TrimSpace(old[i+k]) == strings.TrimSpace(added[c.i+k]) {
			k++
		}
		if k > n {
			best, n = c, k
		}
	}
	return best, n
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// alnum counts letters and digits.
func alnum(lines []string) int {
	n := 0
	for _, l := range lines {
		for _, r := range l {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				n++
			}
		}
	}
	return n
}
