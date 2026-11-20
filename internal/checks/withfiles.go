package checks

import (
	"encoding/json"
	"regexp"
	"strings"
)

// filesLine is a top-level `files:` line, as an empty `files: []` writes it.
var filesLine = regexp.MustCompile(`(?m)^files\s*:[^\n]*\n?`)

// WithFiles scopes a drafted rule YAML (or, with judge, a judge's frontmatter)
// to globs: it writes a `files:` line, replacing an empty one. Call it only for
// a check whose parsed Files are empty (ScopeCheck); with no globs, or a judge
// with no frontmatter, it returns body unchanged.
func WithFiles(body string, judge bool, globs []string) string {
	if len(globs) == 0 {
		return body
	}
	list, _ := json.Marshal(globs) // a JSON array is a YAML flow sequence
	scope := func(yaml string) string {
		return strings.TrimRight(filesLine.ReplaceAllString(yaml, ""), "\n") + "\nfiles: " + string(list) + "\n"
	}
	if !judge {
		return scope(body)
	}
	front, rest, ok := SplitFrontmatter([]byte(body))
	if !ok {
		return body
	}
	return "---" + scope(string(front)) + "---\n" + string(rest)
}
