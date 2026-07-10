package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/prereview/internal/review"
)

// runComments implements `prereview comments [--out <dir>] [--json] [--all]` —
// the supported way for the coding agent (or a human) to ENUMERATE review
// comments from a stable interface, instead of hand-parsing comments.csv. It is
// the read counterpart of `prereview processed`: read ids here, mark them there.
//
// By default it prints only the actionable set (unresolved, non-outdated,
// non-draft — what the agent should act on); --all includes every comment.
// --json emits a JSON array in the SAME shape the --agent snapshot uses
// (so an agent parses one contract everywhere); without it, a terse human table.
func runComments(args []string) error {
	fs := flag.NewFlagSet("comments", flag.ContinueOnError)
	out := fs.String("out", "", "directory whose .prereview/ holds the review (the REPO printed at launch); defaults to the current directory")
	asJSON := fs.Bool("json", false, "print the comments as a JSON array (same shape as the --agent snapshot)")
	all := fs.Bool("all", false, "include resolved / outdated / draft comments (default: only the actionable set)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview comments [--out <dir>] [--json] [--all]\n\n"+
				"  List the review's comments from a stable interface (no CSV hand-parsing).\n"+
				"  Defaults to the actionable set the agent should act on; --all includes\n"+
				"  resolved/outdated/draft. --json emits the same shape as the snapshot.\n"+
				"  Pipe the ids into `prereview processed --file -` to mark them worked on.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	csvPath := filepath.Join(dir, review.CommentsFileName)
	comments, err := review.LoadComments(csvPath, *all)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(comments) // non-nil slice ⇒ `[]` when empty, never `null`
	}

	if len(comments) == 0 {
		fmt.Println("no comments")
		return nil
	}
	for _, c := range comments {
		fmt.Printf("%s  %s  %s\n", c.ID, commentLocation(c), firstLine(c.Body))
	}
	return nil
}

// commentLocation renders a comment's anchor for the human table: file:line for
// line/text comments, file for whole-file comments, and the page URL for
// live-site region comments.
func commentLocation(c review.StreamComment) string {
	switch c.Kind {
	case "file":
		return c.File
	case "region":
		return c.URL
	default:
		if c.ToLine > c.FromLine {
			return fmt.Sprintf("%s:%d-%d", c.File, c.FromLine, c.ToLine)
		}
		return fmt.Sprintf("%s:%d", c.File, c.FromLine)
	}
}

// firstLine collapses a multi-line body to its first line for the one-per-row
// table, so a long comment never breaks the layout.
func firstLine(body string) string {
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		return strings.TrimSpace(body[:i]) + " …"
	}
	return strings.TrimSpace(body)
}
