package gitdiff

import (
	"fmt"
	"strings"
	"testing"
)

// TestDetectMoves (#115 B2): a block moved into a new file is found at both ends,
// as are one moved with a new indent and one moved down its own file; a 2-line
// block, a block with fewer than 20 letters and digits, and a block re-indented
// in place are not moves.
func TestDetectMoves(t *testing.T) {
	block := func(name string) string {
		return "\tif err := " + name + "(order); err != nil {\n\t\treturn fmt.Errorf(\"" + name + ": %w\", err)\n\t}\n\tlog.Printf(\"" + name + " %s\", order.ID)\n"
	}
	filler := strings.Repeat("\tstep()\n", 12)
	repo := fixtureRepo(t, map[string]string{
		"a.go": "package a\n\nfunc A() {\n" + block("charge") + "}\n",
		"c.go": "package c\n\nvar first = loadTheFirstValue()\nvar second = loadTheSecondValue()\n",
		"e.go": "package e\n\nfunc E() {\n{\n  i++\n}\n}\n",
		"g.go": "package g\n\nfunc G() {\n" + block("refund") + "}\n",
		"m.go": "package m\n\nfunc M() {\n" + block("settle") + "}\n",
		"s.go": "package s\n\nfunc S() {\n" + block("audit") + filler + "}\n",
	})
	write(t, repo, "a.go", "package a\n\nfunc A() {\n}\n")                    // the block left a.go...
	write(t, repo, "b.go", "package b\n\nfunc B() {\n"+block("charge")+"}\n") // ...into a new, untracked file
	write(t, repo, "c.go", "package c\n")                                     // 2 lines leave c.go: too short
	write(t, repo, "d.go", "package d\n\nvar first = loadTheFirstValue()\nvar second = loadTheSecondValue()\n")
	write(t, repo, "e.go", "package e\n\nfunc E() {\n}\n") // too few letters and digits
	write(t, repo, "f.go", "package f\n\nfunc F() {\n{\n  i++\n}\n}\n")
	write(t, repo, "g.go", "package g\n\nfunc G() {\n\tif ok {\n"+indent(block("refund"))+"\t}\n}\n") // in place
	write(t, repo, "m.go", "package m\n\nfunc M() {\n}\n")                                            // moved with a deeper indent
	write(t, repo, "n.go", "package n\n\nfunc N() {\n\tfor {\n"+indent(block("settle"))+"\t}\n}\n")
	write(t, repo, "s.go", "package s\n\nfunc S() {\n"+filler+block("audit")+"}\n") // moved down its file
	files, err := ListFiles(repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	idx, err := LoadChangedIndex(repo, "HEAD", files)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range idx.Moves {
		got = append(got, fmt.Sprintf("%s %s L%d-L%d -> %s L%d", m.File, m.Side, m.From, m.To, m.Other.File, m.Other.Line))
	}
	want := []string{
		"a.go old L4-L7 -> b.go L4",
		"b.go new L4-L7 -> a.go L4",
		"m.go old L4-L7 -> n.go L5",
		"n.go new L5-L8 -> m.go L4",
		"s.go old L4-L7 -> s.go L16",
		"s.go new L16-L19 -> s.go L4",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("moves =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func indent(s string) string {
	return "\t" + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n\t") + "\n"
}
