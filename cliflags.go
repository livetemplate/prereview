package main

import (
	"flag"
	"strings"
)

// parseInterspersed parses fs like fs.Parse, but keeps reading flags after a
// positional argument, so `prereview reply <id> --body "…"` works the same as
// `prereview reply --body "…" <id>`. Go's flag package stops at the first
// non-flag, which left every later flag as a stray positional: the documented
// `reply --answered <id> --body …` failed with "takes exactly one id", and a
// `--out` after a path was silently ignored. A bare `--` still ends flag parsing;
// everything after it is positional. fs.Args() afterwards holds the positionals in
// order. An unknown flag is an error, as with fs.Parse.
func parseInterspersed(fs *flag.FlagSet, args []string) error {
	return parseArgs(fs, args, false)
}

// parseInterspersedText is parseInterspersed for a command whose positionals are
// free text (`status <state> <message…>`): a dash-led word that names no flag of fs
// ("- fixed X", "--dry-run now works") stays part of the text instead of failing.
func parseInterspersedText(fs *flag.FlagSet, args []string) error {
	return parseArgs(fs, args, true)
}

func parseArgs(fs *flag.FlagSet, args []string, unknownIsText bool) error {
	var pos []string
	for len(args) > 0 {
		if unknownIsText && args[0] != "--" && isFlagLike(args[0]) && fs.Lookup(flagName(args[0])) == nil {
			pos, args = append(pos, args[0]), args[1:]
			continue
		}
		if err := fs.Parse(args); err != nil {
			return err
		}
		rest := fs.Args()
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" && !(n >= 2 && takesValue(fs, args[n-2])) {
			pos = append(pos, rest...) // a terminator (not a flag's "--" value): the rest is positional
			break
		}
		if len(rest) == 0 {
			break
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
	// Re-parse just the positionals behind a terminator so fs.Args() returns them
	// without touching any flag value.
	return fs.Parse(append([]string{"--"}, pos...))
}

// isFlagLike reports whether arg is shaped like a flag ("-x", "--x", "--x=v"); a bare
// "-" (stdin) is a positional.
func isFlagLike(arg string) bool { return len(arg) > 1 && arg[0] == '-' }

func flagName(arg string) string {
	name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
	return name
}

// takesValue reports whether arg is a flag of fs written without "=" that consumes
// the next argument as its value (any non-boolean flag).
func takesValue(fs *flag.FlagSet, arg string) bool {
	if !isFlagLike(arg) || strings.Contains(arg, "=") {
		return false
	}
	f := fs.Lookup(flagName(arg))
	if f == nil {
		return false
	}
	b, isBool := f.Value.(interface{ IsBoolFlag() bool })
	return !isBool || !b.IsBoolFlag()
}
