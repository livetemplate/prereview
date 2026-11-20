package review

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/livetemplate/prereview/gitdiff"
)

// mdFileDiff is src as a reviewed Markdown file, rendered blocks included.
func mdFileDiff(src string) *gitdiff.FileDiff {
	d := &gitdiff.FileDiff{Path: "doc.md"}
	for i, l := range strings.Split(strings.TrimSuffix(src, "\n"), "\n") {
		d.Lines = append(d.Lines, gitdiff.DiffLine{Kind: "add", NewNum: i + 1, Content: l})
	}
	d.MarkdownBlocks = gitdiff.RenderMarkdownBlocks([]byte(src), d.Path)
	return d
}

const importDoc = "# Plan\n\nWe ship **on Friday** after review.\n" +
	"The rollout starts small and\ngrows each week.\n\n" +
	"- first item\n- second item\n\n| step | owner |\n|---|---|\n| deploy | Alice |\n\n" +
	"Twice here. Twice here.\n"

func TestResolveQuote(t *testing.T) {
	x := newQuoteIndex(mdFileDiff(importDoc))
	for _, tc := range []struct {
		name, quote      string
		ok               bool
		from, to, fc, tc int
		snippet          string
	}{
		{name: "source, one line", quote: "after review", ok: true, from: 3, to: 3, fc: 22, tc: 34, snippet: "after review"},
		{name: "source, across lines", quote: "starts small and grows each", ok: true, from: 4, to: 5, fc: 12, tc: 10, snippet: "starts small and\ngrows each"},
		{name: "extra whitespace and curly quotes fold", quote: "  The rollout starts  ", ok: true, from: 4, to: 4, fc: 0, tc: 18, snippet: "The rollout starts"},
		{name: "rendered: **b** quoted as b (its paragraph, lines 3-5)", quote: "ship on Friday after", ok: true, from: 3, to: 5, snippet: "ship on Friday after"},
		{name: "rendered: a table cell", quote: "deploy Alice", ok: true, from: 12, to: 12, snippet: "deploy Alice"},
		{name: "a list item", quote: "second item", ok: true, from: 8, to: 8, fc: 2, tc: 13, snippet: "second item"},
		{name: "twice: ambiguous", quote: "Twice here."},
		{name: "not there", quote: "nothing like this"},
		{name: "empty", quote: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := x.resolve(tc.quote)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.ok, m)
			}
			if !ok {
				return
			}
			if want := (quoteMatch{from: tc.from, to: tc.to, fromCol: tc.fc, toCol: tc.tc, snippet: tc.snippet}); m != want {
				t.Errorf("match %+v, want %+v", m, want)
			}
		})
	}
}

// importController is a controller reviewing doc.md (no git) with an inbox.
func importController(t *testing.T) *PrereviewController {
	t.Helper()
	c := checksController(t)
	c.NoGit = true
	writeRepoFile(t, c, "doc.md", importDoc)
	return c
}

func queueImports(t *testing.T, c *PrereviewController, recs ...RemoteComment) {
	t.Helper()
	var vs []any
	for _, r := range recs {
		vs = append(vs, r)
	}
	if err := AppendJSONL(ImportsPath(c.CSVPath), vs...); err != nil {
		t.Fatal(err)
	}
}

func remote(id, quote, body string) RemoteComment {
	return RemoteComment{RemoteID: id, Source: "Google Docs", Author: "Alice", URL: "https://docs.example/" + id,
		File: "doc.md", Quote: quote, Body: body}
}

// TestDrain: each remote comment lands once, anchored on its quote or on the
// whole file, with its source; a deleted one isn't brought back, and an invalid
// or resolved one is skipped.
func TestDrain(t *testing.T) {
	c := importController(t)
	bad := remote("x3", "", "on a file that isn't here")
	bad.File = "missing.md"
	resolved := remote("x4", "after review", "done there")
	resolved.Resolved = true
	queueImports(t, c, remote("x1", "after review", "Is this still true?"), remote("x2", "the old intro", "This moved."), bad, resolved)
	n, err := c.DrainImports()
	if err != nil || n != 2 {
		t.Fatalf("drained %d (%v), want 2", n, err)
	}
	cms := c.loadCommentsFromDisk()
	applyProcessedMarks(cms, c.CSVPath)
	if len(cms) != 2 {
		t.Fatalf("%d comments, want 2: %+v", len(cms), cms)
	}
	text, file := cms[0], cms[1]
	if text.Kind != commentKindText || text.FromLine != 3 || text.FromCol != 22 || text.Anchor.Snippet != "after review" || text.Body != "Is this still true?" {
		t.Errorf("quoted comment %+v", text)
	}
	if file.Kind != commentKindFile || file.Source == nil || file.Source.Quote != "the old intro" {
		t.Errorf("unmatched quote: %+v (source %+v)", file, file.Source)
	}
	if s := text.Source; s == nil || s.Name != "Google Docs" || s.Author != "Alice" || s.URL != "https://docs.example/x1" {
		t.Errorf("source %+v", s)
	}
	if sc := toStreamComment(text); sc.Source == nil || sc.Source.Name != "Google Docs" || sc.Text != "after review" {
		t.Errorf("snapshot item %+v", sc)
	}

	// Draining again, or importing the same ids again, adds nothing; nor does a
	// comment the reviewer deleted come back.
	s := PrereviewState{Comments: c.loadCommentsFromDisk()}
	if _, err := c.DeleteComment(s, checksCtx("deleteComment", map[string]interface{}{"id": text.ID})); err != nil {
		t.Fatal(err)
	}
	queueImports(t, c, remote("x1", "after review", "Is this still true?"))
	if n, err := c.DrainImports(); err != nil || n != 0 {
		t.Errorf("a second drain added %d (%v)", n, err)
	}
	if got := len(c.loadCommentsFromDisk()); got != 1 {
		t.Errorf("%d comments after re-import, want 1", got)
	}
}

// TestDrain_MergeKeepsTabEdits: a tab that loaded before the import saves, and
// the imported rows stay; CommentsImported then shows them in the tab.
func TestDrain_MergeKeepsTabEdits(t *testing.T) {
	c := importController(t)
	tab := PrereviewState{Comments: c.loadCommentsFromDisk()}
	queueImports(t, c, remote("x1", "after review", "Is this still true?"))
	if n, err := c.DrainImports(); err != nil || n != 1 {
		t.Fatalf("drained %d (%v)", n, err)
	}
	tab.Comments = append(tab.Comments, testComment("mine"))
	if err := c.persist(tab.Comments); err != nil {
		t.Fatal(err)
	}
	if got := diskIDs(c); len(got) != 2 {
		t.Fatalf("on disk %v, want the import and the tab's comment", got)
	}
	tab, _ = c.CommentsAdded(tab, nil)
	if len(tab.Comments) != 2 || tab.Comments[1].Source == nil {
		t.Errorf("tab after CommentsAdded: %+v", tab.Comments)
	}
}

// TestNormalize: every field is required; a path must stay inside the review,
// be the file under review in a single-file review, and, with a root, be a
// regular file there reached without a symlink out of it.
func TestNormalize(t *testing.T) {
	c := importController(t)
	outside := t.TempDir() + "/secret.md"
	if err := os.WriteFile(outside, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, c.RepoPath+"/link.md"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(c.RepoPath+"/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	r := remote("x1", "", "body")
	r.File = "./doc.md"
	if err := r.Normalize(c.RepoPath, ""); err != nil || r.File != "doc.md" {
		t.Errorf("./doc.md: %q %v", r.File, err)
	}
	for _, tc := range []struct {
		root, scope string
		mut         func(*RemoteComment)
	}{
		{c.RepoPath, "", func(r *RemoteComment) { r.RemoteID = "" }},
		{c.RepoPath, "", func(r *RemoteComment) { r.Source = " " }},
		{c.RepoPath, "", func(r *RemoteComment) { r.Body = "" }},
		{c.RepoPath, "", func(r *RemoteComment) { r.File = "" }},
		{"", "", func(r *RemoteComment) { r.File = "../doc.md" }},
		{"", "", func(r *RemoteComment) { r.File = "/etc/passwd" }},
		{"", "other.md", func(r *RemoteComment) {}},
		{c.RepoPath, "", func(r *RemoteComment) { r.File = "missing.md" }},
		{c.RepoPath, "", func(r *RemoteComment) { r.File = "dir" }},
		{c.RepoPath, "", func(r *RemoteComment) { r.File = "link.md" }},
	} {
		r := remote("x1", "", "body")
		tc.mut(&r)
		if err := r.Normalize(tc.root, tc.scope); err == nil {
			b, _ := json.Marshal(r)
			t.Errorf("Normalize(root %q, scope %q) accepted %s", tc.root, tc.scope, b)
		}
	}
	r = remote("x1", "", "body")
	r.File = "missing.md"
	if err := r.Normalize("", ""); err != nil {
		t.Errorf("with no root, a missing file is left to the drain: %v", err)
	}
}
