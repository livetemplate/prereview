package skills

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/library"
)

// own runs fn against a fresh ownership record in lib.
func own(t *testing.T, lib string, fn func(o *Owner) error) {
	t.Helper()
	if err := Own(lib, fn); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestMemoryBlock: the block is added to a missing file and below your text,
// updated in place, left once you edit it, and removed leaving your text exactly
// as it was.
func TestMemoryBlock(t *testing.T) {
	lib, dir := t.TempDir(), t.TempDir()
	gen := []Skill{{ID: WritingID, Name: "Writing"}, {ID: "learned-docs", Name: "Docs", Extends: WritingID}}
	block := MemoryBlock(gen)
	if !strings.Contains(block, "`learned-writing`: any prose") || !strings.Contains(block, "`learned-docs`: docs (apply `learned-writing` too)") || !strings.Contains(block, "prereview check") {
		t.Fatalf("block:\n%s", block)
	}

	// A missing file is created with the block alone; removing it removes the file.
	fresh := filepath.Join(dir, "fresh.md")
	own(t, lib, func(o *Owner) error { _, err := o.writeBlock(fresh, block); return err })
	if got := readFile(t, fresh); got != block {
		t.Fatalf("fresh file = %q", got)
	}
	own(t, lib, func(o *Owner) error { _, err := o.writeBlock(fresh, ""); return err })
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatal("removing the only block left the file")
	}

	// Your text around the block is never touched.
	mine := "# My rules\n\n- be terse\n"
	p := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(p, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	var changed bool
	own(t, lib, func(o *Owner) (err error) { changed, err = o.writeBlock(p, block); return })
	if got := readFile(t, p); !changed || got != mine+"\n"+block {
		t.Fatalf("added: changed=%v\n%s", changed, got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want the file's own 0600", fi.Mode().Perm())
	}
	own(t, lib, func(o *Owner) (err error) { changed, err = o.writeBlock(p, block); return })
	if changed {
		t.Fatal("writing the same block again changed the file")
	}

	// You add text below it: an update replaces the block alone.
	if err := os.WriteFile(p, []byte(readFile(t, p)+"\n- after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newer := MemoryBlock(gen[:1])
	own(t, lib, func(o *Owner) error { _, err := o.writeBlock(p, newer); return err })
	if got := readFile(t, p); got != mine+"\n"+newer+"\n- after\n" {
		t.Fatalf("updated:\n%s", got)
	}

	// You edit inside the block: it is left, and reported.
	edited := strings.Replace(readFile(t, p), "any prose", "all my prose", 1)
	if err := os.WriteFile(p, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Own(lib, func(o *Owner) error { _, err := o.writeBlock(p, block); return err }); !errors.Is(err, ErrEdited) {
		t.Fatalf("edited block: err = %v, want ErrEdited", err)
	}
	if readFile(t, p) != edited {
		t.Fatal("rewrote a block you edited")
	}

	// Put it back as prereview wrote it; removing leaves your text exactly.
	if err := os.WriteFile(p, []byte(mine+"\n"+newer+"\n- after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	own(t, lib, func(o *Owner) error { _, err := o.writeBlock(p, ""); return err })
	if got := readFile(t, p); got != mine+"\n- after\n" {
		t.Fatalf("removed:\n%q", got)
	}
}

// settingsFixture is a settings.json with hooks and keys of your own.
const settingsFixture = `{
  "hooks": {
    "Notification": [{"matcher": "*", "hooks": [{"type": "command", "command": "notify >/dev/null 2>&1 || true"}]}],
    "PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "log-bash"}]}],
    "Stop": [{"matcher": "*", "hooks": [{"type": "command", "command": "stop"}]}]
  },
  "statusLine": {"type": "command", "command": "line"},
  "enabledPlugins": {"a@b": true},
  "alwaysThinkingEnabled": true,
  "effortLevel": "high",
  "zebra": 1,
  "apple": [1, 2, 3]
}
`

// keysInOrder lists an object's keys as written.
func keysInOrder(t *testing.T, raw []byte) []string {
	t.Helper()
	ms, err := decodeObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	var ks []string
	for _, m := range ms {
		ks = append(ks, m.Key)
	}
	return ks
}

func unmarshal(t *testing.T, raw string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestClaudeHookMerge: the hook entry is added beside your hooks, once, with
// every other key keeping its value and place; removing it gives your settings
// back; an entry you edited, or settings that aren't an object, are left alone.
func TestClaudeHookMerge(t *testing.T) {
	lib, dir := t.TempDir(), t.TempDir()
	p := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(p, []byte(settingsFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	var changed bool
	own(t, lib, func(o *Owner) (err error) { changed, err = o.writeHook(p, true); return })
	got := readFile(t, p)
	if !changed || !strings.Contains(got, HookCommand) {
		t.Fatalf("hook not added:\n%s", got)
	}
	if strings.Contains(got, "\\u003e") || strings.Contains(got, "\\u0026") {
		t.Fatalf("a > was HTML-escaped:\n%s", got)
	}
	if want, have := keysInOrder(t, []byte(settingsFixture)), keysInOrder(t, []byte(got)); !reflect.DeepEqual(want, have) {
		t.Fatalf("top-level keys %v, want %v (same order)", have, want)
	}
	hooks := func(raw string) []member {
		ms, _ := decodeObject([]byte(raw))
		h, _ := decodeObject(ms[find(ms, "hooks")].Val)
		return h
	}
	if ks := []string{}; true {
		for _, m := range hooks(got) {
			ks = append(ks, m.Key)
		}
		if !reflect.DeepEqual(ks, []string{"Notification", "PostToolUse", "Stop"}) {
			t.Fatalf("hook events %v, want them in your order", ks)
		}
	}
	a, b := unmarshal(t, settingsFixture), unmarshal(t, got)
	post := b["hooks"].(map[string]any)["PostToolUse"].([]any)
	if len(post) != 2 || !reflect.DeepEqual(post[0], a["hooks"].(map[string]any)["PostToolUse"].([]any)[0]) {
		t.Fatalf("PostToolUse = %v, want your Bash entry then prereview's", post)
	}
	delete(a, "hooks")
	delete(b, "hooks")
	if !reflect.DeepEqual(a, b) {
		t.Fatal("a key outside hooks changed")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 kept", fi.Mode().Perm())
	}
	if readFile(t, p+".prereview-bak") != settingsFixture {
		t.Fatal("the previous settings weren't kept beside it")
	}

	own(t, lib, func(o *Owner) (err error) { changed, err = o.writeHook(p, true); return })
	if changed || strings.Count(readFile(t, p), HookMarker) != 1 {
		t.Fatal("adding it twice changed the file or added a second entry")
	}

	own(t, lib, func(o *Owner) error { _, err := o.writeHook(p, false); return err })
	if !reflect.DeepEqual(unmarshal(t, readFile(t, p)), unmarshal(t, settingsFixture)) {
		t.Fatalf("removing it didn't give your settings back:\n%s", readFile(t, p))
	}

	// You edit prereview's entry: it is left.
	own(t, lib, func(o *Owner) error { _, err := o.writeHook(p, true); return err })
	edited := strings.Replace(readFile(t, p), `"timeout": 30`, `"timeout": 90`, 1)
	if err := os.WriteFile(p, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{true, false} {
		if err := Own(lib, func(o *Owner) error { _, err := o.writeHook(p, on); return err }); !errors.Is(err, ErrEdited) {
			t.Fatalf("on=%v over an edited entry: err = %v, want ErrEdited", on, err)
		}
	}
	if readFile(t, p) != edited {
		t.Fatal("rewrote an entry you edited")
	}

	// A missing file is created; settings that aren't an object are never written.
	np := filepath.Join(dir, "new.json")
	own(t, lib, func(o *Owner) error { _, err := o.writeHook(np, true); return err })
	if v := unmarshal(t, readFile(t, np)); len(v["hooks"].(map[string]any)["PostToolUse"].([]any)) != 1 {
		t.Fatalf("new settings: %v", v)
	}
	for _, bad := range []string{`[1]`, `{"hooks": []}`, `{"hooks": {"PostToolUse": {}}}`, `{"a": 1} {"b": 2}`, `{bad`} {
		if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Own(lib, func(o *Owner) error { _, err := o.writeHook(p, true); return err }); !errors.Is(err, errSettingsShape) {
			t.Fatalf("%s: err = %v, want errSettingsShape", bad, err)
		}
		if readFile(t, p) != bad {
			t.Fatalf("%s: wrote over settings it couldn't read", bad)
		}
	}
}

// TestSync_AgentPointers: with a generated skill, Sync adds the memory block
// where each agent is set up and the Claude Code hook; with the switch off, or
// once the last learning goes, both are removed. All under a temp agent home.
func TestSync_AgentPointers(t *testing.T) {
	home, lib := t.TempDir(), t.TempDir()
	for _, d := range []string{".claude/skills", ".codex", ".config/opencode"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mine := "# Mine\n"
	claudeMD := filepath.Join(home, ".claude/CLAUDE.md")
	if err := os.WriteFile(claudeMD, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	env := Env{LibDir: lib, Home: home}
	ls := []library.Learning{{ID: "l1", Text: "Cut filler words.", Scope: library.DefaultScope, Status: library.StatusAccepted}}

	rep, err := Sync(env, ls)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, claudeMD); !strings.HasPrefix(got, mine+"\n"+blockStart) || !strings.Contains(got, "`learned-writing`") {
		t.Fatalf("CLAUDE.md:\n%s", got)
	}
	if got := readFile(t, filepath.Join(home, ".codex/AGENTS.md")); !strings.HasPrefix(got, blockStart) {
		t.Fatalf("Codex AGENTS.md:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".config/opencode/AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("created opencode's AGENTS.md (it would hide ~/.claude/CLAUDE.md from opencode)")
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Fatal("created an agent's folder")
	}
	if !strings.Contains(readFile(t, filepath.Join(home, claudeSettings)), HookCommand) {
		t.Fatal("the hook wasn't added")
	}
	states := map[string]string{}
	for _, r := range rep.Pointers {
		states[r.Label] = r.State
	}
	want := map[string]string{"Claude Code memory": Added, "Codex memory": Added, "Gemini CLI memory": NotSetUp, "opencode memory": NotSetUp, "Claude Code checks after each edit": Added}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("pointers %v, want %v", states, want)
	}

	if _, err := Sync(Env{LibDir: lib, Home: home, NoPointers: true}, ls); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, claudeMD); got != mine {
		t.Fatalf("switched off, CLAUDE.md = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex/AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("switched off, the AGENTS.md that held only the block is still there")
	}
	if strings.Contains(readFile(t, filepath.Join(home, claudeSettings)), HookMarker) {
		t.Fatal("switched off, the hook is still there")
	}

	if _, err := Sync(env, ls); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(env, nil); err != nil { // the last learning undone
		t.Fatal(err)
	}
	if readFile(t, claudeMD) != mine || strings.Contains(readFile(t, filepath.Join(home, claudeSettings)), HookMarker) {
		t.Fatal("with no skills left, the block or the hook stayed")
	}
}
