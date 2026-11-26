package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/livetemplate/prereview/internal/review"
)

// runQuestions implements `prereview questions [--out <dir>] [--json] [--all]`:
// the question sets the agent asked (`prereview ask`), as it should read them.
// It is `prereview comments` for questions, for agents that read the review by
// polling instead of `prereview watch`: by default the sets to act on (the
// reviewer sent or resolved them, and they are not done), which is exactly a
// snapshot's questions field; --all lists every set.
func runQuestions(args []string) error {
	fs := flag.NewFlagSet("questions", flag.ContinueOnError)
	out := fs.String("out", "", "this review's store: the STORE path printed at launch; defaults to the current directory")
	asJSON := fs.Bool("json", false, "print the sets as a JSON array (the shape of a snapshot's questions field)")
	all := fs.Bool("all", false, "include every set, unanswered ones too (default: only the sets to act on)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview questions [--out <dir>] [--json] [--all]\n\n"+
				"  List the question sets you asked with `prereview ask`. Defaults to the\n"+
				"  ones to act on: answered and sent, or resolved, and not yet done. Each\n"+
				"  answer carries the picked option labels, or `other`, a typed answer.\n"+
				"  Close a set with `prereview done <id>`.\n")
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	sets, err := review.LoadQuestions(filepath.Join(dir, review.CommentsFileName), *all)
	if err != nil {
		return err
	}
	if *asJSON {
		return printAgentJSON(dir, "questions", sets)
	}
	if len(sets) == 0 {
		fmt.Println("no questions")
		return nil
	}
	for _, q := range sets {
		state := "waiting"
		switch {
		case q.Resolved:
			state = "resolved"
		case q.Replaced:
			state = "replaced"
		case len(q.Answers) > 0:
			state = "answered"
		}
		where := q.File
		switch {
		case q.FromLine == 0: // a set about the whole file
		case q.ToLine > q.FromLine:
			where = fmt.Sprintf("%s:%d-%d", q.File, q.FromLine, q.ToLine)
		default:
			where = fmt.Sprintf("%s:%d", q.File, q.FromLine)
		}
		fmt.Printf("%s  %s  %s\n", q.ID, where, state)
	}
	return nil
}
