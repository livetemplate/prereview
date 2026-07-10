package main

import (
	"bufio"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// body.md is the shared, agent-neutral one-shot workflow. Each non-Claude
// client wraps it in its own command/skill/rules file format. Claude Code is
// the exception: it gets the full streaming SKILL.md (skillMD), not this body.
//
//go:embed skill/clients/body.md
var clientBody string

// clientTrigger is the one-line "when to use this" description shared by every
// client's command/skill metadata. Phrased as the trigger condition because
// most agents match on exactly this string to decide whether to invoke.
const clientTrigger = "Use when the user wants to review their working tree (or a file) and have you apply the fixes: prereview opens a browser review UI, the user queues comments, then you read them (prereview comments --json) and apply the still-open ones."

// clientFile is one file an installer writes, at a path relative to $HOME.
// rel always uses forward slashes; it is converted per-OS at write time.
// mode is the file permission (0 means default 0644); executable helpers set 0755.
type clientFile struct {
	rel  string
	body string
	mode os.FileMode
}

// clientTarget is an installable agent integration.
type clientTarget struct {
	id    string // stable --client value, e.g. "codex"
	label string // human label shown in the selector / install output
	hint  string // post-install invocation hint
	files []clientFile
}

// clientTargets is the single source of truth for which agents prereview can
// install into and where. The docs (docs/integrations.md) and the README table
// mirror this list — keep them in sync.
func clientTargets() []clientTarget {
	body := strings.TrimSpace(clientBody)
	return []clientTarget{
		{
			id:    "claude",
			label: "Claude Code (streaming, multi-round)",
			hint:  `Invoke with /prereview (or "review my changes"). If unknown, run /reload.`,
			// Claude gets the real streaming skill, not the one-shot body.
			// SKILL.md must come first: installSkill returns files[0].
			files: []clientFile{
				{rel: ".claude/skills/prereview/SKILL.md", body: skillMD},
				{rel: ".claude/skills/prereview/reference.md", body: skillReferenceMD},
			},
		},
		{
			id:    "codex",
			label: "OpenAI Codex CLI",
			hint:  "Invoke by mentioning $prereview in your message, or via /skills.",
			// Codex is mid-migration between ~/.codex/skills and the
			// agent-agnostic ~/.agents/skills; versions differ on which they
			// scan, so write both. See docs/integrations.md.
			files: []clientFile{
				{rel: ".codex/skills/prereview/SKILL.md", body: frontmatterDoc("prereview", clientTrigger, body)},
				{rel: ".agents/skills/prereview/SKILL.md", body: frontmatterDoc("prereview", clientTrigger, body)},
			},
		},
		{
			id:    "gemini",
			label: "Gemini CLI",
			hint:  "Invoke with /prereview.",
			files: []clientFile{
				{rel: ".gemini/commands/prereview.toml", body: geminiCommand(clientTrigger, body)},
			},
		},
		{
			id:    "opencode",
			label: "opencode",
			hint:  "Invoke with /prereview, or headless: opencode run --command prereview",
			files: []clientFile{
				{rel: ".config/opencode/commands/prereview.md", body: opencodeCommand(clientTrigger, body)},
			},
		},
		{
			id:    "aider",
			label: "aider (one-shot)",
			hint:  "Review with prereview, then: ~/.config/prereview/aider/prereview-aider.sh <files>",
			files: []clientFile{
				{rel: ".config/prereview/aider/prereview-aider.sh", body: aiderScript(), mode: 0o755},
			},
		},
		{
			id:    "cursor",
			label: "cursor-agent",
			hint:  `Invoke: cursor-agent -p --force "use prereview to apply the review comments"`,
			files: []clientFile{
				{rel: ".cursor/rules/prereview.mdc", body: cursorRule(clientTrigger, body)},
			},
		},
	}
}

// clientByID returns the target with the given id, or false.
func clientByID(id string) (clientTarget, bool) {
	for _, t := range clientTargets() {
		if t.id == id {
			return t, true
		}
	}
	return clientTarget{}, false
}

// clientIDs lists every installable id, in registry order.
func clientIDs() []string {
	ts := clientTargets()
	ids := make([]string, len(ts))
	for i, t := range ts {
		ids[i] = t.id
	}
	return ids
}

// unknownClientErr is the shared "no such client" error, listing the known ids.
func unknownClientErr(id string) error {
	return fmt.Errorf("unknown client %q (known: %s)", id, strings.Join(clientIDs(), ", "))
}

// installClient writes every file for one client and returns the paths written.
// MkdirAll + overwrite, so re-running upgrades the integration in place.
func installClient(home, id string) ([]string, error) {
	t, ok := clientByID(id)
	if !ok {
		return nil, unknownClientErr(id)
	}
	var written []string
	for _, f := range t.files {
		path := filepath.Join(home, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return written, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(path, []byte(f.body), mode); err != nil {
			return written, fmt.Errorf("write %s: %w", path, err)
		}
		written = append(written, path)
	}
	return written, nil
}

// resolveClients turns the --client flag into a validated, deduped id list. An
// empty flag opens the interactive menu (selectClients); a non-empty flag is a
// comma-separated list of ids validated against the registry.
func resolveClients(flagVal string) ([]string, error) {
	if strings.TrimSpace(flagVal) == "" {
		return selectClients(os.Stdin, os.Stdout)
	}
	var ids []string
	seen := map[string]bool{}
	for _, tok := range strings.Split(flagVal, ",") {
		id := strings.TrimSpace(tok)
		if id == "" {
			continue
		}
		if _, ok := clientByID(id); !ok {
			return nil, unknownClientErr(id)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// selectClients renders an interactive menu of all clients and parses the
// chosen ids. Empty input (or no input at all, e.g. a non-interactive pipe)
// defaults to Claude Code, preserving the historical `--install-skill`
// behavior. "all" picks every client.
func selectClients(in io.Reader, out io.Writer) ([]string, error) {
	targets := clientTargets()
	fmt.Fprintln(out, "Install the prereview integration for which agent(s)?")
	for i, t := range targets {
		fmt.Fprintf(out, "  %d) %s\n", i+1, t.label)
	}
	fmt.Fprint(out, "Enter numbers (comma-separated), 'all', or Enter for Claude Code: ")

	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return []string{"claude"}, nil // EOF / no input → default
	}
	return parseClientSelection(sc.Text(), targets)
}

// parseClientSelection turns a menu answer into a deduped, registry-ordered id
// list. Accepts "all", or comma/space-separated 1-based indices.
func parseClientSelection(line string, targets []clientTarget) ([]string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return []string{"claude"}, nil
	}
	if strings.EqualFold(line, "all") {
		return clientIDs(), nil
	}
	picked := make(map[int]bool)
	for _, tok := range strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' }) {
		n, err := strconv.Atoi(tok)
		if err != nil || n < 1 || n > len(targets) {
			return nil, fmt.Errorf("invalid choice %q (pick 1-%d, comma-separated, or 'all')", tok, len(targets))
		}
		picked[n] = true
	}
	var ids []string
	for i, t := range targets { // registry order, deduped
		if picked[i+1] {
			ids = append(ids, t.id)
		}
	}
	return ids, nil
}

// --- per-format wrappers ---------------------------------------------------

// yamlQuote renders s as a YAML double-quoted scalar. Required for any value
// that may contain a colon: an unquoted `description: a: b` parses as a nested
// mapping and the whole frontmatter fails to load (Codex rejected exactly this).
func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// frontmatterDoc renders a markdown doc with a name+description YAML header,
// the shape Codex skills and other markdown-skill formats expect.
func frontmatterDoc(name, desc, body string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n# prereview\n\n%s\n", name, yamlQuote(desc), body)
}

// geminiCommand renders a Gemini CLI custom command (TOML; only `description`
// and `prompt` keys exist). The prompt is a triple-quoted string; body.md
// never contains `"""`, so no escaping is needed.
func geminiCommand(desc, body string) string {
	return fmt.Sprintf("description = %q\n\nprompt = \"\"\"\n%s\n\"\"\"\n", desc, body)
}

// opencodeCommand renders an opencode markdown command (frontmatter + body as
// the prompt template). `agent: build` selects opencode's edit-capable agent.
func opencodeCommand(desc, body string) string {
	return fmt.Sprintf("---\ndescription: %s\nagent: build\n---\n\n%s\n", yamlQuote(desc), body)
}

// cursorRule renders a Cursor rule (.mdc). alwaysApply:false + a description
// makes it an "apply intelligently" rule the CLI loads when relevant. Cursor's
// .cursor/commands is an editor-only feature unconfirmed for the CLI, so a rule
// is the reliable surface — see docs/integrations.md.
func cursorRule(desc, body string) string {
	return fmt.Sprintf("---\ndescription: %s\nalwaysApply: false\n---\n\n%s\n", yamlQuote(desc), body)
}

// aiderScript renders an executable wrapper for aider. aider has no stream/watch
// mode and no user-global command registry, so this is a one-shot: review with
// prereview first, then run this script with the files to edit.
//
// It uses `--message` + `--read`, NOT `--load`/`/code`: smoke-testing showed
// `/code` in a `--load` script is skipped in non-interactive mode ("only
// supported in interactive mode") and never applies edits, whereas `--message`
// applies them headlessly. `--read` puts the CSV in context so aider sees every
// comment; the message tells it which rows to act on.
func aiderScript() string {
	return `#!/bin/sh
# prereview -> aider: apply the review comments you left in prereview.
#
# Usage (after queuing your comments in prereview):
#   ~/.config/prereview/aider/prereview-aider.sh <files-to-edit>
#
# Pass the files you commented on. Run from the repo root (where .prereview/ is).
exec aider \
  --read .prereview/comments.csv \
  --message "Apply every comment in .prereview/comments.csv where the resolved column is not true and the anchor_status column is not outdated. Read them all first, then make one coherent change. Use the file column plus from_line/to_line to locate each anchor and the body column for intent. kind=file is whole-file guidance; kind=area or kind=region point at an image or live-page region, not a file edit." \
  --yes --no-auto-commits "$@"
`
}
