package review

import (
	"html/template"
	"strings"
)

// search.go implements the cmd+k content search (issue #91): grep over file
// paths AND line contents across the changed set (default) or all files, with
// the results stored on state.SearchHits and jumped-to via JumpToSearchResult.
//
// The scan lives on the CONTROLLER, not a zero-arg PrereviewState method,
// because it needs loadDiffCached (the mtime-cached per-file line source, which
// only the controller holds). The actions call computeSearch and stash the
// slice; the template just ranges state.SearchHits.

const (
	// minSearchLen gates the scan so a single stray character doesn't grep every
	// file. Rune count, so a 2-char CJK query still searches.
	minSearchLen = 2
	// maxSearchHits caps the result list — both the render size and (by stopping
	// early) the scan work when a query matches broadly. SearchTruncated reflects
	// the cap in the UI.
	maxSearchHits = 100
)

// SearchHit is one row in the palette: either a file-PATH match (Kind "file",
// no line) or a line-CONTENT match (Kind "line") with the 1-based diff line
// numbers and the matched line pre-escaped + <mark>-highlighted.
type SearchHit struct {
	File   string        `json:"file"`
	Kind   string        `json:"kind"` // "file" | "line"
	OldNum int           `json:"old_num"`
	NewNum int           `json:"new_num"`
	Line   template.HTML `json:"line"` // empty for Kind == "file"
}

// computeSearch scans the in-scope files for the trimmed query and returns up to
// maxSearchHits hits (filename matches first per file, then content matches).
// Empty/short queries return nil. Files whose diff can't load (binary, oversized,
// gone) are skipped for content but still match by path.
func (c *PrereviewController) computeSearch(state PrereviewState) []SearchHit {
	q := strings.TrimSpace(state.SearchQuery)
	if len([]rune(q)) < minSearchLen {
		return nil
	}
	lq := strings.ToLower(q)
	var hits []SearchHit
	for _, f := range state.Files {
		// Scope: changed set by default (Status != ""), or every file when the
		// user toggles SearchScopeAll. (Distinct from the drawer's ShowAllFiles.)
		if !state.SearchScopeAll && f.Status == "" {
			continue
		}
		if strings.Contains(strings.ToLower(f.Path), lq) {
			hits = append(hits, SearchHit{File: f.Path, Kind: "file"})
			if len(hits) >= maxSearchHits {
				return hits
			}
		}
		diff, err := c.loadDiffCached(state.Base, f.Path)
		if err != nil || diff == nil {
			continue // binary/oversized/unreadable — path match above still stands
		}
		for _, l := range diff.Lines {
			if l.NewNum == 0 {
				continue // only lines that exist in the working-tree file
			}
			if strings.Contains(strings.ToLower(l.Content), lq) {
				hits = append(hits, SearchHit{
					File: f.Path, Kind: "line",
					OldNum: l.OldNum, NewNum: l.NewNum,
					Line: highlightMatch(l.Content, q),
				})
				if len(hits) >= maxSearchHits {
					return hits
				}
			}
		}
	}
	return hits
}

// highlightMatch escapes line for HTML and wraps each case-insensitive
// occurrence of query in <mark class="search-match">. It bails to a plain
// escaped line if lower-casing changes the byte length (rare non-ASCII), rather
// than risk misaligned slices — a missed highlight, never broken markup.
func highlightMatch(line, query string) template.HTML {
	if query == "" {
		return template.HTML(template.HTMLEscapeString(line))
	}
	lower := strings.ToLower(line)
	lq := strings.ToLower(query)
	if len(lower) != len(line) { // ToLower changed byte length → don't risk offsets
		return template.HTML(template.HTMLEscapeString(line))
	}
	var b strings.Builder
	pos := 0
	for {
		idx := strings.Index(lower[pos:], lq)
		if idx < 0 {
			b.WriteString(template.HTMLEscapeString(line[pos:]))
			break
		}
		start := pos + idx
		end := start + len(lq) // 1:1 byte map since len(lower)==len(line)
		b.WriteString(template.HTMLEscapeString(line[pos:start]))
		b.WriteString(`<mark class="search-match">`)
		b.WriteString(template.HTMLEscapeString(line[start:end]))
		b.WriteString(`</mark>`)
		pos = end
	}
	return template.HTML(b.String()) //nolint:gosec // every dynamic segment is HTMLEscapeString'd; only the fixed <mark> tags are literal
}

// SearchTruncated reports whether the result list hit the cap (so the palette
// can note "showing first N"). Zero-arg for the template.
func (s PrereviewState) SearchTruncated() bool {
	return len(s.SearchHits) >= maxSearchHits
}
