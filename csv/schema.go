// Package csv writes comment rows to a CSV file that the launching Claude
// skill consumes. RFC-4180 quoting via encoding/csv handles multi-line
// bodies and embedded commas/quotes correctly without ad-hoc escaping.
package csv

// Column names — load-bearing. The skill's reference docs and any
// LLM-side parser depend on these exact strings. Reordering breaks the
// contract.
const (
	ColID        = "id"
	ColFile      = "file"
	ColFromLine  = "from_line"
	ColToLine    = "to_line"
	ColSide      = "side"
	ColBody      = "body"
	ColCreatedAt = "created_at"
	// `resolved` is "true" or "false". The skill should act only on
	// unresolved comments; resolved ones are kept as historical record.
	ColResolved = "resolved"
	// `anchor` is an opaque JSON blob (the content the comment was
	// anchored to at creation time) used internally to re-locate a
	// comment when the file changes. The skill must NOT parse it.
	ColAnchor = "anchor"
	// `anchor_status` is "ok" | "moved" | "outdated" (empty == "ok" for
	// legacy rows). A flat scalar the skill filters exactly like
	// `resolved`: treat `outdated` like `resolved=true` — the line
	// numbers no longer point at the intended content.
	ColAnchorStatus = "anchor_status"
)

// Header is the row written before any data. Position-stable.
var Header = []string{
	ColID, ColFile, ColFromLine, ColToLine, ColSide, ColBody, ColCreatedAt, ColResolved,
	ColAnchor, ColAnchorStatus,
}
