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
	// `kind` classifies the comment shape: "line" (or empty for
	// pre-kind rows) for line-anchored comments where `from_line` and
	// `to_line` are meaningful; "file" for whole-file comments where
	// `from_line` / `to_line` / `side` / `anchor` / `anchor_status`
	// are all zero/empty; "area" for image-overlay annotations where
	// `area` (column 12) carries the rectangle.
	ColKind = "kind"
	// `area` is a JSON blob {"x":0.1,"y":0.2,"w":0.3,"h":0.15} where each
	// value is a 0..1 fraction. For `kind=area` it's a fraction of the
	// image's natural dimensions; for `kind=region` it's a fraction of the
	// live page's document scroll dimensions (so a re-pinned annotation
	// survives scroll). Empty for line / file rows. Fractions (not pixels)
	// so a re-encoded image / re-laid-out page still highlights the same
	// logical region.
	ColArea = "area"
	// `kind=region` annotations (the --external live-site mode) anchor to a
	// URL + rectangle instead of a file + line. `url` is the proxied page,
	// app-relative (pathname+query, no proxy origin — the proxy port is
	// random per run). Empty for every file-based kind.
	ColURL = "url"
	// `from_col` / `to_col` delimit the selected character range for
	// `kind=text` comments: a half-open [from_col, to_col) offset in raw
	// line coordinates (0-based rune index into the line). from_col binds
	// from_line, to_col binds to_line; interior lines are fully covered.
	// "0" for every other kind (line comments cover whole lines).
	ColFromCol = "from_col"
	ColToCol   = "to_col"
	// `hidden` is "true" or "false". A reviewer-only VIEW flag: an individually
	// re-hidden RESOLVED comment (issue #88) stays out of the diff/overview even
	// when "Show resolved" is on. The skill MUST ignore it — it never changes
	// which comments are actionable (resolved comments are already excluded from
	// the handoff); it only declutters the human's view.
	ColHidden = "hidden"
	// `enqueued` is "true" or "false" (#119): whether the comment is queued for
	// the agent. A "draft" comment (enqueued=false) is the reviewer's not-yet-
	// submitted note, kept out of the actionable snapshot until they enqueue it.
	// ABSENT (short legacy rows) reads as "true": pre-#119 comments were always
	// active and must never silently become drafts. Persisted inverted from the
	// in-memory Draft flag (enqueued == !Draft) so the zero value is "enqueued".
	ColEnqueued = "enqueued"
)

// Header is the row written before any data. Position-stable: only ever
// append new columns (readers tolerate short legacy rows by length).
var Header = []string{
	ColID, ColFile, ColFromLine, ColToLine, ColSide, ColBody, ColCreatedAt, ColResolved,
	ColAnchor, ColAnchorStatus, ColKind, ColArea, ColURL, ColFromCol, ColToCol, ColHidden,
	ColEnqueued,
}
