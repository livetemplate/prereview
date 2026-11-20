package review

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

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

// TestImportSources_LedgerURL: a ledger edited to hold a script URL yields no link.
func TestImportSources_LedgerURL(t *testing.T) {
	c := importController(t)
	if err := AppendJSONL(ledgerPath(c.CSVPath), map[string]string{"remote_id": "x", "comment_id": "c1", "source": "Docs", "url": "javascript:alert(1)"}); err != nil {
		t.Fatal(err)
	}
	cms := []Comment{{ID: "c1"}}
	applyImportSources(cms, c.CSVPath)
	if cms[0].Source == nil || cms[0].Source.URL != "" {
		t.Errorf("source %+v, want no URL", cms[0].Source)
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
	tab, _ = c.CommentsChanged(tab, nil)
	if len(tab.Comments) != 2 || tab.Comments[1].Source == nil {
		t.Errorf("tab after CommentsChanged: %+v", tab.Comments)
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
	r = remote("x1", "", "body")
	r.URL = "javascript:alert(1)"
	if err := r.Normalize(c.RepoPath, ""); err != nil || r.URL != "" {
		t.Errorf("a script URL: kept %q (%v), want the comment with no link", r.URL, err)
	}
}

// TestDrain_Replies: a re-import adds only the replies it hasn't seen, as
// reviewer entries named for their remote author, in drain order; a new remote
// reply after the agent's hands the comment back to it.
func TestDrain_Replies(t *testing.T) {
	c := importController(t)
	r := remote("x1", "after review", "Is this still true?")
	r.Replies = []RemoteReply{{RemoteID: "y1", Author: "Bob", Body: "I think so"}}
	queueImports(t, c, r)
	if n, err := c.DrainImports(); err != nil || n != 2 { // the comment and its reply
		t.Fatalf("drained %d (%v), want 2", n, err)
	}
	id := c.loadCommentsFromDisk()[0].ID
	thread := func() []ThreadEntry { return groupThreads(loadThreads(c.CSVPath))[id] }
	if th := thread(); len(th) != 1 || th[0].Author != AuthorReviewer || th[0].Who() != "Bob" || th[0].Body != "I think so" {
		t.Fatalf("thread %+v", th)
	}

	// The agent answers; the comment leaves the queue.
	if err := AppendJSONL(AgentRepliesPath(c.CSVPath), ThreadEntry{TargetID: id, Author: AuthorAgent, Body: "Checked: yes.", At: time.Now().UnixNano()}); err != nil {
		t.Fatal(err)
	}
	if !agentRepliedLast(thread()) {
		t.Fatal("the agent's reply isn't last")
	}
	// A re-import with one new reply, its remote time earlier than the agent's.
	r.Replies = append(r.Replies, RemoteReply{RemoteID: "y2", Author: "Alice", CreatedAt: "2020-01-01T00:00:00Z", Body: "No, it changed"})
	queueImports(t, c, r)
	if n, err := c.DrainImports(); err != nil || n != 1 {
		t.Fatalf("re-import drained %d (%v), want 1 (the new reply)", n, err)
	}
	th := thread()
	if len(th) != 3 || th[2].Who() != "Alice" || agentRepliedLast(th) {
		t.Errorf("after the new reply: %+v; it should be last, handing the comment back", th)
	}
	if got := toStreamThread(th)[2]; got.Author != AuthorReviewer || got.Name != "Alice" {
		t.Errorf("the agent sees %+v", got)
	}
	if n, _ := c.DrainImports(); n != 0 {
		t.Errorf("a third drain made %d changes", n)
	}
}

// TestDrain_ResolveFollow: a comment resolved on the remote copy after it came
// in is resolved here, once; a reopen there changes nothing; a tab's copy takes
// the resolve.
func TestDrain_ResolveFollow(t *testing.T) {
	c := importController(t)
	queueImports(t, c, remote("x1", "after review", "Is this still true?"))
	if _, err := c.DrainImports(); err != nil {
		t.Fatal(err)
	}
	tab := PrereviewState{Comments: c.loadCommentsFromDisk()}
	r := remote("x1", "after review", "Is this still true?")
	r.Resolved = true
	queueImports(t, c, r)
	if n, err := c.DrainImports(); err != nil || n != 1 {
		t.Fatalf("drained %d (%v), want the resolve", n, err)
	}
	if cm := c.loadCommentsFromDisk()[0]; !cm.Resolved {
		t.Fatal("not resolved after the remote copy was")
	}
	tab, _ = c.CommentsChanged(tab, nil)
	if !tab.Comments[0].Resolved {
		t.Error("the tab's copy didn't take the resolve")
	}

	// The reviewer reopens it here; the remote copy reopens and resolves again:
	// nothing more happens (a resolve is followed once).
	tab, _ = c.ToggleResolved(tab, checksCtx("toggleResolved", map[string]interface{}{"id": tab.Comments[0].ID}))
	r.Resolved = false
	queueImports(t, c, r)
	r.Resolved = true
	queueImports(t, c, r)
	if n, _ := c.DrainImports(); n != 0 {
		t.Errorf("a reopen and a second resolve made %d changes", n)
	}
	if c.loadCommentsFromDisk()[0].Resolved {
		t.Error("the reviewer's reopen was undone")
	}
}

// TestWebURL: the badge links only to http(s) pages, so neither an import nor a
// ledger edited by hand can put a script URL in an href.
func TestWebURL(t *testing.T) {
	for u, want := range map[string]string{
		"https://docs.example/d#c1": "https://docs.example/d#c1",
		"http://wiki.local/p":       "http://wiki.local/p",
		"javascript:alert(1)":       "",
		"JavaScript:alert(1)":       "",
		"data:text/html,x":          "",
		"//evil.example/x":          "",
		"":                          "",
	} {
		if got := webURL(u); got != want {
			t.Errorf("webURL(%q) = %q, want %q", u, got, want)
		}
	}
}

// TestImportedReply_ResolvedAgrees: an imported reply last on a resolved
// comment reopens nothing anywhere: the queue's reply count, the card's
// "awaiting agent" and the snapshot agree. On an open comment it counts.
func TestImportedReply_ResolvedAgrees(t *testing.T) {
	th := []ThreadEntry{{TargetID: "c1", Author: AuthorAgent, At: 1}, {TargetID: "c1", Author: AuthorReviewer, At: 2, RemoteID: "y1", Name: "Bob"}}
	for _, resolved := range []bool{true, false} {
		cm := Comment{ID: "c1", File: "doc.md", Kind: commentKindLine, FromLine: 1, ToLine: 1, Resolved: resolved}
		s := PrereviewState{Comments: []Comment{cm}, ThreadEntries: th}
		want := !resolved
		got := map[string]bool{
			"queue count":    s.AwaitingReplyCount() > 0,
			"awaiting agent": s.AwaitingAgent()["c1"],
			"snapshot":       commentActionable(cm, th),
		}
		for where, v := range got {
			if v != want {
				t.Errorf("resolved=%v: %s = %v, want %v", resolved, where, v, want)
			}
		}
	}
}
