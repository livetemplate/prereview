package review

import (
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/livetemplate/prereview/gitdiff"
)

// pageExts are the file types a local site serves a page from: static HTML,
// Markdown content (Hugo, Jekyll, MkDocs, Docusaurus) and Go templates.
var pageExts = []string{".html", ".htm", ".md", ".markdown", ".mdx", ".tmpl", ".gohtml"}

// fileForPage is the reviewed file that most likely serves the live site's
// page at pageURL (a path, with any query), or "" when none does. A site maps
// /docs/install to docs/install.md, docs/install.html or docs/install/index.md,
// often under a content folder, so a file matches when its path without the
// extension ends with one of those names. Ties go to the exact name before
// the index forms, then to a changed file, then to the shortest path.
func fileForPage(pageURL string, files []gitdiff.FileEntry) string {
	u, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	p := strings.Trim(path.Clean("/"+u.Path), "/")
	for _, ext := range pageExts {
		p = strings.TrimSuffix(p, ext)
	}
	var names []string
	if p == "" || p == "index" {
		names = []string{"index", "_index", "README"}
	} else {
		names = []string{p, p + "/index", p + "/_index", p + "/README"}
	}

	best, bestRank := "", [3]int{}
	for _, f := range files {
		if f.Status == "D" {
			continue
		}
		stem, ok := pageStem(f.Path)
		if !ok {
			continue
		}
		for i, n := range names {
			if stem != n && !strings.HasSuffix(stem, "/"+n) {
				continue
			}
			unchanged := 0
			if f.Status == "" {
				unchanged = 1
			}
			rank := [3]int{i, unchanged, strings.Count(f.Path, "/")}
			if best == "" || less(rank, bestRank) || (rank == bestRank && f.Path < best) {
				best, bestRank = f.Path, rank
			}
			break
		}
	}
	return best
}

// pageStem is a page file's path without its extension; ok is false for any
// other file type.
func pageStem(p string) (string, bool) {
	if !slices.Contains(pageExts, strings.ToLower(path.Ext(p))) {
		return "", false
	}
	return strings.TrimSuffix(p, path.Ext(p)), true
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
