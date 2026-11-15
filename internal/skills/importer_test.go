package skills

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/library"
)

func TestDiscover_DedupesAndSkipsOurs(t *testing.T) {
	home := t.TempDir()
	env := Env{LibDir: t.TempDir(), Home: home}
	house := "---\nname: house-style\ndescription: \"Our style\"\n---\n\nBe brief.\n"
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		mustMkdir(t, filepath.Join(home, root, "house-style"))
		mustWrite(t, filepath.Join(home, root, "house-style", "SKILL.md"), house)
		mustMkdir(t, filepath.Join(home, root, "prereview"))
		mustWrite(t, filepath.Join(home, root, "prereview", "SKILL.md"), "ours")
	}
	mustMkdir(t, filepath.Join(home, ".claude/skills/house-style/evals"))
	if _, err := Sync(env, []library.Learning{acc("l1", "Plain words.", library.DefaultScope, "2026-11-01T00:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	found := Discover(env)
	if len(found) != 1 || found[0].Name != "house-style" || found[0].Description != "Our style" || len(found[0].Paths) != 2 || !found[0].HasEvals {
		t.Fatalf("found = %+v", found)
	}
}

// TestOverlay: learnings scoped to an imported skill stay out of the generated
// skills, the skill's folder is never written, and Export patch applies cleanly.
func TestOverlay_PatchAppliesAndFolderUntouched(t *testing.T) {
	for _, body := range []string{"---\nname: house-style\n---\n\nBe brief.\n", "---\nname: house-style\n---\n\nBe brief."} {
		home := t.TempDir()
		env := Env{LibDir: t.TempDir(), Home: home}
		dir := filepath.Join(home, ".claude/skills/house-style")
		mustMkdir(t, dir)
		mustWrite(t, filepath.Join(dir, "SKILL.md"), body)
		f := Discover(env)[0]
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(ImportSkill(env.LibDir, f))
		ls := []library.Learning{
			acc("o1", "Lead with the change.", "house-style", "2026-11-01T00:00:00Z"),
			acc("w1", "Plain words.", library.DefaultScope, "2026-11-02T00:00:00Z"),
		}
		rep, err := Sync(env, ls)
		must(err)
		if read(t, filepath.Join(dir, "SKILL.md")) != body {
			t.Fatal("sync wrote into an imported skill's folder")
		}
		if len(rep.Skills) != 1 || len(rep.Skills[0].Lines) != 1 || rep.Skills[0].Lines[0].LearningID != "w1" {
			t.Fatalf("the overlay learning leaked into a generated skill: %+v", rep.Skills)
		}
		imp := Imports(env.LibDir)[0]
		if imp.Changed() {
			t.Fatal("reported changed right after import")
		}
		patch, err := OverlayPatch(imp, Overlay(ls, "house-style"))
		must(err)
		pf := filepath.Join(t.TempDir(), "o.patch")
		mustWrite(t, pf, patch)
		if out, err := exec.Command("git", "-C", dir, "apply", pf).CombinedOutput(); err != nil {
			t.Fatalf("git apply: %v %s\n%s", err, out, patch)
		}
		got := read(t, filepath.Join(dir, "SKILL.md"))
		if !strings.HasPrefix(got, strings.TrimSuffix(body, "\n")) || !strings.HasSuffix(got, "## Learned preferences\n\nLearned from your own review comments in prereview:\n\n- Lead with the change.\n") {
			t.Fatalf("patched SKILL.md:\n%s", got)
		}
		if !imp.Changed() {
			t.Fatal("an edited skill isn't reported changed since import")
		}
		_ = os.Remove(pf)
	}
}

func TestImportedChecks_MapsDocumentGraders(t *testing.T) {
	home := t.TempDir()
	env := Env{LibDir: t.TempDir(), ChecksDir: filepath.Join(t.TempDir(), "checks"), Home: home}
	dir := filepath.Join(home, ".claude/skills/house-style")
	g := func(c, name, body string) {
		mustMkdir(t, filepath.Join(dir, "evals", c, "graders"))
		mustWrite(t, filepath.Join(dir, "evals", c, "graders", name+".md"), body)
	}
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, "SKILL.md"), "---\nname: house-style\n---\nBe brief.\n")
	g("notes", "reader-first", "---\ntype: llm\n---\n\nPASS if it leads with what changed for the reader.\n")
	g("notes", "no-utilize", "---\ntype: regex\npattern: utilize\nflags: i\nmatch: not_contains\n---\n")
	g("notes", "says-fetch", "---\ntype: regex\npattern: fetchUser\n---\n")
	g("notes", "ran-tests", "---\ntype: tool_used\ntool: Bash\n---\n")
	f := Discover(env)[0]
	if err := ImportSkill(env.LibDir, f); err != nil {
		t.Fatal(err)
	}
	ics, skipped := ImportedChecks(env, Imports(env.LibDir)[0])
	if len(ics) != 2 || !ics[0].Judge && !ics[1].Judge {
		t.Fatalf("mapped %+v", ics)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped %+v, want the must-contain regex and the tool_used grader", skipped)
	}
	if _, err := Sync(env, nil); err != nil {
		t.Fatal(err)
	}
	for _, ic := range ics {
		if read(t, filepath.Join(env.ChecksDir, ic.File())) != ic.Body {
			t.Fatalf("%s wasn't written to the checks folder", ic.File())
		}
	}
	if err := ForgetImport(env.LibDir, "house-style"); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(env, nil); err != nil {
		t.Fatal(err)
	}
	for _, ic := range ics {
		if _, err := os.Stat(filepath.Join(env.ChecksDir, ic.File())); !os.IsNotExist(err) {
			t.Fatalf("%s outlived the import", ic.File())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "evals", "notes", "graders", "reader-first.md")); err != nil {
		t.Fatal("forgetting the import touched the skill's folder")
	}
}
