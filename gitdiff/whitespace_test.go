package gitdiff

import (
	"fmt"
	"strings"
	"testing"
)

// TestPairWhitespace: a re-indented line pairs into one unchanged row (old number
// from the deleted line, new number and text from the added one); a line whose
// text changed doesn't; a kept row never pairs; lines that only swapped places
// pair one of the two, not both; context rows pass through.
func TestPairWhitespace(t *testing.T) {
	lines := []DiffLine{
		{Kind: "ctx", OldNum: 1, NewNum: 1, Content: "func f() {"},
		{Kind: "del", OldNum: 2, Content: "  a := 1"},
		{Kind: "del", OldNum: 3, Content: "  b := 2"},
		{Kind: "del", OldNum: 4, Content: "  return a"},
		{Kind: "add", NewNum: 2, Content: "\ta := 1"},
		{Kind: "add", NewNum: 3, Content: "\tb := 3"},   // a real change
		{Kind: "add", NewNum: 4, Content: "\treturn a"}, // kept: never pairs
		{Kind: "ctx", OldNum: 5, NewNum: 5, Content: "}"},
		{Kind: "del", OldNum: 6, Content: "x()"},
		{Kind: "del", OldNum: 7, Content: "y()"},
		{Kind: "add", NewNum: 6, Content: "y()"},
		{Kind: "add", NewNum: 7, Content: " x()"},
	}
	keep := func(l DiffLine) bool { return l.Kind == "add" && l.NewNum == 4 }
	var got []string
	for _, l := range PairWhitespace(lines, keep) {
		got = append(got, fmt.Sprintf("%s:%d-%d:%q", l.Kind, l.OldNum, l.NewNum, l.Content))
	}
	want := []string{
		`ctx:1-1:"func f() {"`,
		`ctx:2-2:"\ta := 1"`,
		`del:3-0:"  b := 2"`,
		`del:4-0:"  return a"`,
		`add:0-3:"\tb := 3"`,
		`add:0-4:"\treturn a"`,
		`ctx:5-5:"}"`,
		`del:6-0:"x()"`,
		`ctx:7-6:"y()"`,
		`add:0-7:" x()"`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("PairWhitespace =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if out := PairWhitespace(lines[:1], nil); len(out) != 1 {
		t.Errorf("context only: %d rows, want 1", len(out))
	}
}
