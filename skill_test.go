package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/skills"
)

// libFor is a test library folder beside home: the ownership record has to
// survive between an install and the sync that follows it.
func libFor(home string) string { return filepath.Join(filepath.Dir(home), filepath.Base(home)+"-lib") }

// writeAsPrereview puts body at rel as an older prereview binary would have:
// recorded in the ownership record, so the next sync treats a difference as an
// upgrade, not a hand edit.
func writeAsPrereview(t *testing.T, home, rel, body string) {
	t.Helper()
	err := skills.Own(libFor(home), func(o *skills.Owner) error {
		return o.Put(filepath.Join(home, filepath.FromSlash(rel)), []byte(body), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func syncByClient(rs []skills.ClientSync) map[string]skills.ClientSync {
	m := map[string]skills.ClientSync{}
	for _, r := range rs {
		m[r.Client] = r
	}
	return m
}

// TestSyncInstalledSkills is the #208 contract: every installed integration is
// kept current on launch; nothing is installed uninvited; a hand edit is left
// and reported.
func TestSyncInstalledSkills(t *testing.T) {
	read := func(t *testing.T, home, rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("nothing installed → nothing written", func(t *testing.T) {
		home := t.TempDir()
		if rs := syncInstalledSkills(home, libFor(home)); rs != nil {
			t.Errorf("results = %+v, want none", rs)
		}
		if entries, _ := os.ReadDir(home); len(entries) != 0 {
			t.Errorf("home gained %v", entries)
		}
	})

	t.Run("installed and current → no result", func(t *testing.T) {
		home := t.TempDir()
		for _, id := range clientIDs() {
			if _, err := installClient(home, libFor(home), id); err != nil {
				t.Fatal(err)
			}
		}
		if rs := syncInstalledSkills(home, libFor(home)); rs != nil {
			t.Errorf("results = %+v, want none", rs)
		}
	})

	// Folders other commands or rules share: an existing folder is not an install.
	for _, tc := range []struct{ id, dir, other string }{
		{"gemini", ".gemini/commands", "deploy.toml"},
		{"cursor", ".cursor/rules", "style.mdc"},
		{"opencode", ".config/opencode/commands", "ship.md"},
		{"aider", ".config/prereview", "ui-prefs.json"},
	} {
		t.Run(tc.id+": a shared folder without prereview's file → untouched", func(t *testing.T) {
			home := t.TempDir()
			mustWriteFile(t, filepath.Join(home, filepath.FromSlash(tc.dir), tc.other), []byte("theirs"))
			if rs := syncInstalledSkills(home, libFor(home)); rs != nil {
				t.Errorf("results = %+v, want none", rs)
			}
			target, _ := clientByID(tc.id)
			for _, f := range target.files {
				if _, err := os.Stat(clientPath(home, f)); !os.IsNotExist(err) {
					t.Errorf("%s was created (err=%v)", f.rel, err)
				}
			}
		})
	}

	t.Run("a stale file from an older binary → refreshed, others left", func(t *testing.T) {
		home := t.TempDir()
		if _, err := installClient(home, libFor(home), "codex"); err != nil {
			t.Fatal(err)
		}
		writeAsPrereview(t, home, ".codex/skills/prereview/SKILL.md", "old")
		got := syncByClient(syncInstalledSkills(home, libFor(home)))
		r, ok := got["codex"]
		if !ok || r.State != skills.Refreshed || len(r.Paths) != 1 || !strings.HasSuffix(r.Paths[0], filepath.FromSlash(".codex/skills/prereview/SKILL.md")) {
			t.Fatalf("codex = %+v, want refreshed with the one stale path", r)
		}
		if read(t, home, ".codex/skills/prereview/SKILL.md") == "old" {
			t.Error("stale SKILL.md not rewritten")
		}
		if len(got) != 1 {
			t.Errorf("results = %+v, want only codex", got)
		}
	})

	t.Run("claude: missing reference.md → recreated; ready flag set", func(t *testing.T) {
		home := t.TempDir()
		if _, err := installClient(home, libFor(home), "claude"); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(home, ".claude/skills/prereview/reference.md")); err != nil {
			t.Fatal(err)
		}
		rs := syncInstalledSkills(home, libFor(home))
		if !skills.AnyRefreshed(rs, "claude") {
			t.Fatalf("claude not refreshed: %+v", rs)
		}
		if read(t, home, ".claude/skills/prereview/reference.md") != skillReferenceMD {
			t.Error("reference.md not restored")
		}
	})

	t.Run("codex: a copy whose folder is gone isn't recreated", func(t *testing.T) {
		home := t.TempDir()
		if _, err := installClient(home, libFor(home), "codex"); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(home, ".agents")); err != nil {
			t.Fatal(err)
		}
		if rs := syncInstalledSkills(home, libFor(home)); rs != nil {
			t.Errorf("results = %+v, want none", rs)
		}
		if _, err := os.Stat(filepath.Join(home, ".agents")); !os.IsNotExist(err) {
			t.Errorf(".agents was recreated (err=%v)", err)
		}
	})

	t.Run("a hand-edited file → left byte-identical and reported, with the fix", func(t *testing.T) {
		home := t.TempDir()
		if _, err := installClient(home, libFor(home), "cursor"); err != nil {
			t.Fatal(err)
		}
		rule := filepath.Join(home, ".cursor/rules/prereview.mdc")
		mustWriteFile(t, rule, []byte("my own rule"))
		r := syncByClient(syncInstalledSkills(home, libFor(home)))["cursor"]
		if r.State != skills.EditedThere || r.Fix() != "prereview --install-skill --client=cursor" {
			t.Errorf("cursor = %+v (fix %q), want edited with the install fix", r, r.Fix())
		}
		if read(t, home, ".cursor/rules/prereview.mdc") != "my own rule" {
			t.Error("the edit was overwritten")
		}
		if skills.AnyRefreshed([]skills.ClientSync{r}, "claude") {
			t.Error("an edited cursor rule must not set the Claude ready flag")
		}

		// --install-skill takes this binary's copy back and records it, so the
		// next launch is quiet.
		if _, err := installClient(home, libFor(home), "cursor"); err != nil {
			t.Fatal(err)
		}
		if read(t, home, ".cursor/rules/prereview.mdc") == "my own rule" {
			t.Error("install didn't overwrite the edit")
		}
		if rs := syncInstalledSkills(home, libFor(home)); rs != nil {
			t.Errorf("after install, results = %+v, want none", rs)
		}
	})

	t.Run("an install with no ownership record → adopted once", func(t *testing.T) {
		home := t.TempDir()
		// What every install made before #208 looks like: files, no record.
		mustWriteFile(t, filepath.Join(home, ".claude/skills/prereview/SKILL.md"), []byte("# skill from v0.28"))
		r := syncByClient(syncInstalledSkills(home, libFor(home)))["claude"]
		if r.State != skills.Refreshed {
			t.Fatalf("claude = %+v, want refreshed", r)
		}
		if read(t, home, ".claude/skills/prereview/SKILL.md") != skillMD {
			t.Error("unrecorded SKILL.md not adopted")
		}
		if rs := syncInstalledSkills(home, libFor(home)); rs != nil {
			t.Errorf("second launch results = %+v, want none", rs)
		}
	})

	t.Run("aider's script keeps its executable bit", func(t *testing.T) {
		home := t.TempDir()
		if _, err := installClient(home, libFor(home), "aider"); err != nil {
			t.Fatal(err)
		}
		writeAsPrereview(t, home, ".config/prereview/aider/prereview-aider.sh", "#!/bin/sh\nold\n")
		script := filepath.Join(home, ".config/prereview/aider/prereview-aider.sh")
		if err := os.Chmod(script, 0o755); err != nil {
			t.Fatal(err)
		}
		if r := syncByClient(syncInstalledSkills(home, libFor(home)))["aider"]; r.State != skills.Refreshed {
			t.Fatalf("aider = %+v, want refreshed", r)
		}
		fi, err := os.Stat(script)
		if err != nil || fi.Mode().Perm() != 0o755 {
			t.Errorf("script mode = %v (err %v), want 0755", fi.Mode().Perm(), err)
		}
	})

	t.Run("no library folder → each installed target reported failed", func(t *testing.T) {
		home := t.TempDir()
		mustWriteFile(t, filepath.Join(home, ".gemini/commands/prereview.toml"), []byte("x"))
		r := syncByClient(syncInstalledSkills(home, ""))["gemini"]
		if r.State != skills.Failed || !strings.Contains(r.Status(), "PREREVIEW_LIBRARY_DIR") {
			t.Errorf("gemini = %+v, want failed naming PREREVIEW_LIBRARY_DIR", r)
		}
	})
}

func TestReportSkillSyncs(t *testing.T) {
	var b bytes.Buffer
	reportSkillSyncs(&b, []skills.ClientSync{
		{Client: "claude", Name: "Claude Code", Paths: []string{"/h/.claude/skills/prereview/SKILL.md"}, State: skills.Refreshed},
		{Client: "cursor", Name: "cursor-agent", Paths: []string{"/h/.cursor/rules/prereview.mdc"}, State: skills.EditedThere},
	})
	out := b.String()
	for _, want := range []string{
		"refreshed the Claude Code integration", "/h/.claude/skills/prereview/SKILL.md", "reload it in Claude Code",
		"cursor-agent integration was not updated", "edited there — not updated", "prereview --install-skill --client=cursor",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr missing %q:\n%s", want, out)
		}
	}
	b.Reset()
	reportSkillSyncs(&b, nil)
	if b.Len() != 0 {
		t.Errorf("nothing to report printed %q", b.String())
	}
}
