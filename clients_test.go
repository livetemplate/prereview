package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallClientWritesEveryTarget asserts each registered client installs
// all of its files, to the registry's exact relative paths, with the embedded
// content. Guards against a target silently writing nothing or to a wrong path.
func TestInstallClientWritesEveryTarget(t *testing.T) {
	for _, target := range clientTargets() {
		t.Run(target.id, func(t *testing.T) {
			home := t.TempDir()
			paths, err := installClient(home, target.id)
			if err != nil {
				t.Fatalf("installClient(%q): %v", target.id, err)
			}
			if len(paths) != len(target.files) {
				t.Fatalf("wrote %d paths, want %d", len(paths), len(target.files))
			}
			for i, f := range target.files {
				want := filepath.Join(home, filepath.FromSlash(f.rel))
				if paths[i] != want {
					t.Errorf("path[%d] = %q, want %q", i, paths[i], want)
				}
				got, err := os.ReadFile(want)
				if err != nil {
					t.Fatalf("read %s: %v", want, err)
				}
				if string(got) != f.body {
					t.Errorf("%s content does not match the embedded body", f.rel)
				}
				if len(got) == 0 {
					t.Errorf("%s is empty", f.rel)
				}
			}
		})
	}
}

// TestInstallClientCodexWritesBothDirs pins the dual-write that works around
// Codex's unresolved skills-directory migration (~/.codex/skills vs
// ~/.agents/skills). If either disappears, discovery silently breaks on some
// Codex versions.
func TestInstallClientCodexWritesBothDirs(t *testing.T) {
	home := t.TempDir()
	if _, err := installClient(home, "codex"); err != nil {
		t.Fatalf("installClient(codex): %v", err)
	}
	for _, rel := range []string{
		".codex/skills/prereview/SKILL.md",
		".agents/skills/prereview/SKILL.md",
	} {
		if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected codex to write %s: %v", rel, err)
		}
	}
}

// TestInstallClientIdempotent: re-running upgrades in place (no error).
func TestInstallClientIdempotent(t *testing.T) {
	home := t.TempDir()
	for _, id := range clientIDs() {
		if _, err := installClient(home, id); err != nil {
			t.Fatalf("installClient(%q) first run: %v", id, err)
		}
		if _, err := installClient(home, id); err != nil {
			t.Fatalf("installClient(%q) second run: %v", id, err)
		}
	}
}

func TestInstallClientUnknown(t *testing.T) {
	if _, err := installClient(t.TempDir(), "bogus"); err == nil {
		t.Fatal("expected error for unknown client")
	}
}

func TestParseClientSelection(t *testing.T) {
	targets := clientTargets()
	all := clientIDs()
	cases := []struct {
		name, in string
		want     []string
		wantErr  bool
	}{
		{"empty defaults to claude", "", []string{"claude"}, false},
		{"whitespace defaults to claude", "   ", []string{"claude"}, false},
		{"all keyword", "all", all, false},
		{"all uppercase", "ALL", all, false},
		{"single index", "3", []string{"gemini"}, false},
		{"comma list", "2,3", []string{"codex", "gemini"}, false},
		{"space list", "2 3", []string{"codex", "gemini"}, false},
		{"out-of-registry order normalizes", "3,2", []string{"codex", "gemini"}, false},
		{"dedupes", "2,2,3", []string{"codex", "gemini"}, false},
		{"zero is invalid", "0", nil, true},
		{"too large is invalid", "99", nil, true},
		{"non-numeric is invalid", "claude", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseClientSelection(c.in, targets)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", c.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseClientSelection(%q): %v", c.in, err)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("parseClientSelection(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestSelectClients(t *testing.T) {
	// A real choice.
	got, err := selectClients(strings.NewReader("3\n"), io.Discard)
	if err != nil {
		t.Fatalf("selectClients: %v", err)
	}
	if strings.Join(got, ",") != "gemini" {
		t.Errorf("got %v, want [gemini]", got)
	}
	// EOF / no input → default to claude (preserves historical behavior of a
	// bare `--install-skill`, e.g. when stdin is a non-interactive pipe).
	got, err = selectClients(strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatalf("selectClients EOF: %v", err)
	}
	if strings.Join(got, ",") != "claude" {
		t.Errorf("EOF got %v, want [claude]", got)
	}
}

func TestResolveClients(t *testing.T) {
	got, err := resolveClients("gemini, codex ,gemini")
	if err != nil {
		t.Fatalf("resolveClients: %v", err)
	}
	if strings.Join(got, ",") != "gemini,codex" {
		t.Errorf("resolveClients dedupe/trim = %v, want [gemini codex]", got)
	}
	if _, err := resolveClients("nope"); err == nil {
		t.Fatal("expected error for unknown client in --client")
	}
}

// TestYAMLFrontmatterDescriptionQuoted is a regression guard for a real bug
// the codex smoke test caught: clientTrigger contains ": " (a colon), and an
// unquoted YAML `description: a: b` parses as a nested mapping, so Codex (and
// any YAML-frontmatter loader) rejected the whole skill. Every YAML-frontmatter
// client must quote the description value.
func TestYAMLFrontmatterDescriptionQuoted(t *testing.T) {
	if !strings.Contains(clientTrigger, ": ") {
		t.Fatal("precondition: clientTrigger should contain a colon for this guard to be meaningful")
	}
	home := t.TempDir()
	// id → relative path of the file whose frontmatter carries the description.
	yamlClients := map[string]string{
		"codex":    ".codex/skills/prereview/SKILL.md",
		"opencode": ".config/opencode/commands/prereview.md",
		"cursor":   ".cursor/rules/prereview.mdc",
	}
	for id, rel := range yamlClients {
		if _, err := installClient(home, id); err != nil {
			t.Fatalf("installClient(%q): %v", id, err)
		}
		got, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		var descLine string
		for _, line := range strings.Split(string(got), "\n") {
			if strings.HasPrefix(line, "description:") {
				descLine = line
				break
			}
		}
		if descLine == "" {
			t.Errorf("%s: no description line found", id)
			continue
		}
		// The value (after "description:") must be a quoted scalar so embedded
		// colons don't break YAML.
		val := strings.TrimSpace(strings.TrimPrefix(descLine, "description:"))
		if !strings.HasPrefix(val, `"`) || !strings.HasSuffix(val, `"`) {
			t.Errorf("%s: description must be a quoted YAML scalar, got: %s", id, descLine)
		}
	}
}

// TestAiderWrapper guards the fix for a real bug the keyless smoke test caught:
// aider's `/code` in a `--load` script is skipped non-interactively and never
// applies edits. The integration must instead be an executable wrapper that
// uses `--message` (which does apply edits headlessly).
func TestAiderWrapper(t *testing.T) {
	home := t.TempDir()
	paths, err := installClient(home, "aider")
	if err != nil {
		t.Fatalf("installClient(aider): %v", err)
	}
	p := paths[0]
	if filepath.Base(p) != "prereview-aider.sh" {
		t.Errorf("aider artifact should be prereview-aider.sh, got %s", p)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("aider wrapper must be executable, got mode %v", info.Mode().Perm())
	}
	body, _ := os.ReadFile(p)
	s := string(body)
	if !strings.HasPrefix(s, "#!/bin/sh") {
		t.Errorf("wrapper must start with a shebang, got:\n%.40s", s)
	}
	if !strings.Contains(s, "--message") {
		t.Errorf("wrapper must use --message (applies edits headlessly), got:\n%s", s)
	}
	if strings.Contains(s, "/code") || strings.Contains(s, "--load") {
		t.Errorf("wrapper must NOT use --load//code (skipped non-interactively), got:\n%s", s)
	}
}

func TestYAMLQuote(t *testing.T) {
	cases := map[string]string{
		`plain`:      `"plain"`,
		`has: colon`: `"has: colon"`,
		`a "quote"`:  `"a \"quote\""`,
		`back\slash`: `"back\\slash"`,
	}
	for in, want := range cases {
		if got := yamlQuote(in); got != want {
			t.Errorf("yamlQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestClientContentShape pins the format-specific scaffolding each agent needs,
// so a wrapper change that breaks the file format is caught here.
func TestClientContentShape(t *testing.T) {
	home := t.TempDir()
	if _, err := installClient(home, "gemini"); err != nil {
		t.Fatal(err)
	}
	toml, _ := os.ReadFile(filepath.Join(home, ".gemini/commands/prereview.toml"))
	if !strings.Contains(string(toml), "description = ") || !strings.Contains(string(toml), `prompt = """`) {
		t.Errorf("gemini TOML missing required keys:\n%s", toml)
	}

	for _, tc := range []struct{ id, rel, prefix string }{
		{"codex", ".codex/skills/prereview/SKILL.md", "---\nname: prereview"},
		{"opencode", ".config/opencode/commands/prereview.md", "---\ndescription:"},
		{"cursor", ".cursor/rules/prereview.mdc", "---\ndescription:"},
	} {
		if _, err := installClient(home, tc.id); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(home, filepath.FromSlash(tc.rel)))
		if !strings.HasPrefix(string(got), tc.prefix) {
			t.Errorf("%s should start with %q, got:\n%.60s", tc.id, tc.prefix, got)
		}
	}
}
