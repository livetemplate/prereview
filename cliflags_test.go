package main

import (
	"flag"
	"io"
	"slices"
	"testing"
)

func TestParseInterspersed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantBody string
		wantAns  bool
		wantPos  []string
	}{
		{"flags first", []string{"--answered", "--body", "hi", "c1"}, "hi", true, []string{"c1"}},
		{"flags after the id (the documented reply form)", []string{"--out", "s", "--answered", "c1", "--body", "hi"}, "hi", true, []string{"c1"}},
		{"flags between positionals", []string{"a", "--body", "x", "b"}, "x", false, []string{"a", "b"}},
		{"terminator keeps a dash-led positional", []string{"--body", "x", "--", "--not-a-flag", "c1"}, "x", false, []string{"--not-a-flag", "c1"}},
		{"a value that looks like a flag stays a value", []string{"--body", "-x", "c1"}, "-x", false, []string{"c1"}},
		{"no positionals", []string{"--answered"}, "", true, nil},
		{"a flag's \"--\" value is not a terminator", []string{"--body", "--", "c1", "--answered"}, "--", true, []string{"c1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			fs.String("out", "", "")
			body := fs.String("body", "", "")
			ans := fs.Bool("answered", false, "")
			if err := parseInterspersed(fs, tc.args); err != nil {
				t.Fatal(err)
			}
			if *body != tc.wantBody || *ans != tc.wantAns || !slices.Equal(fs.Args(), tc.wantPos) {
				t.Errorf("body=%q answered=%v args=%q, want %q %v %q", *body, *ans, fs.Args(), tc.wantBody, tc.wantAns, tc.wantPos)
			}
		})
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := parseInterspersed(fs, []string{"c1", "--nope"}); err == nil {
		t.Error("an unknown flag after a positional must still be an error")
	}
}

// TestParseInterspersedText: free-text positionals (`status done "- fixed X"`) keep a
// dash-led word that names no flag, while a real flag anywhere is still parsed.
func TestParseInterspersedText(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	out := fs.String("out", "", "")
	if err := parseInterspersedText(fs, []string{"done", "- fixed X;", "--dry-run", "now works", "--out", "S"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"done", "- fixed X;", "--dry-run", "now works"}; *out != "S" || !slices.Equal(fs.Args(), want) {
		t.Errorf("out=%q args=%q, want S %q", *out, fs.Args(), want)
	}
}
