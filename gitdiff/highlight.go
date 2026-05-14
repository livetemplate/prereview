package gitdiff

import (
	"bytes"
	htmlpkg "html"
	"html/template"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// Syntax highlighting via chroma. Server-side so:
//   - no JS download/parse on the client
//   - works without JS (Tier 1)
//   - the existing content-visibility: auto on .line-row means
//     off-screen rows skip rendering the highlighted spans entirely
//
// Implementation notes — first cut was a per-line HighlightLine that
// re-did `lexers.Match` + `chroma.Coalesce` + tokenise for every
// DiffLine. For a 670-line file that was 670 fresh tokenizer startups
// (~1-5 ms each) and the whole file-switch latency ballooned to 1-3s.
//
// Current approach:
//   - lexer per filename is cached in a sync.Map (one Match + Coalesce
//     per language per process lifetime)
//   - HighlightLines tokenises the WHOLE input in one Tokenise call and
//     splits the token stream by newlines into per-line groups. One
//     tokenizer startup amortises across every line in the file.
//
// We intentionally still tokenise diff content (add+del+ctx
// interleaved as it appears in the unified diff) rather than
// reconstructing the pre-diff "new side" or "old side" file content.
// Multi-line constructs (raw strings, block comments) that straddle a
// del/add pair render slightly wrong but the common case looks right
// and the implementation stays simple.

var (
	chromaFormatter = html.New(
		html.WithClasses(true),
		html.PreventSurroundingPre(true),
		html.WithLineNumbers(false),
	)
	chromaStyle = func() *chroma.Style {
		s := styles.Get("github")
		if s == nil {
			return styles.Fallback
		}
		return s
	}()
	// lexerCache holds Coalesce-wrapped lexers keyed by filename so the
	// per-language match + wrap cost is paid exactly once per file path.
	// Filenames within a session are stable, so a Map (vs. a fixed-size
	// LRU) is fine — bounded by the repo's file count.
	lexerCache sync.Map // map[string]chroma.Lexer
)

// HighlightCSS returns the chroma stylesheet for the active style.
// Computed once at package-init time; main.go serves it as /syntax.css.
var HighlightCSS = func() string {
	var buf bytes.Buffer
	if err := chromaFormatter.WriteCSS(&buf, chromaStyle); err != nil {
		return ""
	}
	return buf.String()
}()

// lexerFor returns a Coalesce-wrapped lexer matched against `filename`,
// caching the result so repeat calls for the same path skip the
// pattern-match + wrap overhead.
//
// Includes hand-rolled overrides for extensions chroma's registry
// doesn't natively map:
//   - .tmpl → "Go HTML Template" (chroma registers it without any
//     filename patterns, so `Match()` returns nil for *.tmpl by default;
//     prereview's templates and most projects' .tmpl files are
//     html-with-go-template, so we point them there).
func lexerFor(filename string) chroma.Lexer {
	if v, ok := lexerCache.Load(filename); ok {
		return v.(chroma.Lexer)
	}
	lx := lexers.Match(filename)
	if lx == nil {
		if strings.HasSuffix(filename, ".tmpl") {
			lx = lexers.Get("Go HTML Template")
		}
	}
	if lx == nil {
		lx = lexers.Fallback
	}
	lx = chroma.Coalesce(lx)
	lexerCache.Store(filename, lx)
	return lx
}

// HighlightLines tokenises every line of `contents` in a single pass
// and returns the syntax-highlighted HTML per line. Falls back to
// per-line HTML escape on tokenizer errors. Empty input returns nil.
//
// Joining with '\n' before tokenising lets chroma understand line
// transitions naturally — string tokens that span lines keep their
// type, etc. The output is then split back into per-line groups by
// walking the token stream and splitting tokens on their '\n' bytes.
func HighlightLines(filename string, contents []string) []template.HTML {
	if len(contents) == 0 {
		return nil
	}
	lx := lexerFor(filename)
	joined := strings.Join(contents, "\n")
	iter, err := lx.Tokenise(nil, joined)
	if err != nil {
		return escapeAll(contents)
	}

	// Walk the token stream, grouping into a slice-per-line. A token's
	// Value may contain zero or more '\n's; each '\n' closes the
	// current line group and starts a new one.
	lineGroups := make([][]chroma.Token, len(contents))
	cur := 0
	for _, tok := range iter.Tokens() {
		v := tok.Value
		for {
			if cur >= len(lineGroups) {
				// Defensive: chroma can emit a final newline beyond the
				// input range. Drop the rest.
				break
			}
			nl := strings.IndexByte(v, '\n')
			if nl < 0 {
				if v != "" {
					lineGroups[cur] = append(lineGroups[cur], chroma.Token{Type: tok.Type, Value: v})
				}
				break
			}
			before := v[:nl]
			if before != "" {
				lineGroups[cur] = append(lineGroups[cur], chroma.Token{Type: tok.Type, Value: before})
			}
			cur++
			v = v[nl+1:]
		}
	}

	out := make([]template.HTML, len(contents))
	for i, group := range lineGroups {
		if len(group) == 0 {
			// Empty line — render as empty HTML to keep the line slot
			// in the per-line array stable.
			continue
		}
		var buf bytes.Buffer
		if err := chromaFormatter.Format(&buf, chromaStyle, chroma.Literator(group...)); err != nil {
			out[i] = template.HTML(htmlpkg.EscapeString(contents[i]))
			continue
		}
		// Chroma may append a trailing newline; strip so our pre-styled
		// span doesn't add an extra blank line per diff row.
		out[i] = template.HTML(strings.TrimRight(buf.String(), "\n"))
	}
	return out
}

func escapeAll(contents []string) []template.HTML {
	out := make([]template.HTML, len(contents))
	for i, c := range contents {
		out[i] = template.HTML(htmlpkg.EscapeString(c))
	}
	return out
}

// HighlightLine is retained for callers that want to highlight a single
// line in isolation (currently none in the project — kept for backward
// compatibility and potential ad-hoc use). For bulk highlighting use
// HighlightLines, which amortises tokenizer startup across all lines.
func HighlightLine(filename, content string) template.HTML {
	if content == "" {
		return ""
	}
	out := HighlightLines(filename, []string{content})
	if len(out) == 0 {
		return template.HTML(htmlpkg.EscapeString(content))
	}
	return out[0]
}
