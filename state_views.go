package main

import (
	"fmt"
	"path/filepath"
)

// CSVBasename returns just the filename portion of CSVPath — useful for
// compact toast/banner display where the full repo path is noise.
func (s PrereviewState) CSVBasename() string {
	return filepath.Base(s.CSVPath)
}

// SelectionEmpty reports whether nothing is currently selected.
func (s PrereviewState) SelectionEmpty() bool { return s.SelectionAnchor == 0 }

// CommentsByEndLine groups the current comments by their ToLine — the line
// the comment trails. The template renders each line N, then inlines any
// comment whose ToLine == N right after it (GitHub-mobile-style). Zero-arg
// for the same reason as SelectedLines: the livetemplate framework only
// pre-computes zero-arg methods into the data map.
//
// Restricted to the currently-selected file. Resolved comments are filtered
// out when ShowResolved is false so the diff stays focused on open issues.
func (s PrereviewState) CommentsByEndLine() map[int][]Comment {
	if s.SelectedFile == "" {
		return nil
	}
	out := make(map[int][]Comment)
	for _, c := range s.Comments {
		if c.File != s.SelectedFile {
			continue
		}
		if c.IsFileLevel() || c.IsAreaLevel() {
			continue
		}
		if c.Resolved && !s.ShowResolved {
			continue
		}
		out[c.ToLine] = append(out[c.ToLine], c)
	}
	return out
}

// FileComments returns the selected file's visible LINE-anchored
// comments (honoring ShowResolved) as a flat slice. Zero-arg so the
// rendered-Markdown view can, per block, show the comments whose
// ToLine falls in that block's source range — the line-view path
// uses CommentsByEndLine (exact-line map) instead.
//
// File-level (Kind=="file") and area-level (Kind=="area") comments
// are excluded here so they don't accidentally try to anchor at line
// 0 inside a block range. They're rendered in their own sections via
// FileLevelComments() and AreaComments().
func (s PrereviewState) FileComments() []Comment {
	if s.SelectedFile == "" {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if c.File != s.SelectedFile {
			continue
		}
		if c.IsFileLevel() || c.IsAreaLevel() {
			continue
		}
		if c.Resolved && !s.ShowResolved {
			continue
		}
		out = append(out, c)
	}
	return out
}

// FileLevelComments returns the selected file's visible whole-file
// comments (Kind == "file") in creation order. Rendered in a dedicated
// section above the per-line body so reviewers see "comments on the
// file itself" before any line-anchored feedback.
func (s PrereviewState) FileLevelComments() []Comment {
	if s.SelectedFile == "" {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if c.File != s.SelectedFile {
			continue
		}
		if !c.IsFileLevel() {
			continue
		}
		if c.Resolved && !s.ShowResolved {
			continue
		}
		out = append(out, c)
	}
	return out
}

// AreaComments returns the selected file's visible image-area comments
// (Kind == "area") in creation order. Rendered as semi-transparent
// rectangle overlays inside the image wrapper, with paired list
// entries for body + Resolve/Edit/Delete actions in the file-comments
// section. Same shape as FileLevelComments — parallel iteration.
func (s PrereviewState) AreaComments() []Comment {
	if s.SelectedFile == "" {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if c.File != s.SelectedFile {
			continue
		}
		if !c.IsAreaLevel() {
			continue
		}
		if c.Resolved && !s.ShowResolved {
			continue
		}
		out = append(out, c)
	}
	return out
}

// RegionComments returns the visible region annotations (kind="region")
// for the CURRENT proxied page (--external mode), in creation order.
// Rendered as the re-pin overlay markers + paired list entries. Scoped by
// CurrentURL (the page the iframe is on) rather than SelectedFile, since
// external mode has no file. Zero-arg so the template can range over it.
func (s PrereviewState) RegionComments() []Comment {
	if !s.ExternalMode || s.CurrentURL == "" {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if !c.IsRegionLevel() || c.URL != s.CurrentURL {
			continue
		}
		if c.Resolved && !s.ShowResolved {
			continue
		}
		out = append(out, c)
	}
	return out
}

// FocusedComment returns the region annotation the user tapped to locate
// (FocusedCommentID), or nil. The template reads its URL + Area to tell the
// client which page to show and where to scroll the iframe.
func (s PrereviewState) FocusedComment() *Comment {
	if s.FocusedCommentID == "" {
		return nil
	}
	for i := range s.Comments {
		if s.Comments[i].ID == s.FocusedCommentID {
			return &s.Comments[i]
		}
	}
	return nil
}

// AllRegionComments returns every visible region annotation for the
// --external sidebar, in creation order. Unlike RegionComments it is NOT
// scoped to the current page, so the sidebar can show annotations across
// every page the user has visited.
func (s PrereviewState) AllRegionComments() []Comment {
	if !s.ExternalMode {
		return nil
	}
	var out []Comment
	for _, c := range s.Comments {
		if !c.IsRegionLevel() {
			continue
		}
		if c.Resolved && !s.ShowResolved {
			continue
		}
		out = append(out, c)
	}
	return out
}

// SelectionEndMax returns max(SelectionAnchor, SelectionEnd) — the line
// after which the inline composer should render. Zero means "no selection,
// don't render the composer". Order-independent so the user can pick
// anchor=10 → end=5 and the composer still lands after line 10.
func (s PrereviewState) SelectionEndMax() int {
	if s.SelectionAnchor == 0 {
		return 0
	}
	if s.SelectionEnd > s.SelectionAnchor {
		return s.SelectionEnd
	}
	return s.SelectionAnchor
}

// SelectedLines returns a set of line numbers currently selected. Zero-arg
// so the livetemplate framework eagerly pre-computes it once per render
// (the framework only pre-computes zero-arg methods, so a SelectionContains(n)
// helper would not be callable from the template). The template membership-tests
// with `{{index $.SelectedLines $n}}` — index returns the zero value (false)
// for missing keys, which is exactly what we want for unselected lines.
func (s PrereviewState) SelectedLines() map[int]bool {
	if s.SelectionAnchor == 0 {
		return nil
	}
	lo, hi := s.SelectionAnchor, s.SelectionEnd
	if lo > hi {
		lo, hi = hi, lo
	}
	out := make(map[int]bool, hi-lo+1)
	for n := lo; n <= hi; n++ {
		out[n] = true
	}
	return out
}

// SelectionLabel returns the human-readable form of the current selection
// (e.g. "L42" or "L42-L48"). Empty string when nothing is selected.
func (s PrereviewState) SelectionLabel() string {
	if s.SelectionAnchor == 0 {
		return ""
	}
	lo, hi := s.SelectionAnchor, s.SelectionEnd
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo == hi {
		return fmt.Sprintf("L%d", lo)
	}
	return fmt.Sprintf("L%d-L%d", lo, hi)
}
