package gitdiff

import (
	"bytes"
	"html/template"
	htmlpkg "html"
	"strings"

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
// We highlight each line independently. This misses multi-line
// constructs (raw strings, block comments) — those render as code-like
// but not perfectly. Highlighting the whole-file would be more accurate
// but requires reconstructing pre-diff state per side (add vs del); the
// per-line approach is good-enough and trivial. Reassess if users
// complain about specific languages.

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

// HighlightLine tokenises one line via the lexer that matches `filename`
// (e.g. ".go" → Go lexer) and returns the syntax-highlighted HTML.
// Returns the HTML-escaped raw content if the lexer can't tokenise.
func HighlightLine(filename, content string) template.HTML {
	if content == "" {
		return ""
	}
	lx := lexers.Match(filename)
	if lx == nil {
		lx = lexers.Fallback
	}
	lx = chroma.Coalesce(lx)
	iter, err := lx.Tokenise(nil, content)
	if err != nil {
		return template.HTML(htmlpkg.EscapeString(content))
	}
	var buf bytes.Buffer
	if err := chromaFormatter.Format(&buf, chromaStyle, iter); err != nil {
		return template.HTML(htmlpkg.EscapeString(content))
	}
	out := buf.String()
	// Chroma appends a trailing newline; strip so our <pre>-styled span
	// doesn't add a blank line of its own per diff row.
	out = strings.TrimRight(out, "\n")
	return template.HTML(out)
}
