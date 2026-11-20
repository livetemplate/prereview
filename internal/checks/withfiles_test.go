package checks

import (
	"slices"
	"testing"
)

// TestWithFiles: a rule or judge whose parsed Files are empty gains the globs
// (and still parses and validates); an explicit empty `files: []` is replaced,
// never doubled (yaml refuses a duplicate key).
func TestWithFiles(t *testing.T) {
	rule := "extends: existence\nname: No basically\nmessage: Cut it.\ntokens: [basically]"
	judge := "---\nkind: judge\nname: One line\n---\nIs it one line?\n"
	globs := []string{"docs/**/*.md", "reply.md"}
	for _, body := range []string{rule, rule + "\nfiles: []\n", "extends: existence\nfiles: []\nname: n\nmessage: m\ntokens: [x]\n"} {
		scoped := WithFiles(body, false, globs)
		if r := ParseRule("x", SourceLearned, "", []byte(scoped)); !slices.Equal(r.Files, globs) || r.Err != "" {
			t.Errorf("rule files = %v (err %q):\n%s", r.Files, r.Err, scoped)
		}
		if err := ValidateDrafted([]byte(scoped), false); err != nil {
			t.Errorf("scoped rule doesn't validate: %v", err)
		}
	}
	for _, body := range []string{judge, "---\nkind: judge\nfiles: []\nname: One line\n---\nIs it one line?\n"} {
		r := ParseJudge("x", SourceLearned, "", []byte(WithFiles(body, true, globs)))
		if !slices.Equal(r.Files, globs) || r.Err != "" || r.Rubric != "Is it one line?" {
			t.Errorf("judge = files %v, rubric %q (err %q)", r.Files, r.Rubric, r.Err)
		}
	}
	if got := WithFiles(rule, false, nil); got != rule {
		t.Errorf("no globs changed the rule")
	}
}
