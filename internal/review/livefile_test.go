package review

import (
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

func TestFileForPage(t *testing.T) {
	hugo := []gitdiff.FileEntry{
		{Path: "content/index.md", Status: ""},
		{Path: "content/getting-started/index.md", Status: ""},
		{Path: "content/getting-started/install.md", Status: "M"},
		{Path: "content/guides/install.md", Status: ""},
		{Path: "main.go", Status: "M"},
	}
	static := []gitdiff.FileEntry{
		{Path: "index.html", Status: ""},
		{Path: "about.html", Status: "M"},
		{Path: "pricing/index.html", Status: ""},
		{Path: "styles.css", Status: "M"},
		{Path: "old.html", Status: "D"},
	}
	for _, tc := range []struct {
		name  string
		url   string
		files []gitdiff.FileEntry
		want  string
	}{
		{"site root", "/", hugo, "content/index.md"},
		{"a page under content/", "/getting-started/install", hugo, "content/getting-started/install.md"},
		{"trailing slash and query", "/getting-started/install/?tab=mac", hugo, "content/getting-started/install.md"},
		{"a section's index", "/getting-started/", hugo, "content/getting-started/index.md"},
		{"the longer path wins over a same-named page elsewhere", "/guides/install", hugo, "content/guides/install.md"},
		{"static root", "/", static, "index.html"},
		{"page with its extension", "/about.html", static, "about.html"},
		{"page without it", "/about", static, "about.html"},
		{"folder index", "/pricing", static, "pricing/index.html"},
		{"a deleted page is not opened", "/old", static, ""},
		{"no page file for it", "/api/users", static, ""},
		{"code files never match", "/main", hugo, ""},
	} {
		if got := fileForPage(tc.url, tc.files); got != tc.want {
			t.Errorf("%s: fileForPage(%q) = %q, want %q", tc.name, tc.url, got, tc.want)
		}
	}
}

// TestFileForPagePrefersChanged: when a page name matches more than one file,
// the changed one is the one under review.
func TestFileForPagePrefersChanged(t *testing.T) {
	files := []gitdiff.FileEntry{
		{Path: "a/install.md", Status: ""},
		{Path: "b/install.md", Status: "M"},
	}
	if got := fileForPage("/install", files); got != "b/install.md" {
		t.Errorf("got %q, want the changed b/install.md", got)
	}
}
