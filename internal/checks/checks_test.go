package checks

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func rule(t *testing.T, yml string) *Rule {
	t.Helper()
	r := ParseRule("t", SourceUser, "", []byte(yml))
	if r.Err != "" {
		t.Fatalf("parse: %s", r.Err)
	}
	return r
}

func run(t *testing.T, yml, file, text string) Result {
	t.Helper()
	return RunInline(rule(t, yml), NewDoc(file, strings.Split(text, "\n")))
}

func TestExistence(t *testing.T) {
	yml := "extends: existence\nmessage: \"Avoid %s.\"\ntokens: [very, in order to]\n"
	res := run(t, yml, "a.md", "This is ✨ very good.\nVery nice.\nWe wrap in order\nto test.")
	if res.Status != StatusFail {
		t.Fatalf("status %q", res.Status)
	}
	// Case-sensitive by default: "Very" on line 2 doesn't match.
	if len(res.Findings) != 2 {
		t.Fatalf("findings %+v", res.Findings)
	}
	f := res.Findings[0]
	// Rune columns: ✨ is one column (matches Vale's Span [11,14] → [10,14)).
	if f.Line != 1 || f.FromCol != 10 || f.ToCol != 14 || f.Message != "Avoid very." {
		t.Errorf("first finding %+v", f)
	}
	// A phrase across a hard wrap matches and spans both lines.
	g := res.Findings[1]
	if g.Line != 3 || g.EndLine != 4 || g.FromCol != 8 || g.ToCol != 2 {
		t.Errorf("wrapped finding %+v", g)
	}

	res = run(t, yml+"ignorecase: true\n", "a.md", "Very nice.")
	if len(res.Findings) != 1 {
		t.Errorf("ignorecase: %+v", res.Findings)
	}
	// Word-bounded: "every" doesn't contain the word "very".
	if res := run(t, yml, "a.md", "every day"); res.Status != StatusPass {
		t.Errorf("word boundary: %+v", res.Findings)
	}
	// nonword drops the boundary.
	if res := run(t, "extends: existence\nnonword: true\ntokens: [very]\n", "a.md", "every day"); len(res.Findings) != 1 {
		t.Errorf("nonword: %+v", res.Findings)
	}
}

func TestSubstitution(t *testing.T) {
	yml := "extends: substitution\nignorecase: true\nswap:\n  utilize: use\n  e-mail: email\n"
	res := run(t, yml, "a.md", "Utilize the e-mail.")
	if len(res.Findings) != 2 {
		t.Fatalf("findings %+v", res.Findings)
	}
	if res.Findings[0].Message != "Use 'use' instead of 'Utilize'." {
		t.Errorf("message %q", res.Findings[0].Message)
	}
	// Already the preferred text → no finding.
	if res := run(t, "extends: substitution\nswap:\n  JavaScript: JavaScript\n", "a.md", "JavaScript"); res.Status != StatusPass {
		t.Errorf("same text flagged: %+v", res.Findings)
	}
}

func TestOccurrence(t *testing.T) {
	yml := "extends: occurrence\nmessage: Too long.\nmax: 3\ntoken: '\\b\\w+\\b'\n"
	res := run(t, yml, "a.md", "One two three. One two three four.\nShort one.")
	if len(res.Findings) != 1 {
		t.Fatalf("findings %+v", res.Findings)
	}
	if f := res.Findings[0]; f.Line != 1 || f.FromCol != 15 || f.ToCol != 34 {
		t.Errorf("sentence span %+v", f)
	}
	// A sentence wrapped across lines counts as one sentence.
	res = run(t, yml, "a.md", "One two\nthree four.")
	if len(res.Findings) != 1 || res.Findings[0].EndLine != 2 {
		t.Errorf("wrapped sentence %+v", res.Findings)
	}
	// A blank line or a list item ends the block.
	if res := run(t, yml, "a.md", "One two\n\nthree four"); res.Status != StatusPass {
		t.Errorf("blank line didn't split: %+v", res.Findings)
	}
	if res := run(t, yml, "a.md", "- one two\n- three four"); res.Status != StatusPass {
		t.Errorf("list items didn't split: %+v", res.Findings)
	}
	// Line scope.
	res = run(t, yml+"scope: line\n", "a.md", "a b c d\na b")
	if len(res.Findings) != 1 || res.Findings[0].Line != 1 || res.Findings[0].ToCol != 7 {
		t.Errorf("line scope %+v", res.Findings)
	}
}

func TestVocab(t *testing.T) {
	yml := "extends: vocab\naccept: [JavaScript]\nreject: [synergy]\n"
	res := run(t, yml, "a.md", "javascript and JavaScript and Synergy")
	if len(res.Findings) != 2 {
		t.Fatalf("findings %+v", res.Findings)
	}
	if res.Findings[0].Message != "Use 'JavaScript' instead of 'javascript'." || res.Findings[1].Message != "Avoid 'Synergy'." {
		t.Errorf("messages %q / %q", res.Findings[0].Message, res.Findings[1].Message)
	}
}

func TestMarkdownMasking(t *testing.T) {
	yml := "extends: existence\ntokens: [very]\n"
	text := strings.Join([]string{
		"---",
		"title: very",
		"---",
		"Some `very` code and [very](http://very.example) link <http://very.example>.",
		"```",
		"very in a fence",
		"```",
		"~~~go",
		"very",
		"~~~",
		"Real very.",
	}, "\n")
	res := run(t, yml, "doc.md", text)
	// Only the link TEXT on line 4 and line 11 are prose.
	if len(res.Findings) != 2 || res.Findings[0].Line != 4 || res.Findings[0].FromCol != 22 || res.Findings[1].Line != 11 {
		t.Fatalf("findings %+v", res.Findings)
	}
	// A .txt file isn't Markdown: nothing is masked.
	if res := run(t, yml, "doc.txt", "Some `very` code"); len(res.Findings) != 1 {
		t.Errorf("txt masked: %+v", res.Findings)
	}
}

func TestFilesGlobs(t *testing.T) {
	r := rule(t, "extends: existence\ntokens: [x]\n")
	for p, want := range map[string]bool{"README.md": true, "docs/a.txt": true, "main.go": false} {
		if got := r.Applies(p); got != want {
			t.Errorf("default Applies(%q) = %v", p, got)
		}
	}
	r = rule(t, "extends: existence\ntokens: [x]\nfiles: ['*.go', 'docs/*.md']\n")
	for p, want := range map[string]bool{"main.go": true, "docs/a.md": true, "README.md": false} {
		if got := r.Applies(p); got != want {
			t.Errorf("custom Applies(%q) = %v", p, got)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for name, yml := range map[string]string{
		"yaml":      "extends: [",
		"extends":   "message: hi\n",
		"unknown":   "extends: spelling\n",
		"regex":     "extends: existence\ntokens: ['(']\n",
		"level":     "extends: existence\ntokens: [x]\nlevel: fatal\n",
		"glob":      "extends: existence\ntokens: [x]\nfiles: ['[']\n",
		"occurence": "extends: occurrence\ntoken: x\n",
	} {
		r := ParseRule("t", SourceUser, "", []byte(yml))
		if r.Err == "" {
			t.Errorf("%s: no error", name)
		}
		if res := RunInline(r, NewDoc("a.md", []string{"x"})); res.Status != StatusError || res.Err == "" {
			t.Errorf("%s: result %+v", name, res)
		}
	}
}

func TestLibraryOverlay(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("filler-intensifiers.yml", "extends: existence\nname: Mine\ntokens: [quite]\n")
	write("broken.yml", "extends: [")
	write("notes.txt", "ignored")
	lib := Load(Dir{Path: dir, Source: SourceUser})
	byID := map[string]*Rule{}
	for _, r := range lib.Rules {
		byID[r.ID] = r
	}
	if r := byID["filler-intensifiers"]; r == nil || r.Name != "Mine" || r.Source != SourceUser {
		t.Errorf("overlay didn't replace the built-in: %+v", r)
	}
	if r := byID["weasel-words"]; r == nil || r.Source != SourceBuiltin {
		t.Errorf("built-in missing: %+v", r)
	}
	if r := byID["broken"]; r == nil || r.Err == "" {
		t.Errorf("broken rule should load errored: %+v", r)
	}
	if _, ok := byID["notes"]; ok {
		t.Error("non-YAML file loaded")
	}
	// Editing a rule changes the hash; the stamp moves too.
	h, st := lib.Hash, Stamp(dir)
	time.Sleep(10 * time.Millisecond)
	write("broken.yml", "extends: existence\ntokens: [x]\n")
	if Load(Dir{Path: dir, Source: SourceUser}).Hash == h {
		t.Error("hash unchanged after edit")
	}
	if Stamp(dir) == st {
		t.Error("stamp unchanged after edit")
	}
	if Load(Dir{Path: filepath.Join(dir, "missing"), Source: SourceUser}).Hash == "" {
		t.Error("missing dir should still load built-ins")
	}
}

func TestBuiltinsParse(t *testing.T) {
	lib := Load()
	if len(lib.Rules) == 0 {
		t.Fatal("no built-ins")
	}
	for _, r := range lib.Rules {
		if r.Err != "" {
			t.Errorf("built-in %s: %s", r.ID, r.Err)
		}
	}
}

func TestCommandRefusedFromRepo(t *testing.T) {
	r := ParseRule("t", SourceRepo, "", []byte("extends: command\ncommand: [echo]\n"))
	if r.Err == "" || !strings.Contains(r.Err, "own checks folder") {
		t.Fatalf("repo command not refused: %q", r.Err)
	}
	res := RunCommand(context.Background(), r, "/x", nil)
	if res.Status != StatusError {
		t.Errorf("ran a repo command: %+v", res)
	}
}

func script(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	p := filepath.Join(t.TempDir(), "check.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCommand(t *testing.T) {
	// Reads stdin, gets the path as its last argument, prints JSON.
	sh := script(t, `cat >/dev/null; printf '[{"line":2,"from_col":1,"to_col":3,"message":"m %s"}]' "$1"`)
	r := rule(t, "extends: command\nlevel: error\ncommand: ['"+sh+"']\n")
	res := RunCommand(context.Background(), r, "/abs/doc.md", []byte("hi"))
	if res.Status != StatusFail || len(res.Findings) != 1 {
		t.Fatalf("result %+v", res)
	}
	if f := res.Findings[0]; f.Line != 2 || f.EndLine != 2 || f.FromCol != 1 || f.ToCol != 3 || f.Message != "m /abs/doc.md" || f.Severity != "error" {
		t.Errorf("finding %+v", f)
	}

	empty := rule(t, "extends: command\ncommand: ['"+script(t, "exit 0")+"']\n")
	if res := RunCommand(context.Background(), empty, "/x", nil); res.Status != StatusPass {
		t.Errorf("no output should pass: %+v", res)
	}
	for name, body := range map[string]string{
		"exit":    "echo boom >&2; exit 3",
		"badjson": "echo not json",
	} {
		r := rule(t, "extends: command\ncommand: ['"+script(t, body)+"']\n")
		if res := RunCommand(context.Background(), r, "/x", nil); res.Status != StatusError {
			t.Errorf("%s: %+v", name, res)
		}
	}
	slow := rule(t, "extends: command\ntimeout: 200ms\ncommand: ['"+script(t, "sleep 5")+"']\n")
	start := time.Now()
	res = RunCommand(context.Background(), slow, "/x", nil)
	if res.Status != StatusError || !strings.Contains(res.Err, "timed out") {
		t.Errorf("timeout: %+v", res)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("timeout took %s", time.Since(start))
	}
}

func TestCommandRelativeToRuleFile(t *testing.T) {
	sh := script(t, `echo '[]'`)
	r := ParseRule("t", SourceUser, filepath.Join(filepath.Dir(sh), "t.yml"), []byte("extends: command\ncommand: ['./check.sh']\n"))
	if res := RunCommand(context.Background(), r, "/x", nil); res.Status != StatusPass {
		t.Errorf("relative argv[0]: %+v", res)
	}
}

func TestParseValeJSON(t *testing.T) {
	data, err := os.ReadFile("testdata/vale-output.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseValeJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Name != "Demo.Weasel" || res[0].Status != StatusFail {
		t.Fatalf("results %+v", res)
	}
	// vale-doc.md line 3 is "This is ✨ very good."; vale Span [11,14] → [10,14),
	// the same columns RunInline computes for the same text.
	if f := res[0].Findings[0]; f.Line != 3 || f.FromCol != 10 || f.ToCol != 14 || f.Match != "very" {
		t.Errorf("finding %+v", f)
	}
	if _, err := ParseValeJSON([]byte("nope")); err == nil {
		t.Error("bad JSON accepted")
	}
}
