package review

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/livetemplate/livetemplate"

	"github.com/livetemplate/prereview/csv"
	"github.com/livetemplate/prereview/gitdiff"
	"github.com/livetemplate/prereview/internal/library"
)

// Comments on a remote copy of a reviewed doc (#209). The agent reads them
// from wherever the doc was synced (Google Docs, Notion, a wiki…) and runs
// `prereview import`, which appends them to the store's inbox. The server
// drains the inbox into comments.csv, the one store every surface reads, so an
// imported comment is queued, done, threaded and re-anchored like any other.
// The quote is the truth: the anchor is found in the text the review shows at
// drain time, never taken from line numbers the agent sent.

// RemoteComment is one comment as `prereview import` takes it.
type RemoteComment struct {
	RemoteID  string        `json:"remote_id"`
	Source    string        `json:"source"` // shown as is: "Google Docs"
	URL       string        `json:"url,omitempty"`
	Author    string        `json:"author,omitempty"`
	CreatedAt string        `json:"created_at,omitempty"`
	File      string        `json:"file"`
	Quote     string        `json:"quote,omitempty"`
	Body      string        `json:"body"`
	Resolved  bool          `json:"resolved,omitempty"`
	Replies   []RemoteReply `json:"replies,omitempty"`
}

// RemoteReply is one reply under a RemoteComment.
type RemoteReply struct {
	RemoteID  string `json:"remote_id"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	Body      string `json:"body"`
}

// ImportSource is where an imported comment came from, read from the ledger at
// load (never stored in comments.csv: re-anchoring and older binaries rewrite
// rows, and would drop it). The snapshot carries it as `source`; Quote is the
// text it was left on there.
type ImportSource struct {
	Name   string `json:"name"`
	URL    string `json:"url,omitempty"`
	Author string `json:"author,omitempty"`
	Quote  string `json:"quote,omitempty"`
}

// webURL is u when it's an absolute http or https URL with a host, else "": the
// badge links only to a web page, never to a javascript: or data: URL.
func webURL(u string) string {
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return ""
	}
	return p.String()
}

// Normalize checks the record and cleans its file path in place. file must stay
// inside the review, and be the file under review when scope is set (a
// single-file review). With root set, it must also be a regular file there,
// reached without a symlink that leaves root; "" skips that check (the CLI
// with no review running, which can't know the root).
func (r *RemoteComment) Normalize(root, scope string) error {
	switch {
	case strings.TrimSpace(r.RemoteID) == "":
		return fmt.Errorf("missing \"remote_id\"")
	case strings.TrimSpace(r.Source) == "":
		return fmt.Errorf("missing \"source\"")
	case strings.TrimSpace(r.File) == "":
		return fmt.Errorf("missing \"file\"")
	case strings.TrimSpace(r.Body) == "":
		return fmt.Errorf("missing \"body\"")
	}
	f := path.Clean(filepath.ToSlash(r.File))
	if path.IsAbs(f) || f == ".." || strings.HasPrefix(f, "../") {
		return fmt.Errorf("%q isn't a path inside the review", r.File)
	}
	if scope != "" && f != scope {
		return fmt.Errorf("%q isn't the file under review (%s)", r.File, scope)
	}
	r.URL = webURL(r.URL) // an unusable link is dropped, not the comment
	if root != "" {
		full, err := gitdiff.ResolveWithin(root, f)
		if err != nil {
			return fmt.Errorf("%q isn't a file in the review: %w", r.File, err)
		}
		if st, err := os.Stat(full); err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("%q isn't a file in the review", r.File)
		}
	}
	r.File = f
	return nil
}

// ImportsPath is the store's inbox; LedgerPath its ledger.
func ImportsPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), library.ImportInboxFile)
}

func ledgerPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), library.ImportLedgerFile)
}

// quoteMatch is where a quote was found.
type quoteMatch struct {
	from, to, fromCol, toCol int    // 1-based lines; rune columns, 0/0 on the rendered text
	snippet                  string // the matched text, as kind=text keeps it
}

// quoteIndex is a file's text, folded once for every quote matched against it:
// its source lines and, built on the first miss there (Markdown only), its
// rendered text with each block's start offset.
type quoteIndex struct {
	d          *gitdiff.FileDiff
	lines      []string
	src        []rune
	folded     folded
	rendered   *folded
	blockStart []int // rune offset of each Markdown block in the rendered text
}

func newQuoteIndex(d *gitdiff.FileDiff) *quoteIndex {
	x := &quoteIndex{d: d}
	if d != nil {
		x.lines = sideContent(d, "new")
		x.src = []rune(strings.Join(x.lines, "\n"))
		x.folded = foldRunes(x.src)
	}
	return x
}

// resolve finds quote in the file's text: first in its source lines, then (for
// Markdown) in its rendered text, as a reader of a rendered remote copy saw it.
// Each needs exactly one match; ok is false otherwise, and the comment becomes
// a file comment.
func (x *quoteIndex) resolve(quote string) (quoteMatch, bool) {
	q := foldRunes([]rune(strings.TrimSpace(quote))).s
	if q == "" || x.d == nil {
		return quoteMatch{}, false
	}
	if s, e, ok := findOnce(x.folded, q); ok {
		fl, fc := runeLineCol(x.lines, s)
		tl, tc := runeLineCol(x.lines, e)
		return quoteMatch{from: fl, to: tl, fromCol: fc, toCol: tc, snippet: string(x.src[s:e])}, true
	}
	blocks := x.d.MarkdownBlocks
	if len(blocks) == 0 {
		return quoteMatch{}, false
	}
	if x.rendered == nil {
		var text []rune
		for _, b := range blocks {
			x.blockStart = append(x.blockStart, len(text))
			text = append(text, []rune(gitdiff.BlockText(b.HTML)+"\n")...)
		}
		f := foldRunes(text)
		x.rendered = &f
	}
	s, e, ok := findOnce(*x.rendered, q)
	if !ok {
		return quoteMatch{}, false
	}
	// the block holding a rune offset: the last one starting at or before it
	block := func(off int) gitdiff.MarkdownBlock {
		i, _ := slices.BinarySearch(x.blockStart, off+1)
		return blocks[i-1]
	}
	return quoteMatch{from: block(s).StartLine, to: block(e - 1).EndLine, snippet: strings.TrimSpace(quote)}, true
}

// folded is text with what a remote copy changes folded away — curly quotes,
// non-breaking spaces, runs of whitespace and line breaks — and, for each rune
// kept, its index in the original.
type folded struct {
	s   string
	pos []int
}

func foldRunes(rs []rune) folded {
	var b strings.Builder
	var pos []int
	space := false
	for i, r := range rs {
		switch r {
		case '‘', '’':
			r = '\''
		case '“', '”':
			r = '"'
		}
		if unicode.IsSpace(r) {
			if space {
				continue
			}
			r, space = ' ', true
		} else {
			space = false
		}
		b.WriteRune(r)
		pos = append(pos, i)
	}
	return folded{s: b.String(), pos: pos}
}

// findOnce finds q in t once, and returns the match's rune span in t's
// original text; false when q is absent or appears more than once.
func findOnce(t folded, q string) (start, end int, ok bool) {
	i := strings.Index(t.s, q)
	if i < 0 || strings.Contains(t.s[i+1:], q) {
		return 0, 0, false
	}
	first := utf8.RuneCountInString(t.s[:i])
	last := first + utf8.RuneCountInString(q) - 1
	return t.pos[first], t.pos[last] + 1, true
}

// runeLineCol maps a rune offset in lines joined by "\n" to a 1-based line and
// a rune column.
func runeLineCol(lines []string, off int) (line, col int) {
	for i, l := range lines {
		n := utf8.RuneCountInString(l)
		if off <= n {
			return i + 1, off
		}
		off -= n + 1
	}
	return len(lines), 0
}

// importedComment is r as a comment on the file's current text x: on its quote
// when it's found once, else on the whole file.
func importedComment(x *quoteIndex, r RemoteComment) Comment {
	if m, ok := x.resolve(r.Quote); ok {
		a := captureAnchor(x.d, m.from, m.to, "new")
		a.Snippet = m.snippet
		return newTextComment(r.File, m.from, m.to, m.fromCol, m.toCol, "new", r.Body, a)
	}
	return Comment{ID: newCommentID(), File: r.File, Body: r.Body, Created: time.Now().UTC(), Kind: commentKindFile}
}

// readInbox is the store's queued imports, in order.
func readInbox(csvPath string) []RemoteComment {
	var out []RemoteComment
	readJSONL(ImportsPath(csvPath), func(line []byte) {
		var r RemoteComment
		if json.Unmarshal(line, &r) == nil {
			out = append(out, r)
		}
	})
	return out
}

// DrainImports applies the inbox's new remote comments to the review and
// records each change in the ledger; it returns how many it made. A new comment
// is added to comments.csv through persist's merge, so no tab's save can drop
// it. For a comment imported before, a later import adds only its new replies
// (reviewer entries in its thread, so the agent sees them), and resolves it
// once the remote copy is resolved. A comment resolved on its first import
// isn't brought in, a remote reopen changes nothing, and a record naming a file
// the review doesn't cover is skipped. Run once per server: at launch and from
// the watcher.
func (c *PrereviewController) DrainImports() (int, error) {
	if c.CSVPath == "" || c.ExternalMode {
		return 0, nil
	}
	inbox := readInbox(c.CSVPath)
	if len(inbox) == 0 {
		return 0, nil
	}
	commentFor := map[string]string{} // remote comment → comment id
	applied := map[string]bool{}      // kind + remote key, for replies and resolves
	for _, r := range library.ImportLedger(filepath.Dir(c.CSVPath)) {
		key := library.RemoteKey(r.Source, r.RemoteID)
		if r.Kind == library.ImportComment {
			commentFor[key] = r.CommentID
		} else {
			applied[r.Kind+key] = true
		}
	}
	files := map[string]*quoteIndex{}
	scope := SessionScope(c.CSVPath)
	var add []Comment
	var replies, recs []any
	resolve := map[string]bool{}
	stamp := time.Now().UTC()
	now, at := stamp.Format(time.RFC3339Nano), stamp.UnixNano()
	for _, r := range inbox {
		key := library.RemoteKey(r.Source, r.RemoteID)
		id, known := commentFor[key]
		if !known {
			if r.Resolved { // resolved on the remote copy before it ever came in
				continue
			}
			cm, ok := c.importNew(r, scope, files)
			if !ok {
				continue
			}
			add = append(add, cm)
			id = cm.ID
			commentFor[key] = id
			recs = append(recs, library.ImportRecord{RemoteID: r.RemoteID, CommentID: id, Source: r.Source,
				URL: webURL(r.URL), Author: r.Author, Quote: strings.TrimSpace(r.Quote), At: now})
		}
		for _, rp := range r.Replies {
			rkey := library.ImportReply + library.RemoteKey(r.Source, rp.RemoteID)
			if rp.RemoteID == "" || strings.TrimSpace(rp.Body) == "" || applied[rkey] {
				continue
			}
			applied[rkey] = true
			at++ // drain order, not the remote clock: a new remote reply sorts after the agent's
			replies = append(replies, ThreadEntry{TargetID: id, Author: AuthorReviewer, Body: rp.Body, At: at,
				Name: rp.Author, RemoteID: rp.RemoteID})
			recs = append(recs, library.ImportRecord{Kind: library.ImportReply, RemoteID: rp.RemoteID, CommentID: id,
				Source: r.Source, Author: rp.Author, At: now})
		}
		if rkey := library.ImportResolve + key; r.Resolved && !applied[rkey] { // known: see the top of the loop
			applied[rkey] = true
			resolve[id] = true
			recs = append(recs, library.ImportRecord{Kind: library.ImportResolve, RemoteID: r.RemoteID, CommentID: id,
				Source: r.Source, At: now})
		}
	}
	if len(recs) == 0 {
		return 0, nil
	}
	if len(add) > 0 { // persist keeps every row on disk the slice doesn't name: an append
		if err := c.persist(add); err != nil {
			return 0, fmt.Errorf("import: %w", err)
		}
	}
	if err := c.resolveRows(resolve); err != nil {
		return 0, fmt.Errorf("import: %w", err)
	}
	if err := AppendJSONL(ReviewerRepliesPath(c.CSVPath), replies...); err != nil {
		return 0, fmt.Errorf("import: replies: %w", err)
	}
	if err := AppendJSONL(ledgerPath(c.CSVPath), recs...); err != nil {
		return 0, fmt.Errorf("import: ledger: %w", err)
	}
	c.scheduleEmit()
	return len(recs), nil
}

// importNew is remote comment r as a new comment, anchored on its quote in the
// file's text (files caches each file's index for the drain); false when the
// record names a file the review doesn't cover.
func (c *PrereviewController) importNew(r RemoteComment, scope string, files map[string]*quoteIndex) (Comment, bool) {
	if err := r.Normalize(c.RepoPath, scope); err != nil { // the inbox is a file anyone can write
		slog.Warn("import: skipped a remote comment", "remote_id", r.RemoteID, "err", err)
		return Comment{}, false
	}
	x, ok := files[r.File]
	if !ok {
		d, err := c.loadDiffFresh(c.Base, r.File)
		if err != nil {
			slog.Warn("import: no text to anchor to; adding it as a file comment", "file", r.File, "err", err)
		}
		x = newQuoteIndex(d)
		files[r.File] = x
	}
	return importedComment(x, r), true
}

// resolveRows sets Resolved on the rows named by ids, changing only that field
// of each, under persistMu, so a tab's save in between can't be overwritten.
func (c *PrereviewController) resolveRows(ids map[string]bool) error {
	if len(ids) == 0 {
		return nil
	}
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	rows, err := csv.Read(c.CSVPath)
	if err != nil {
		return err
	}
	changed := false
	for i := range rows {
		if ids[rows[i].ID] && !rows[i].Resolved {
			rows[i].Resolved, changed = true, true
		}
	}
	if !changed {
		return nil
	}
	return c.CSVWriter.Write(rows)
}

// applyImportSources sets Source on the comments the ledger says were imported.
func applyImportSources(comments []Comment, csvPath string) {
	imp := library.Imports(filepath.Dir(csvPath))
	if len(imp) == 0 {
		return
	}
	for i := range comments {
		if r, ok := imp[comments[i].ID]; ok {
			// webURL again: the ledger is a file, and its URL reaches an href
			comments[i].Source = &ImportSource{Name: r.Source, URL: webURL(r.URL), Author: r.Author, Quote: r.Quote}
		}
	}
}

// CommentsChanged brings this tab's copy up to date after a change made outside
// it, such as an import: it adds the comments on disk the copy lacks, takes each
// row's resolved state from disk (every tab saves as it acts, so disk is the
// latest), and reloads the threads. Only Resolved is taken: a tab's relocated
// anchors reach disk on its own schedule (persistAnchors).
func (c *PrereviewController) CommentsChanged(state PrereviewState, _ *livetemplate.Context) (PrereviewState, error) {
	at := make(map[string]int, len(state.Comments))
	for i, cm := range state.Comments {
		at[cm.ID] = i
	}
	for _, cm := range c.loadCommentsFromDisk() {
		if i, ok := at[cm.ID]; ok {
			state.Comments[i].Resolved = cm.Resolved
			state.Comments[i].base.Resolved = cm.Resolved // seen now: a later change to it is this tab's
		} else {
			state.Comments = append(state.Comments, cm)
		}
	}
	c.applyProcessed(&state)
	state.ThreadEntries = loadThreads(c.CSVPath)
	state.Files = annotateCommentCounts(state.Files, state.Comments)
	return state, nil
}
