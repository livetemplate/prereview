package checks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed builtin/*.yml
var builtinFS embed.FS

// Dir is one folder of rule files and the trust it carries (Source).
type Dir struct {
	Path   string
	Source string // SourceUser or SourceRepo
}

// Library is the loaded set of rules, sorted by name. Hash covers every rule
// file's name and content, so a cache keyed on it reruns checks after any edit.
type Library struct {
	Rules []*Rule
	Hash  string
}

// Load reads the built-in rules, then each dir in order; a later file with the
// same ID replaces an earlier one (so a user file overrides a built-in). A missing
// dir is skipped and an unreadable or invalid file becomes an errored rule: a rule
// library must never break a review.
func Load(dirs ...Dir) *Library {
	byID := map[string]*Rule{}
	h := sha256.New()
	add := func(name, source, p string, data []byte) {
		id := ruleID(name)
		if strings.HasSuffix(name, ".md") {
			// Judges never run here, so they stay out of the hash: editing a
			// rubric must not invalidate every checker result (RubricHash
			// versions each judge on its own).
			byID[id] = ParseJudge(id, source, p, data)
			return
		}
		h.Write([]byte(source + "\x00" + p + "\x00" + name + "\x00"))
		h.Write(data)
		h.Write([]byte{0})
		byID[id] = ParseRule(id, source, p, data)
	}
	entries, _ := fs.ReadDir(builtinFS, "builtin")
	for _, e := range entries {
		data, _ := builtinFS.ReadFile("builtin/" + e.Name()) // just-listed → readable
		add(e.Name(), SourceBuiltin, "", data)
	}
	for _, d := range dirs {
		if d.Path == "" {
			continue
		}
		entries, err := os.ReadDir(d.Path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isRuleFile(e.Name()) {
				continue
			}
			p := filepath.Join(d.Path, e.Name())
			data, err := os.ReadFile(p)
			if err != nil {
				id := ruleID(e.Name())
				byID[id] = &Rule{ID: id, Name: id, Source: d.Source, Path: p, Err: "unreadable: " + err.Error()}
				continue
			}
			add(e.Name(), d.Source, p, data)
		}
	}
	lib := &Library{Hash: hex.EncodeToString(h.Sum(nil))[:16]}
	for _, r := range byID {
		lib.Rules = append(lib.Rules, r)
	}
	sort.Slice(lib.Rules, func(i, j int) bool {
		if lib.Rules[i].Name != lib.Rules[j].Name {
			return lib.Rules[i].Name < lib.Rules[j].Name
		}
		return lib.Rules[i].ID < lib.Rules[j].ID
	})
	return lib
}

func isRuleFile(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".md")
}

// Builtin returns a built-in rule file's bytes, for "copy to my folder".
func Builtin(id string) ([]byte, bool) {
	b, err := builtinFS.ReadFile("builtin/" + id + ".yml")
	return b, err == nil
}

// Stamp summarises a dir's rule files (name, size, mtime) so a poller can tell
// cheaply whether to reload. Empty for a missing dir.
func Stamp(dir string) string {
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || !isRuleFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		b.WriteString(e.Name())
		b.WriteByte(':')
		b.WriteString(info.ModTime().UTC().Format("20060102150405.000000000"))
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(info.Size(), 10))
		b.WriteByte('\n')
	}
	return b.String()
}

// RunAll runs every rule that applies to rel (a path relative to repo) and, when
// runVale (the user trusts this repo's Vale config), the repo has one and vale is
// installed, vale too — synchronously, for
// the CLI. lines is the file's text. The server splits this instead: data rules
// inline, commands and vale in the background.
func (lib *Library) RunAll(ctx context.Context, repo, rel string, lines []string, runVale bool) []Result {
	var out []Result
	abs := filepath.Join(repo, rel)
	content := []byte(strings.Join(lines, "\n") + "\n")
	doc := NewDoc(rel, lines)
	for _, r := range lib.Rules {
		switch {
		case !r.Applies(rel), r.IsJudge(): // judges are the agent's to answer
		case r.Async():
			out = append(out, RunCommand(ctx, r, abs, content))
		default:
			out = append(out, RunInline(r, doc))
		}
	}
	if runVale && ValeConfig(repo) != "" {
		if bin := ValeBinary(); bin != "" {
			out = append(out, RunVale(ctx, bin, repo, rel)...)
		}
	}
	return out
}

// ErrBinary is ReadLines' answer for a file that isn't text.
var ErrBinary = errors.New("binary file")

// ReadLines reads a file as the diff view's new-side lines: split on "\n" after
// dropping the final newline (gitdiff's split), so a finding's columns land where
// the view draws them. The CLI and the Checks page both read through it. A file
// with a NUL byte is binary (gitdiff's rule) and returns ErrBinary.
func ReadLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, ErrBinary
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), nil
}
