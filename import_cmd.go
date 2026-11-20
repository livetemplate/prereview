package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/livetemplate/prereview/internal/review"
)

// runImport implements `prereview import` (#209): the agent brings in the
// comments left on a remote copy of a reviewed doc, and the review adds them as
// ordinary comments, anchored on their quoted text. Like `suggest`, it only
// appends to a sidecar (the inbox); the server owns comments.csv and drains it.
func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	out := fs.String("out", "", "this review's store: the STORE path printed at launch (the reviewed file, or a directory whose .prereview/ holds the review, also work); defaults to the current directory")
	file := fs.String("file", "", "read the JSON payload from this file instead of stdin")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(),
			"Usage: prereview import [--out <dir>] [--file <payload.json>]\n\n"+
				"  Bring in comments left on a remote copy of a reviewed file (Google Docs,\n"+
				"  Notion, a wiki…), so they are queued like comments left in the review.\n"+
				"  Reads a JSON payload from --file (\"-\" = stdin) or stdin: an object, an array,\n"+
				"  or newline-delimited objects. Each object:\n\n"+
				"    {\n"+
				"      \"remote_id\": \"AAAAx1\",            // required; the remote comment's id\n"+
				"      \"source\":    \"Google Docs\",       // required; shown on the comment\n"+
				"      \"file\":      \"docs/plan.md\",      // required, relative to the review's root\n"+
				"      \"body\":      \"Is this still true?\", // required\n"+
				"      \"quote\":     \"the text it's on\",  // the exact text the comment is on\n"+
				"      \"author\":    \"Alice\",\n"+
				"      \"url\":       \"https://…\",\n"+
				"      \"resolved\":  false               // resolved ones are skipped\n"+
				"    }\n\n"+
				"  The quote is found in the file's text (or, for Markdown, its rendered\n"+
				"  text); a quote that isn't found, or is found twice, makes a comment on\n"+
				"  the whole file. Importing the same remote_id again adds nothing.\n")
	}
	if err := parseInterspersed(fs, args); err != nil {
		return err
	}
	raw, err := readPayload(*file)
	if err != nil {
		return err
	}
	recs, err := decodePayload[review.RemoteComment](raw)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		fs.Usage()
		return fmt.Errorf("no comments in payload")
	}
	dir, err := storeDir(*out)
	if err != nil {
		return err
	}
	csvPath := filepath.Join(dir, review.CommentsFileName)
	// A running review knows its root, so a file that isn't there fails here;
	// with none running, the review checks it when it starts.
	rec, ok := readServerRecord(filepath.Join(dir, serverPIDFileName))
	running := ok && processAlive(rec.PID)
	root := ""
	if running {
		root = rec.Repo
	}
	scope := review.SessionScope(csvPath)
	vs := make([]any, len(recs))
	for i := range recs {
		if err := recs[i].Normalize(root, scope); err != nil {
			return fmt.Errorf("comment %d (%s): %w", i+1, recs[i].RemoteID, err)
		}
		vs[i] = recs[i]
	}
	if err := review.AppendJSONL(review.ImportsPath(csvPath), vs...); err != nil {
		return err
	}
	when := "the running review adds them now"
	if !running {
		when = "no review is running here, so they're added when it next starts"
	}
	fmt.Printf("queued %d comment(s) for import; %s\n", len(recs), when)
	return nil
}
