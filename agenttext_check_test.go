package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/review"
)

// agentChecksFrom loads the checks a command runs on the agent's own text from
// a checks folder and a learned-checks folder (turned-off checks: TestRunReply_Check).
func agentChecksFrom(t *testing.T, user, learned map[string]string) agentChecks {
	t.Helper()
	dirs := map[string]map[string]string{"user": user, "learned": learned}
	paths := map[string]string{}
	for name, files := range dirs {
		d := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, body := range files {
			if err := os.WriteFile(filepath.Join(d, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		paths[name] = d
	}
	return newAgentChecks(checks.Load(checks.UserDirs(paths["user"], paths["learned"])...))
}

func none() []string { return nil }

const (
	noBasically = "extends: existence\nname: No basically\nmessage: Cut 'basically'.\ntokens: [basically]\n"
	replyOnly   = "extends: existence\nname: Reply length\nmessage: Say what you changed, not how.\nfiles: [reply.md]\ntokens: [refactored]\n"
	replyJudge  = "---\nkind: judge\nname: Reply says what changed\nfiles: [reply.md]\n---\nDoes the reply say what changed, in one line?\n"
	docJudge    = "---\nkind: judge\nname: Lead with the point\n---\nDoes each section lead with its point?\n"
)

// TestAgentTextCheck: the agent's reply, suggestion and quiz text is checked
// under its own name (reply.md, suggestion.md, quiz.md), so a check's `files:`
// picks it; the built-in checks don't run on it (too noisy on one line), but a
// suggestion's proposed text is file text and gets them. Findings and scoped
// judges print; nothing fails.
func TestAgentTextCheck(t *testing.T) {
	ac := agentChecksFrom(t, map[string]string{"reply-judge.md": replyJudge, "doc-judge.md": docJudge},
		map[string]string{"learned-l1.yml": noBasically, "learned-l2.yml": replyOnly})
	long := "This is basically a very long sentence that keeps going on and on with words that add nothing at all, only to pass forty words and so trip the built-in sentence length check here, which wants a long one to fire on, so here are some more words."

	var out bytes.Buffer
	ac.report(&out, "reply", []agentPiece{{Label: "reply", Name: "reply.md", Lines: []string{"I refactored it, basically."}}}, none)
	got := out.String()
	for _, want := range []string{"reply:1:18: warning: Cut 'basically'. (No basically)", "reply:1:3: warning: Say what you changed, not how. (Reply length)",
		`Reply says what changed`, "Does the reply say what changed, in one line?"} {
		if !strings.Contains(got, want) {
			t.Errorf("reply report lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Lead with the point") {
		t.Errorf("an unscoped (whole-document) judge was asked about a reply:\n%s", got)
	}
	// A check scoped to replies doesn't fire on a README.
	out.Reset()
	ac.report(&out, "suggestion", []agentPiece{{Label: "suggestion s1 (README.md)", Name: "README.md", Lines: []string{"I refactored it."}, FileText: true}}, none)
	if strings.Contains(out.String(), "Reply length") {
		t.Errorf("a reply-only check fired on a README:\n%s", out.String())
	}

	// No built-ins on agent text: a filler word and a 40-word sentence in a reply
	// print nothing (with no other check matching)...
	bare := agentChecksFrom(t, nil, nil)
	out.Reset()
	bare.report(&out, "reply", []agentPiece{{Label: "reply", Name: "reply.md", Lines: []string{long}}}, none)
	if out.Len() != 0 {
		t.Errorf("built-in checks ran on a reply:\n%s", out.String())
	}
	// ...but the same text proposed for docs/a.md is file text: the built-ins run.
	out.Reset()
	bare.report(&out, "suggestion", []agentPiece{
		{Label: "suggestion s1 (docs/a.md)", Name: "docs/a.md", Lines: []string{long}, FileText: true},
		{Label: "suggestion s2 (main.go)", Name: "main.go", Lines: []string{long}, FileText: true},
	}, none)
	got = out.String()
	if !strings.Contains(got, "suggestion s1 (docs/a.md):1:") || !strings.Contains(got, "(Sentence length)") || !strings.Contains(got, "(Filler intensifiers)") {
		t.Errorf("a suggestion for docs/a.md wasn't checked as prose:\n%s", got)
	}
	if strings.Contains(got, "main.go") {
		t.Errorf("code was prose-checked:\n%s", got)
	}

	// The skills to write with follow any report, and are read only then.
	read := 0
	ww := func() []string { read++; return []string{"learned-writing", "learned-agent-replies"} }
	out.Reset()
	bare.report(&out, "reply", []agentPiece{{Label: "reply", Name: "reply.md", Lines: []string{"fine"}}}, ww)
	if out.Len() != 0 || read != 0 {
		t.Errorf("a clean reply printed %q or read the skills (%d)", out.String(), read)
	}
	out.Reset()
	ac.report(&out, "reply", []agentPiece{{Label: "reply", Name: "reply.md", Lines: []string{"basically"}}}, ww)
	if !strings.Contains(out.String(), "learned-writing, learned-agent-replies") {
		t.Errorf("the report doesn't name the skills to write with:\n%s", out.String())
	}
}

// TestSuggestionPieces: a suggestion's note is checked as suggestion.md and its
// proposed text under its own file's path.
func TestSuggestionPieces(t *testing.T) {
	ps := suggestionPieces([]review.Suggestion{{ID: "s1", File: "docs/a.md", ProposedText: "a\nb", Note: "shorter"}})
	if len(ps) != 2 || ps[0].Name != "suggestion.md" || ps[0].Lines[0] != "shorter" ||
		ps[1].Name != "docs/a.md" || !ps[1].FileText || len(ps[1].Lines) != 2 {
		t.Fatalf("pieces = %+v", ps)
	}
}
