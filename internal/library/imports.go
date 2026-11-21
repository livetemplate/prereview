package library

import (
	"encoding/json"
	"path/filepath"
)

// Comments from a remote copy of a reviewed doc (#209): `prereview import`
// appends them to the store's inbox, and the review's server adds each to
// comments.csv and records it in the ledger. Teammates write them, so they are
// context for the agent, never evidence of the reviewer's taste.
const (
	ImportInboxFile  = "imports.jsonl"        // the CLI's; append-only
	ImportLedgerFile = "imports-ledger.jsonl" // the server's; append-only
)

// ImportRecord is one ledger line: a remote comment the server added (Kind
// ""), a reply it added to that comment's thread (ImportReply: RemoteID is the
// reply's), or the comment resolved because the remote copy was (ImportResolve).
type ImportRecord struct {
	Kind      string `json:"kind,omitempty"`
	RemoteID  string `json:"remote_id"`
	CommentID string `json:"comment_id"`
	Source    string `json:"source"`
	URL       string `json:"url,omitempty"`
	Author    string `json:"author,omitempty"`
	Quote     string `json:"quote,omitempty"`
	At        string `json:"at"`
}

// RemoteKey identifies a remote comment across imports: ids are only unique
// within their source.
func RemoteKey(source, remoteID string) string { return source + "\x00" + remoteID }

// Ledger record kinds.
const (
	ImportComment = ""
	ImportReply   = "reply"
	ImportResolve = "resolve"
)

// ImportLedger is the store's ledger, in order.
func ImportLedger(store string) []ImportRecord {
	var out []ImportRecord
	eachLine(filepath.Join(store, ImportLedgerFile), func(b []byte) {
		var r ImportRecord
		if json.Unmarshal(b, &r) == nil && r.CommentID != "" {
			out = append(out, r)
		}
	})
	return out
}

// Imports is the store's imported comments, by comment id.
func Imports(store string) map[string]ImportRecord {
	out := map[string]ImportRecord{}
	for _, r := range ImportLedger(store) {
		if r.Kind == ImportComment {
			out[r.CommentID] = r
		}
	}
	return out
}
