package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/livetemplate/prereview/internal/checks"
	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
	"github.com/livetemplate/prereview/internal/skills"
)

// The agent's own text (its replies, suggestions and quiz questions) gets the
// checks your docs get, after it is written: `suggest`, `quiz` and `reply`
// print what they find to stderr and never fail, since the text is already
// posted and a broken rule must not stop the agent. Each kind is checked under
// its own name (library.AgentTexts: reply.md, suggestion.md, quiz.md) so a
// check's `files:` picks it. Only inline rules run: the built-ins were tuned
// for documents and are noisy on a one-line reply, and a command check or Vale
// needs a file on disk.

// agentChecks are the rules `prereview check` runs, minus command checks:
// all of them for a suggestion's proposed text (file text), and without the
// built-ins for the agent's own words.
type agentChecks struct{ file, own *checks.Library }

func newAgentChecks(lib *checks.Library) agentChecks {
	file := slices.DeleteFunc(slices.Clone(lib.Rules), (*checks.Rule).Async)
	own := slices.DeleteFunc(slices.Clone(file), func(r *checks.Rule) bool { return r.Source == checks.SourceBuiltin })
	return agentChecks{file: &checks.Library{Rules: file}, own: &checks.Library{Rules: own}}
}

// agentPiece is one piece of agent text to check.
type agentPiece struct {
	Label string // where a finding is reported: "reply", "suggestion s1 (docs/a.md)"
	Name  string // the name checks see: the kind's file, or the suggested file's path
	Lines []string
	// FileText is a suggestion's proposed text: it lands in the file, so it is
	// checked as file text, built-ins included, and no judge is asked about it.
	FileText bool
}

// report prints the findings on pieces, the rubrics of the judges scoped to
// them and, when it printed anything, the skills to write them with. noun names
// the text ("reply").
func (a agentChecks) report(w io.Writer, noun string, pieces []agentPiece, writeWith func() []string) {
	var out []string
	asked := map[string]bool{}
	for _, p := range pieces {
		lib := a.own
		if p.FileText {
			lib = a.file
		}
		for _, r := range lib.RunAll(context.Background(), "", p.Name, p.Lines, false) {
			if r.Status == checks.StatusError {
				out = append(out, errorLine(p.Label, r))
			}
			for _, f := range r.Findings {
				out = append(out, findingLine(p.Label, r, f))
			}
		}
		if p.FileText {
			continue
		}
		for _, r := range lib.Rules {
			// An unscoped judge is a whole-document rubric: noise on a reply.
			if r.IsJudge() && len(r.Files) > 0 && r.Applies(p.Name) && !asked[r.ID] {
				asked[r.ID] = true
				out = append(out, fmt.Sprintf("judge %q asks of your %s: %s", r.Name, noun, strings.Join(strings.Fields(r.Rubric), " ")))
			}
		}
	}
	if len(out) == 0 {
		return
	}
	if ww := writeWith(); len(ww) > 0 {
		out = append(out, fmt.Sprintf("write your %s with these skills: %s", noun, strings.Join(ww, ", ")))
	}
	fmt.Fprintf(w, "prereview: %s\n", strings.Join(out, "\nprereview: "))
}

// checkAgentText runs the checks on pieces of kind t and prints what they find
// to w.
func checkAgentText(w io.Writer, t library.AgentText, pieces []agentPiece) {
	newAgentChecks(newCheckRun("").lib).report(w, t.Noun, pieces, func() []string {
		lib := library.Dir()
		if lib == "" {
			return nil
		}
		return skills.WriteWith(skills.Current(lib, library.LoadLearnings(lib)))[t.Kind]
	})
}

// replyPieces is a reply's text.
func replyPieces(text string) []agentPiece {
	return []agentPiece{{Label: "reply", Name: library.ReplyText.File, Lines: strings.Split(text, "\n")}}
}

// suggestionPieces is each suggestion's note (as suggestion.md) and its
// proposed text (under its file's path).
func suggestionPieces(sugs []review.Suggestion) []agentPiece {
	var out []agentPiece
	for _, s := range sugs {
		if s.Note != "" {
			out = append(out, agentPiece{Label: "suggestion " + s.ID + " note", Name: library.SuggestionText.File, Lines: strings.Split(s.Note, "\n")})
		}
		if s.ProposedText != "" {
			out = append(out, agentPiece{Label: "suggestion " + s.ID + " (" + s.File + ")", Name: s.File, Lines: strings.Split(s.ProposedText, "\n"), FileText: true})
		}
	}
	return out
}

// quizPieces is each question's prompt, options and explanation, as quiz.md.
func quizPieces(quizzes []review.Quiz) []agentPiece {
	var out []agentPiece
	for _, q := range quizzes {
		for _, qu := range q.Questions {
			var lines []string
			for _, t := range append(append([]string{qu.Prompt}, qu.Options...), qu.Why) {
				lines = append(lines, strings.Split(t, "\n")...)
			}
			out = append(out, agentPiece{Label: "quiz " + review.QuizThreadID(q.ID, qu.ID), Name: library.QuizText.File, Lines: lines})
		}
	}
	return out
}
