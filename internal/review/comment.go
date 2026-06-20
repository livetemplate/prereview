package review

import (
	"encoding/json"
	"fmt"
	"time"
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

// Comment is one row in the CSV output (and one entry in state).
type Comment struct {
	ID       string    `json:"id"`
	File     string    `json:"file"`
	FromLine int       `json:"from_line"`
	ToLine   int       `json:"to_line"`
	Side     string    `json:"side"`
	Body     string    `json:"body"`
	Created  time.Time `json:"created"`
	// Resolved marks the comment as "addressed; keep as history". The skill
	// should act only on unresolved comments. Toggled via ResolveComment.
	Resolved bool `json:"resolved"`
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

// AnchorOutdated reports that re-location could not confidently place
// the comment — its line numbers no longer point at the intended
// content and a human (or the skill) must re-anchor or resolve it.
// File-level and area-level comments never go outdated (no anchor).
func (c Comment) AnchorOutdated() bool { return c.AnchorStatus == anchorOutdated }

// AnchorMoved reports that the comment was auto-shifted to follow its
// content after the file changed (purely informational in the UI).
func (c Comment) AnchorMoved() bool { return c.AnchorStatus == anchorMoved }

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
	if c.FromLine == c.ToLine {
		return fmt.Sprintf("L%d", c.FromLine)
	}
	return fmt.Sprintf("L%d-L%d", c.FromLine, c.ToLine)
}
