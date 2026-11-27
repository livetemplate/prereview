package review

import (
	"cmp"
	"encoding/json"
	"fmt"
	"time"

	"github.com/livetemplate/prereview/csv"
)

// Area is the rectangle for a kind=area comment, expressed as 0..1
// fractions of the host image's rendered (== natural-uniformly-scaled)
// dimensions. Persisted alongside the Comment in the CSV's `area`
// column as a JSON blob; the LLM consuming the CSV can scale these to
// pixels using the image's natural dimensions if it wants. Zero-value
// (W=0, H=0) is the "no area selected" sentinel.
type Area struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Empty reports whether the area carries no rectangle (zero-value).
// Used by the controller (validate non-empty before persisting) and
// the template (only render the pending-overlay when set).
func (a Area) Empty() bool { return a.W == 0 && a.H == 0 }

// JSON encodes a as the compact JSON blob persisted in the CSV's
// `area` column. Returns "" for the zero value so the column stays
// empty for non-area rows.
func (a Area) JSON() string {
	if a.Empty() {
		return ""
	}
	b, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseArea decodes the CSV's `area` JSON back into an Area. Returns
// the zero value for empty strings or malformed JSON — same
// permissive contract as parseAnchor.
func parseArea(s string) Area {
	if s == "" {
		return Area{}
	}
	var a Area
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return Area{}
	}
	return a
}

// PercentX/Y/W/H render the area's coords as CSS percentage strings
// for the template to drop straight into `style="left:...; top:...;"`.
// Multiply by 100 to convert fraction → percent; 4 decimal places is
// enough precision for sub-pixel positioning on any sane image size.
func (a Area) PercentX() string { return fmt.Sprintf("%.4f%%", a.X*100) }
func (a Area) PercentY() string { return fmt.Sprintf("%.4f%%", a.Y*100) }
func (a Area) PercentW() string { return fmt.Sprintf("%.4f%%", a.W*100) }
func (a Area) PercentH() string { return fmt.Sprintf("%.4f%%", a.H*100) }

// PercentBottom is how far down the area's bottom edge is, and PercentRight
// how far in from the right its right edge is: where a card opens under it.
func (a Area) PercentBottom() string { return fmt.Sprintf("%.4f%%", (a.Y+a.H)*100) }
func (a Area) PercentRight() string  { return fmt.Sprintf("%.4f%%", (1-a.X-a.W)*100) }

// Comment is one row in the CSV output (and one entry in state).
type Comment struct {
	ID       string `json:"id"`
	File     string `json:"file"`
	FromLine int    `json:"from_line"`
	ToLine   int    `json:"to_line"`
	Side     string `json:"side"`
	// FromCol/ToCol delimit the selected character range for kind=text
	// comments: a half-open [FromCol, ToCol) offset in *raw line*
	// coordinates (0-based rune count into the line's Content, matching
	// the rendered .content textContent — see gitdiff textcontent_test).
	// FromCol binds FromLine, ToCol binds ToLine; interior lines are fully
	// covered. Zero for every other kind (a line comment covers whole
	// lines, so it needs no column offsets).
	FromCol int       `json:"from_col"`
	ToCol   int       `json:"to_col"`
	Body    string    `json:"body"`
	Created time.Time `json:"created"`
	// Resolved marks the comment as "addressed; keep as history". The skill
	// should act only on unresolved comments. Toggled via ResolveComment.
	Resolved bool `json:"resolved"`
	// Hidden is a reviewer-only VIEW flag (issue #88): an individually re-hidden
	// RESOLVED comment stays out of the diff + overview even when "Show resolved"
	// is on, without turning the whole resolved group back off. Set by
	// HideComment, cleared in bulk by UnhideAllResolved. Only meaningful for
	// resolved comments (commentHiddenFromView ignores it otherwise). Durable in
	// the CSV `hidden` column; ignored by the skill/handoff.
	Hidden bool `json:"hidden"`
	// Processed marks that the agent has addressed this comment (issue #88): it
	// drives the "worked on" badge. Derived, NOT persisted in the CSV — set by
	// applyProcessed from the agent-written .prereview/processed.jsonl markers on
	// every Mount and on each markers-file change (see processed.go).
	Processed bool `json:"processed"`
	// Answered marks that the agent settled this comment with a reply and no edit
	// (#205, `prereview reply --answered`). Derived like Processed, from
	// .prereview/answered.jsonl (see AnsweredFileName for why it is a plain set).
	Answered bool `json:"-"`
	// Source is where an imported comment came from (#209): set from the store's
	// import ledger like Processed, never persisted in the CSV. nil for a comment
	// left in prereview.
	Source *ImportSource `json:"-"`
	// base is this comment's row as its tab last saw it on disk: set when it's
	// loaded (loadCommentsFromDisk) and after each save. persist writes only the
	// fields the tab changed since, so a stale tab can't undo another's change.
	// base.ID == "" for a comment this tab created and hasn't saved: written as
	// it is. Code that copies a field from disk into a tab's comment must set
	// that field in base too, or the copy is saved back as the tab's own change.
	base csv.Row
	// Draft is the not-yet-enqueued flag (#119): a reviewer's in-progress note
	// that is kept OUT of the agent's actionable snapshot until they enqueue it.
	// The default (false) means enqueued/active — "save auto-enqueues" — so the
	// five comment-creation paths need no change. Persisted INVERTED as the CSV
	// `enqueued` column (enqueued == !Draft); legacy rows (no column) read as
	// active. Set by materializeDraft (unsaved-text recovery); cleared by
	// EnqueueComment. There is no manual "move to draft" — a comment only ever
	// becomes a draft via unsaved-text recovery.
	Draft bool `json:"draft"`
	// Anchor is the content fingerprint captured at create/edit time so
	// the comment can be re-located when the file changes (see anchor.go).
	// AnchorStatus is "ok" | "moved" | "outdated" (empty == ok for
	// legacy pre-migration comments).
	Anchor       CommentAnchor `json:"anchor"`
	AnchorStatus string        `json:"anchor_status"`
	// Kind is the comment-shape vocabulary: "line" (or "" for legacy /
	// pre-migration comments) for line-anchored comments —
	// FromLine/ToLine are meaningful — "file" for whole-file comments
	// where line numbers are zero and the anchor is empty, "area" for
	// image-overlay annotations where Area carries the rectangle, and
	// "region" for live-site annotations (--external mode) where Area
	// carries the rectangle and URL carries the page.
	Kind string `json:"kind"`
	// Area is the rectangle for kind=area (fraction of the image) and
	// kind=region (fraction of the live page's document) comments.
	// Zero-value for line / file rows.
	Area Area `json:"area"`
	// URL is the proxied page (app-relative: pathname+query, no proxy
	// origin since the proxy port is random per run) a kind=region
	// comment is anchored to. Empty for every file-based kind.
	URL string `json:"url"`
	// Request marks a comment prereview queued as a request to the agent (an
	// extraction, a judge or benchmark run, a quiz) rather than something the
	// reviewer said: a csv.Request* value, "" for reviewer comments. Set once
	// at creation (queueRequest); the CSV `request` column.
	Request string `json:"request,omitempty"`
	// Check is the ID of the check or judge whose finding this comment was
	// queued from (queueFinding), "" otherwise: what links a queued finding to
	// its card, whatever the body says. The CSV `check` column.
	Check string `json:"check,omitempty"`
}

// IsFileLevel reports whether this comment applies to the whole file
// rather than a line range. The CSV-side persistence reads `kind=file`
// and FromLine=0; this method is the single in-process predicate so
// callers (anchor.relocate, template ranges, skill exports) don't
// re-implement the test.
func (c Comment) IsFileLevel() bool { return c.Kind == commentKindFile }

// IsAreaLevel reports whether this comment overlays an image region
// (kind="area" with a populated Area). Parallel to IsFileLevel; both
// share the "no anchor to drift, skip in relocate" contract.
func (c Comment) IsAreaLevel() bool { return c.Kind == commentKindArea }

// IsRegionLevel reports whether this comment annotates a region of a live
// page in --external mode (kind="region" with a populated Area + URL).
// Parallel to IsAreaLevel; shares the "no anchor to drift, skip in
// relocate" contract.
func (c Comment) IsRegionLevel() bool { return c.Kind == commentKindRegion }

// Location is where the comment lives, for a list row: its file, or a live-site
// annotation's page URL (#114), which has no file.
func (c Comment) Location() string {
	if c.IsRegionLevel() {
		return c.URL
	}
	return c.File
}

// IsTextLevel reports whether this comment anchors to a character range
// (kind="text" with FromCol/ToCol delimiting the selected substring).
// Unlike file/area/region, a text comment DOES drift — it reuses the
// line-anchor relocate machinery, so it is not skipped there.
func (c Comment) IsTextLevel() bool { return c.Kind == commentKindText }

// Snippet returns the exact selected phrase for a kind=text comment, or the
// quote an imported comment was left on when it couldn't be found in the file
// (empty otherwise) — shown as a quote on the card so the reviewer sees what the
// comment is about, especially for rendered-view (Preview) comments that anchor
// line-level.
func (c Comment) Snippet() string {
	switch {
	case c.IsTextLevel():
		return c.Anchor.Snippet
	case c.Source != nil: // an import whose quote wasn't found (#209): the text it was on
		return c.Source.Quote
	}
	return ""
}

// requestSummaries are the one-line labels a request comment shows in place of
// its body, which is the full prompt written for the agent.
var requestSummaries = map[string]string{
	csv.RequestJudge:      "Asked your agent to run this file's judges",
	csv.RequestBenchmark:  "Asked your agent to run a benchmark's judges",
	csv.RequestQuiz:       "Asked your agent for a quiz on the whole change",
	csv.RequestExplain:    "Asked your agent to walk you through the whole change",
	csv.RequestExtraction: "Asked your agent to read past reviews",
}

// RequestSummary is the one line a request comment shows, with its prompt
// behind "Read more"; "" for the reviewer's own comments (and an unknown
// request kind), which show their body as written.
func (c Comment) RequestSummary() string { return requestSummaries[c.Request] }

// Headline is what a one-line listing of comments shows (the Annotations page, the
// Queue): a request's summary, otherwise the comment's text.
func (c Comment) Headline() string { return cmp.Or(c.RequestSummary(), c.Body) }

// AnchorOutdated reports that re-location could not confidently place
// the comment — its line numbers no longer point at the intended
// content and a human (or the skill) must re-anchor or resolve it.
// File-level and area-level comments never go outdated (no anchor).
func (c Comment) AnchorOutdated() bool { return c.AnchorStatus == anchorOutdated }

// AnchorMoved reports that the comment was auto-shifted to follow its
// content after the file changed (purely informational in the UI).
func (c Comment) AnchorMoved() bool { return c.AnchorStatus == anchorMoved }

// AnchorEdited reports that exactly the commented lines were edited in place
// (the recorded text changed but its surrounding context is intact) — a
// deterministic "likely addressed" hint. Unlike outdated it stays ACTIONABLE:
// edited is not proof the point was correctly handled, so it is badged, never
// hidden, until the reviewer resolves it.
func (c Comment) AnchorEdited() bool { return c.AnchorStatus == anchorEdited }

// LineSpan returns "L42" for single-line, "L42-L48" for ranges,
// "file" for whole-file comments, "area" for image-area comments, and
// "region" for live-site comments — used in the template badge and the
// composer label, so every kind renders a recognisable span.
func (c Comment) LineSpan() string {
	if c.IsRegionLevel() {
		return "region"
	}
	if c.IsAreaLevel() {
		return "area"
	}
	if c.IsFileLevel() {
		return "file"
	}
	if c.IsTextLevel() {
		// Rendered-origin (Preview) text comments carry no source columns
		// (rendered text can't map to source offsets) — FromCol==ToCol==0 — so
		// they read as a plain line span, same as a line comment.
		if c.FromCol == 0 && c.ToCol == 0 {
			if c.FromLine == c.ToLine {
				return fmt.Sprintf("L%d", c.FromLine)
			}
			return fmt.Sprintf("L%d-L%d", c.FromLine, c.ToLine)
		}
		if c.FromLine == c.ToLine {
			return fmt.Sprintf("L%d:%d-%d", c.FromLine, c.FromCol, c.ToCol)
		}
		return fmt.Sprintf("L%d:%d-L%d:%d", c.FromLine, c.FromCol, c.ToLine, c.ToCol)
	}
	if c.FromLine == c.ToLine {
		return fmt.Sprintf("L%d", c.FromLine)
	}
	return fmt.Sprintf("L%d-L%d", c.FromLine, c.ToLine)
}
