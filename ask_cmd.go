package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"github.com/livetemplate/prereview/internal/review"
)

// runAsk implements `prereview ask [--out <dir>] [--file <f>] [--check]`: the
// coding agent asks the reviewer to pick from two to four options, one of them
// recommended, instead of asking in prose. Each set holds up to four questions,
// answered as steps; the reviewer's answers reach the agent in a snapshot's
// `questions` once every step is answered and sent.
//
// It reads a JSON payload from --file or stdin (an object, a JSON array, or
// JSONL), validates the whole payload, then appends one line per set to
// questions.jsonl. Re-submitting a set id revises it (last write wins).
func runAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	out := fs.String("out", "", "this review's store: the STORE path printed at launch; defaults to the current directory")
	file := fs.String("file", "", "read the JSON payload from this file, or \"-\" for stdin (the default when omitted)")
	check := fs.Bool("check", false, "validate the payload and print what would be asked, without asking")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview ask [--out <dir>] [--file <set.json>] [--check]\n\n"+
				"  Ask the reviewer to choose, with clickable options, before you build:\n"+
				"  a direction, or data that is missing or ambiguous. Reads a JSON payload\n"+
				"  from --file or stdin: an object, a JSON array, or JSONL.\n\n"+
				"    {\n"+
				"      \"id\":   \"q-refresh\",          // optional; re-use it to revise the set\n"+
				"      \"file\": \"auth/session.go\",\n"+
				"      \"kind\": \"line\",              // line (default) | text | file\n"+
				"      \"from_line\": 41, \"to_line\": 42, \"side\": \"new\",\n"+
				"      \"for\":  \"<id>\",               // optional: reply in that comment's, suggestion's\n"+
				"                                    // or quiz question's thread\n"+
				"      \"steps\": [                     // 1 to 4 questions\n"+
				"        {\"id\": \"on-fail\", \"header\": \"Refresh\",\n"+
				"         \"prompt\": \"When a token refresh fails, what should happen?\",\n"+
				"         \"options\": [{\"label\": \"Retry twice, then sign out\", \"detail\": \"…\"},\n"+
				"                     {\"label\": \"Sign out at once\"}],  // 2 to 4\n"+
				"         \"recommended\": 0,           // optional 0-based index\n"+
				"         \"multi\": false}             // true: pick any number\n"+
				"      ]\n"+
				"    }\n\n"+
				"  The reviewer can always type their own answer instead. When they send\n"+
				"  it, the next snapshot carries it in \"questions\"; `prereview done <id>`\n"+
				"  closes it. Re-asking an open id revises the set; after done, ask with a\n"+
				"  new id.\n")
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	raw, err := readPayload(*file)
	if err != nil {
		return err
	}
	sets, err := parseAskInput(raw)
	if err != nil {
		return err
	}
	if len(sets) == 0 {
		fs.Usage()
		return fmt.Errorf("no question sets in payload")
	}
	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	csvPath := filepath.Join(dir, review.CommentsFileName)
	for _, s := range sets {
		if kind, _ := review.ResolveTarget(csvPath, s.ID); kind != "" && kind != review.TargetQuestionSet {
			return fmt.Errorf("id %q is already a %s in this review; pick another", s.ID, kind)
		}
		if err := checkAskFor(csvPath, s.For); err != nil {
			return err
		}
	}
	verb := "asked"
	if *check {
		verb = "would ask"
	}
	for _, s := range sets {
		where := s.File
		if s.Kind != review.QuestionKindFile {
			where = fmt.Sprintf("%s:%d-%d", s.File, s.FromLine, s.ToLine)
		}
		if s.For != "" {
			where += " in the thread of " + s.For
		}
		fmt.Printf("%s %s (%d question(s)) on %s\n", verb, s.ID, len(s.Steps), where)
	}
	if *check {
		return nil
	}
	vs := make([]any, len(sets))
	for i := range sets {
		vs[i] = sets[i]
	}
	return review.AppendJSONL(filepath.Join(dir, review.QuestionsFileName), vs...)
}

// parseAskInput decodes, normalizes and validates every set before the caller
// writes anything, so a bad second set can't leave a half-written file.
func parseAskInput(raw []byte) ([]review.QuestionSet, error) {
	parsed, err := decodePayload[review.QuestionSet](raw)
	if err != nil {
		return nil, err
	}
	out := make([]review.QuestionSet, 0, len(parsed))
	for i := range parsed {
		s := review.NormalizeQuestionSet(parsed[i])
		if s.CreatedAt == "" {
			s.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if err := review.ValidateQuestionSet(s); err != nil {
			return nil, fmt.Errorf("question set %d (%s): %w", i+1, s.ID, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// checkAskFor checks a set's `for`: a thread the set can reply in, which is
// any id `prereview reply` takes except another question set.
func checkAskFor(csvPath, id string) error {
	if id == "" {
		return nil
	}
	switch kind, _ := review.ResolveTarget(csvPath, id); kind {
	case review.TargetComment, review.TargetSuggestion, review.TargetQuizQuestion:
		return nil
	case review.TargetQuestionSet:
		return fmt.Errorf("\"for\": %s is a question set; ask a new set instead, or revise it with the same id", id)
	default:
		return fmt.Errorf("\"for\": unknown id %s (a comment, suggestion or quiz question; `prereview comments --json` lists comment ids)", id)
	}
}
