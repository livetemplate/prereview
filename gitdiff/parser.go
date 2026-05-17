package gitdiff

import (
	"bufio"
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FileDiff is the full-file, line-by-line diff for one path.
type FileDiff struct {
	Path     string
	Lines    []DiffLine
	IsBinary bool   // true when git reports "Binary files differ" — Lines is nil
	Note     string // e.g. "file added", "file deleted"; informational, may be empty
	// MaxLineChars is the longest Content length (in characters) across
	// all DiffLines. Used by the template to set `.code` width in `ch`
	// units up front; without this hint, the browser would compute
	// `width: max-content` for every line-row to determine the diff
	// container's scrollable extent — expensive on big files (950+
	// rows) and the dominant cost in perceived vertical-scroll lag.
	MaxLineChars int
	// MarkdownBlocks is the rendered-Markdown view of the current
	// (new-side) file: top-level blocks tagged with their source line
	// range. Populated only for .md/.markdown paths; nil otherwise.
	// Computed once per load and cached with the FileDiff.
	MarkdownBlocks []MarkdownBlock
}

// IsMarkdownPath reports whether path is a Markdown file by extension.
func IsMarkdownPath(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".markdown")
}

// DiffLine is one rendered line in the viewer. Exactly one of OldNum / NewNum
// may be zero (for additions and deletions respectively); for context lines
// (Kind == "ctx") both are populated.
type DiffLine struct {
	OldNum  int    // 0 when the line doesn't exist on the old side
	NewNum  int    // 0 when the line doesn't exist on the new side
	Kind    string // "add" | "del" | "ctx"
	Content string // raw line text, no leading +/-/space, no trailing \n
	// HighlightedContent is Content rendered as syntax-highlighted HTML
	// spans via chroma. Templates render this instead of Content; the
	// raw Content stays around for CSV exports and other consumers that
	// don't want HTML markup.
	HighlightedContent template.HTML
}

// maxRenderableFileBytes caps the working-tree file size we'll send to
// the diff viewer. Beyond this, the diff is replaced with a "file too
// large" placeholder — reviewing megabytes of code in the browser is
// pointless and the page-render cost blows up. 1 MB is generous for
// hand-written source; minified bundles and binary blobs hit the cap.
const maxRenderableFileBytes = 1 << 20 // 1 MB

// tooLargeNote is the FileDiff.Note for a file over the render cap.
// Shared by the git (LoadDiff) and no-git (LoadDiffNoGit) paths so the
// wording can't drift between them.
func tooLargeNote(size int64) string {
	return fmt.Sprintf("file too large to review (%.1f MB)", float64(size)/(1<<20))
}

// LoadDiff returns the full-file diff for one path against base.
//
// Uses `git diff --no-color -U999999` so every line of the file appears
// (additions, deletions, context). For files that are pure additions (A) or
// pure deletions (D) git produces a diff against /dev/null, which the parser
// handles naturally — every line is "add" or "del" respectively.
//
// Files larger than maxRenderableFileBytes short-circuit with a Note
// instead of loading content — the viewer renders the note and skips
// the per-line markup entirely.
func LoadDiff(repo, base, path string) (*FileDiff, error) {
	if st, err := os.Stat(filepath.Join(repo, path)); err == nil && st.Size() > maxRenderableFileBytes {
		return &FileDiff{Path: path, Note: tooLargeNote(st.Size())}, nil
	}
	out, err := runGit(repo,
		"diff", "--no-color", "-U999999", "-M", "--no-ext-diff",
		base, "--", path,
	)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		// Empty diff output could mean either: (a) the file is genuinely
		// unchanged, or (b) the file is untracked (git diff is silent about
		// those). Disambiguate by checking whether the file is tracked in
		// base. If it isn't, treat working-tree content as a pure addition.
		if isWorkingTreeBase(repo, base) && !isTracked(repo, base, path) {
			fd, err := loadUntrackedAsAdded(repo, path)
			if err != nil {
				return nil, err
			}
			highlightLines(fd)
			return fd, nil
		}
		// Unchanged vs base — render the file plainly so reviewers can
		// still read and comment on it. Every line is "ctx", so neither
		// the diff overlay nor file-view mode shows any add/del coloring.
		fd, err := loadFileAsCtx(repo, path)
		if err != nil {
			return nil, err
		}
		highlightLines(fd)
		return fd, nil
	}
	fd, err := parseUnifiedDiff(path, out)
	if err != nil {
		return nil, err
	}
	highlightLines(fd)
	return fd, nil
}

// maxHighlightTotalBytes caps the total plain content we'll syntax-
// highlight for a file. Chroma inflates source ~5x in HTML
// (`<span class="kn">` per token); past this the highlighted payload
// + client parse/diff-apply dominates file-switch latency. 40 KB of
// plain source covers the vast majority of hand-written files; only
// genuinely huge ones (prereview.tmpl at 52 KB, vendored bundles,
// generated code) fall back to plain text. Computing MaxLineChars
// still happens so horizontal scroll-width stays correct.
const maxHighlightTotalBytes = 40 << 10 // 40 KB

// highlightLines fills DiffLine.HighlightedContent for each non-binary
// line in the diff and computes fd.MaxLineChars. Runs after
// parsing/loading so all three paths (parseUnifiedDiff, loadFileAsCtx,
// loadUntrackedAsAdded) get highlighting + width info uniformly.
//
// Uses the bulk HighlightLines helper so the chroma tokenizer
// initialises once for the whole file rather than once per line.
// Skips highlighting entirely when total content exceeds
// maxHighlightTotalBytes — the plain-text fallback in the template
// renders Content directly, keeping huge-file load fast.
func highlightLines(fd *FileDiff) {
	if fd == nil || fd.IsBinary || len(fd.Lines) == 0 {
		return
	}
	contents := make([]string, len(fd.Lines))
	maxChars := 0
	totalBytes := 0
	for i := range fd.Lines {
		contents[i] = fd.Lines[i].Content
		totalBytes += len(fd.Lines[i].Content)
		if n := len(fd.Lines[i].Content); n > maxChars {
			maxChars = n
		}
	}
	fd.MaxLineChars = maxChars
	// Rendered-Markdown view: reconstruct the new-side (current) file
	// from the diff lines and render its blocks. Done here (the uniform
	// post-load hook) so every load path gets it and it's cached with
	// the FileDiff. Files >1MB never reach here (LoadDiff short-circuits
	// earlier), so goldmark cost is bounded; unaffected by the
	// highlight byte cap below since rendering is cheap.
	if IsMarkdownPath(fd.Path) {
		var src strings.Builder
		for i := range fd.Lines {
			if fd.Lines[i].NewNum > 0 {
				src.WriteString(fd.Lines[i].Content)
				src.WriteByte('\n')
			}
		}
		fd.MarkdownBlocks = RenderMarkdownBlocks([]byte(src.String()))
	}
	if totalBytes > maxHighlightTotalBytes {
		// Too big to highlight cheaply — leave HighlightedContent empty;
		// the template falls back to the raw Content. Scroll-width
		// (MaxLineChars) is already set above.
		return
	}
	highlighted := HighlightLines(fd.Path, contents)
	for i := range fd.Lines {
		if i < len(highlighted) {
			fd.Lines[i].HighlightedContent = highlighted[i]
		}
	}
}

// loadFileAsCtx reads the working-tree file and emits every line as a
// context DiffLine. Used when LoadDiff finds no changes vs base — the
// reviewer still gets to see and comment on the file. Same content shape
// as loadUntrackedAsAdded but Kind="ctx" so the diff overlay paints
// nothing.
func loadFileAsCtx(repo, path string) (*FileDiff, error) {
	full := filepath.Join(repo, path)
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if bytes.IndexByte(data, 0x00) >= 0 {
		return &FileDiff{Path: path, IsBinary: true, Note: "binary file"}, nil
	}
	fd := &FileDiff{Path: path}
	content := strings.TrimSuffix(string(data), "\n")
	if content == "" {
		return fd, nil
	}
	for i, line := range strings.Split(content, "\n") {
		fd.Lines = append(fd.Lines, DiffLine{OldNum: i + 1, NewNum: i + 1, Kind: "ctx", Content: line})
	}
	return fd, nil
}

// isTracked reports whether path exists in the tree at base.
func isTracked(repo, base, path string) bool {
	_, err := runGit(repo, "cat-file", "-e", base+":"+path)
	return err == nil
}

// loadUntrackedAsAdded reads the working-tree file and synthesizes a FileDiff
// where every line is an add. Used when ListFiles surfaced an untracked file.
func loadUntrackedAsAdded(repo, path string) (*FileDiff, error) {
	full := filepath.Join(repo, path)
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("read untracked %s: %w", path, err)
	}
	// Cheap binary detection: any NUL byte → binary.
	if bytes.IndexByte(data, 0x00) >= 0 {
		return &FileDiff{Path: path, IsBinary: true, Note: "binary file"}, nil
	}

	fd := &FileDiff{Path: path, Note: "file added"}
	content := string(data)
	// Strip trailing newline so we don't emit a phantom empty add line.
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return fd, nil
	}
	for i, line := range strings.Split(content, "\n") {
		fd.Lines = append(fd.Lines, DiffLine{NewNum: i + 1, Kind: "add", Content: line})
	}
	return fd, nil
}

func parseUnifiedDiff(path string, raw []byte) (*FileDiff, error) {
	fd := &FileDiff{Path: path}

	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)

	// Track running line numbers from the hunk header @@ -a,b +c,d @@.
	// With -U999999 there is at most one hunk per file in practice, but the
	// loop tolerates multiple hunks defensively.
	var oldLn, newLn int
	inHunk := false

	for sc.Scan() {
		line := sc.Text()

		if strings.HasPrefix(line, "diff --git ") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "old mode") ||
			strings.HasPrefix(line, "new mode") ||
			strings.HasPrefix(line, "similarity index") ||
			strings.HasPrefix(line, "rename from") ||
			strings.HasPrefix(line, "rename to") {
			continue
		}
		if strings.HasPrefix(line, "new file mode") {
			fd.Note = "file added"
			continue
		}
		if strings.HasPrefix(line, "deleted file mode") {
			fd.Note = "file deleted"
			continue
		}
		if strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ") {
			fd.IsBinary = true
			fd.Note = "binary file"
			return fd, nil
		}
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}

		if strings.HasPrefix(line, "@@") {
			var err error
			oldLn, newLn, err = parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			inHunk = true
			continue
		}

		if !inHunk {
			continue
		}

		// Diff body. -U999999 means context dominates; "+" and "-" mark changes.
		// Note: git emits a "\ No newline at end of file" marker we skip.
		if strings.HasPrefix(line, `\ `) {
			continue
		}
		if line == "" {
			// A truly empty body line is a context line with empty content. The
			// unified-diff format encodes that as " " (a single space) so this
			// branch is for paranoia / robustness.
			fd.Lines = append(fd.Lines, DiffLine{OldNum: oldLn, NewNum: newLn, Kind: "ctx"})
			oldLn++
			newLn++
			continue
		}

		prefix, content := line[0], line[1:]
		switch prefix {
		case '+':
			fd.Lines = append(fd.Lines, DiffLine{NewNum: newLn, Kind: "add", Content: content})
			newLn++
		case '-':
			fd.Lines = append(fd.Lines, DiffLine{OldNum: oldLn, Kind: "del", Content: content})
			oldLn++
		case ' ':
			fd.Lines = append(fd.Lines, DiffLine{OldNum: oldLn, NewNum: newLn, Kind: "ctx", Content: content})
			oldLn++
			newLn++
		default:
			// Unrecognized content line — skip rather than fail; git output
			// generally won't reach here for clean diffs.
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan diff: %w", err)
	}
	return fd, nil
}

// parseHunkHeader extracts the starting old/new line numbers from a hunk header
// of the form "@@ -123,4 +124,7 @@ optional context".
func parseHunkHeader(line string) (oldStart, newStart int, err error) {
	end := strings.Index(line[2:], "@@")
	if end < 0 {
		return 0, 0, fmt.Errorf("bad hunk header: %q", line)
	}
	body := strings.TrimSpace(line[2 : 2+end])
	// body looks like "-123,4 +124,7" or "-1 +0,0" etc.
	parts := strings.Fields(body)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("bad hunk header parts: %q", line)
	}
	oldStart, err = parseHunkRangeStart(parts[0], '-')
	if err != nil {
		return 0, 0, err
	}
	newStart, err = parseHunkRangeStart(parts[1], '+')
	if err != nil {
		return 0, 0, err
	}
	return oldStart, newStart, nil
}

func parseHunkRangeStart(s string, sigil byte) (int, error) {
	if len(s) < 2 || s[0] != sigil {
		return 0, fmt.Errorf("bad hunk range %q", s)
	}
	num := s[1:]
	if comma := strings.IndexByte(num, ','); comma >= 0 {
		num = num[:comma]
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, fmt.Errorf("bad hunk range %q: %w", s, err)
	}
	if n == 0 {
		// git uses "-0,0" for a brand-new file's old side; surface 0 so the
		// caller increments from 1 once content arrives. But -0 actually means
		// "no lines on this side" — return 1 so subsequent increments stay sane.
		return 1, nil
	}
	return n, nil
}
