package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/library"
)

func TestSync_Lifecycle(t *testing.T) {
	home := t.TempDir()
	env := Env{LibDir: t.TempDir(), Home: home}
	mustMkdir(t, filepath.Join(home, ".claude", "skills")) // Claude is set up; nothing else is
	l := acc("l1", "Cut filler words.", library.DefaultScope, "2026-11-01T00:00:00Z")
	l.Checker = "extends: existence\nname: Filler\ntokens: [basically]\n"
	ls := []library.Learning{l}

	rep, err := Sync(env, ls)
	if err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(home, ".claude", "skills", WritingID, "SKILL.md")
	check := filepath.Join(env.LearnedDir(), "learned-l1.yml")
	if !strings.Contains(read(t, claude), "Cut filler words.") || read(t, check) != l.Checker || read(t, env.SourcePath(WritingID)) != read(t, claude) {
		t.Fatalf("first sync didn't install the skill and its checker: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents")); !os.IsNotExist(err) {
		t.Fatal("created ~/.agents for an agent that isn't set up")
	}
	states := map[string]string{}
	for _, in := range rep.Skills[0].Installs {
		states[in.Target.ID] = in.State
	}
	if states["claude"] != Installed || states["agents"] != NotSetUp || states["cursor"] != NotSetUp {
		t.Fatalf("install states = %v", states)
	}
	if rep, _ := Sync(env, ls); len(rep.Changed) != 0 {
		t.Fatalf("a second sync with nothing new changed %v", rep.Changed)
	}

	// Your edit to the source stops regeneration; the install follows it.
	mustWrite(t, env.SourcePath(WritingID), read(t, env.SourcePath(WritingID))+"- My own line.\n")
	ls[0].Text = "Cut every filler word."
	rep, _ = Sync(env, ls)
	if !rep.Skills[0].Edited || strings.Contains(read(t, claude), "every filler") || !strings.Contains(read(t, claude), "My own line.") {
		t.Fatalf("an edited source: edited=%v\n%s", rep.Skills[0].Edited, read(t, claude))
	}
	if err := Regenerate(env, ls, WritingID); err != nil {
		t.Fatal(err)
	}
	if rep, _ = Sync(env, ls); rep.Skills[0].Edited || !strings.Contains(read(t, claude), "Cut every filler word.") || strings.Contains(read(t, claude), "My own line.") {
		t.Fatalf("Regenerate didn't take the learnings back:\n%s", read(t, claude))
	}

	// An edit in the agent's folder is left alone.
	mustWrite(t, claude, "hand-tuned in place\n")
	ls[0].Text = "Cut fillers."
	rep, _ = Sync(env, ls)
	if read(t, claude) != "hand-tuned in place\n" || rep.Skills[0].Installs[0].State != EditedThere {
		t.Fatalf("overwrote an edited install: %q", read(t, claude))
	}

	// Rejecting the only learning removes what it produced (where unchanged).
	ls[0].Status = library.StatusRejected
	rep, _ = Sync(env, ls)
	if _, err := os.Stat(check); !os.IsNotExist(err) {
		t.Fatal("the rejected learning's checker is still in the checks folder")
	}
	if _, err := os.Stat(env.SourcePath(WritingID)); !os.IsNotExist(err) {
		t.Fatal("the empty Writing skill's source is still there")
	}
	if len(rep.Skills) != 0 {
		t.Fatalf("skills after rejecting everything: %+v", rep.Skills)
	}
	if read(t, claude) != "hand-tuned in place\n" {
		t.Fatal("removing the skill deleted your edited copy of it")
	}
}

func TestSync_ForeignSkillNameAndCursor(t *testing.T) {
	home := t.TempDir()
	env := Env{LibDir: t.TempDir(), Home: home}
	theirs := filepath.Join(home, ".agents", "skills", WritingID, "SKILL.md")
	mustMkdir(t, filepath.Dir(theirs))
	mustWrite(t, theirs, "someone else's\n")
	mustMkdir(t, filepath.Join(home, ".cursor", "rules"))
	l := acc("l1", "Number the steps.", library.DefaultScope, "2026-11-01T00:00:00Z")
	l.Globs = []string{"docs/**"}
	rep, err := Sync(env, []library.Learning{l})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, theirs) != "someone else's\n" {
		t.Fatal("overwrote a skill prereview didn't write")
	}
	mdc := read(t, filepath.Join(home, ".cursor", "rules", WritingID+".mdc"))
	if !strings.HasPrefix(mdc, "---\ndescription: ") || strings.Contains(mdc, "globs:") || strings.Contains(mdc, "name: learned-writing") || !strings.Contains(mdc, "- Number the steps. (in docs/**)") {
		t.Fatalf("cursor rule:\n%s", mdc)
	}
	for _, in := range rep.Skills[0].Installs {
		if in.Target.ID == "agents" && in.State != Foreign {
			t.Fatalf("agents install state = %q", in.State)
		}
	}
}

// The sweep only removes what a sync generates: another owned file (say, a
// future owner of prereview's own skill) is never touched.
func TestSync_SweepOnlyGenerated(t *testing.T) {
	home := t.TempDir()
	env := Env{LibDir: t.TempDir(), Home: home}
	root := filepath.Join(home, ".claude", "skills")
	other := filepath.Join(root, "prereview", "SKILL.md")
	mustMkdir(t, root)
	if err := Own(env.LibDir, func(o *Owner) error { _, err := o.Write(root, other, []byte("prereview's own")); return err }); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(env, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, other) != "prereview's own" {
		t.Fatal("a learnings sync removed a file it doesn't generate")
	}
}
