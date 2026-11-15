package skills

import (
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/library"
)

func acc(id, text, scope, created string) library.Learning {
	return library.Learning{ID: id, Text: text, Scope: scope, Status: library.StatusAccepted, CreatedAt: created}
}

// TestGenerate_Deterministic: the same learnings give a byte-identical SKILL.md;
// editing a learning changes exactly its line; rejecting it removes the line.
func TestGenerate_Deterministic(t *testing.T) {
	ls := []library.Learning{
		acc("l2", "Number the steps.", library.DefaultScope, "2026-11-02T00:00:00Z"),
		acc("l1", "Cut   filler\nwords.", library.DefaultScope, "2026-11-01T00:00:00Z"),
		{ID: "l3", Text: "proposed only", Scope: library.DefaultScope, Status: library.StatusProposed},
	}
	render := func(ls []library.Learning) string {
		sk := Generate(ls, nil)
		if len(sk) != 1 || sk[0].ID != WritingID {
			t.Fatalf("skills = %+v", sk)
		}
		return sk[0].SkillMD()
	}
	a := render(ls)
	rev := []library.Learning{ls[2], ls[0], ls[1]}
	if b := render(rev); a != b {
		t.Fatalf("input order changed the output:\n%s\n---\n%s", a, b)
	}
	for _, want := range []string{"name: learned-writing\n", "- Cut filler words. <!-- learning l1 -->\n- Number the steps. <!-- learning l2 -->", Header} {
		if !strings.Contains(a, want) {
			t.Errorf("SKILL.md lacks %q:\n%s", want, a)
		}
	}
	if strings.Contains(a, "proposed only") {
		t.Error("a proposed learning took effect before it was accepted")
	}

	edited := append([]library.Learning(nil), ls...)
	edited[0].Text = "Number every step."
	if d := lineDiff(a, render(edited)); d != "- Number the steps. <!-- learning l2 -->|+ - Number every step. <!-- learning l2 -->" {
		t.Errorf("editing one learning changed more than its line: %s", d)
	}
	rejected := append([]library.Learning(nil), ls...)
	rejected[1].Status = library.StatusRejected
	if d := lineDiff(a, render(rejected)); d != "- Cut filler words. <!-- learning l1 -->|" {
		t.Errorf("rejecting removed more than its line: %s", d)
	}
}

// lineDiff is the lines only in a, then "|", then "+ " lines only in b.
func lineDiff(a, b string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var gone, added []string
	for _, l := range strings.Split(a, "\n") {
		if !mb[l] {
			gone = append(gone, l)
		}
	}
	for _, l := range strings.Split(b, "\n") {
		if !ma[l] {
			added = append(added, "+ "+l)
		}
	}
	return strings.Join(gone, ",") + "|" + strings.Join(added, ",")
}

func TestGenerate_ScopeSkillsChecksAndProposals(t *testing.T) {
	ls := []library.Learning{
		acc("w1", "Plain words.", library.DefaultScope, "2026-11-01T00:00:00Z"),
		func() library.Learning {
			l := acc("t1", "Number the steps.", "Technical docs", "2026-11-02T00:00:00Z")
			l.Globs = []string{"docs/**/*.md"}
			l.Checker = "extends: existence\nname: Filler\ntokens: [basically]\n"
			l.Produces = []string{"checker"} // checks only: no skill line
			return l
		}(),
		func() library.Learning {
			l := acc("bad", "x", library.DefaultScope, "2026-11-03T00:00:00Z")
			l.Checker = "extends: command\nname: Rm\ncommand: [rm]\n" // e.g. imported, never validated
			return l
		}(),
	}
	sk := Generate(ls, []Def{{ID: SlugID("Technical docs"), Name: "Technical writing", Scope: "Technical docs"}})
	if len(sk) != 2 || sk[0].ID != WritingID || sk[1].ID != "learned-technical-docs" {
		t.Fatalf("skills = %+v", sk)
	}
	tech := sk[1]
	if len(tech.Lines) != 0 || len(tech.Checks) != 1 || tech.Checks[0].File() != "learned-t1.yml" || tech.Checks[0].Name != "Filler" {
		t.Fatalf("technical skill = %+v", tech)
	}
	if md := tech.SkillMD(); !strings.Contains(md, "extend the `learned-writing` skill") || !strings.Contains(md, "prereview check") {
		t.Errorf("narrow skill:\n%s", md)
	}
	if r := tech.CursorRule(); strings.Contains(r, "globs:") || !strings.Contains(r, "alwaysApply: false") {
		t.Errorf("cursor rule (a learning's globs stay on its line, not the rule):\n%s", r)
	}
	for _, s := range sk {
		for _, c := range s.Checks {
			if c.LearningID == "bad" {
				t.Fatal("a command checker carried by a learning was generated")
			}
		}
	}

	var many []library.Learning
	for i := 0; i < ProposeAt; i++ {
		many = append(many, acc(string(rune('a'+i)), "x", "Email", "2026-11-01T00:00:00Z"))
	}
	lib := t.TempDir()
	if p := Proposals(lib, many); len(p) != 1 || p[0] != (Proposal{"Email", ProposeAt}) {
		t.Fatalf("proposals = %v", p)
	}
	if p := Proposals(lib, many[:ProposeAt-1]); len(p) != 0 {
		t.Fatalf("proposed with too few: %v", p)
	}
	if err := DismissScope(lib, "Email"); err != nil {
		t.Fatal(err)
	}
	if p := Proposals(lib, many); len(p) != 0 {
		t.Fatalf("proposed a dismissed scope: %v", p)
	}
}
