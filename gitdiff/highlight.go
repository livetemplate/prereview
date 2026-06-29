package gitdiff

import (
	"bytes"
	"fmt"
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

// Scheme is one curated, fully-coordinated color scheme: a data-scheme value,
// a human label for the picker, and the chroma styles that color its syntax in
// each mode. The chrome tokens (surfaces, diff tints) for the same scheme live
// beside it in prereview.css under the matching [data-scheme="name"] block, so
// syntax and chrome stay one system. Adding a scheme = one row here + one CSS
// token block (light + dark + @media).
type Scheme struct {
	Name        string // data-scheme value on .theme-root (and the CSS scope)
	Label       string // picker display name
	chromaLight string // chroma style for Light mode (and System-light)
	chromaDark  string // chroma style for Dark mode (and System-dark)
}

// Schemes is the registry. Order is the picker's cycle order; Schemes[0] is the
// default (see state.SchemeName / DataScheme). internal/review reads Name+Label
// for the picker; this package reads the chroma styles for /syntax.css.
var Schemes = []Scheme{
	{"solarized", "Solarized", "solarized-light", "solarized-dark"},
	{"gruvbox", "Gruvbox", "gruvbox-light", "gruvbox"},
	{"catppuccin", "Catppuccin", "catppuccin-latte", "catppuccin-mocha"},
}

// chromaStyleName is the style whose CLASS names the highlighters emit. With
// WithClasses(true) the markup carries only style-independent token classes
// (`.chroma .k` …) — colors come entirely from /syntax.css — so a single style
// drives class emission for every scheme; the per-scheme colors are layered in
// HighlightCSS. Kept on solarized-light, the default scheme's light style.
const chromaStyleName = "solarized-light"

var (
	chromaFormatter = html.New(
		html.WithClasses(true),
		html.PreventSurroundingPre(true),
		html.WithLineNumbers(false),
	)
	chromaStyle = func() *chroma.Style {
		s := styles.Get(chromaStyleName)
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

// HighlightCSS is the chroma stylesheet served as /syntax.css. It carries every
// registered scheme × both modes so the page never refetches CSS on a theme or
// mode switch. For each scheme, all rules are scoped to that scheme's
// [data-scheme] so the three schemes' (colliding) token classes don't leak into
// one another. Per scheme:
//
//   - light, scoped `[data-scheme="x"]` — applies unless a dark rule overrides;
//   - dark, scoped `[data-scheme="x"][data-mode="dark"]` (higher specificity)
//     for an explicit Dark toggle;
//   - the dark block again inside `@media (prefers-color-scheme: dark)`, scoped
//     `[data-scheme="x"]:not([data-mode="light"])` so System mode follows the OS
//     with no JS (an explicit Light opt-out still wins).
//
// Computed once at package-init; main.go serves it verbatim.
var HighlightCSS = func() string {
	var b strings.Builder
	for _, s := range Schemes {
		b.WriteString(schemeSyntaxCSS(s))
	}
	return b.String()
}()

// schemeSyntaxCSS emits one scheme's scoped light + dark chroma blocks. A
// missing/failed style degrades to whatever did render (still valid CSS) rather
// than dropping the whole sheet.
func schemeSyntaxCSS(s Scheme) string {
	var b strings.Builder
	q := func(sel string) string { return fmt.Sprintf(sel, s.Name) }
	if light := styleCSS(s.chromaLight); light != "" {
		fmt.Fprintf(&b, "\n/* %s — light */\n", s.Label)
		b.WriteString(scopeSyntax(light, q(`[data-scheme=%q]`)))
	}
	dark := styleCSS(s.chromaDark)
	if dark == "" {
		return b.String() // dark style missing: ship this scheme light-only
	}
	fmt.Fprintf(&b, "/* %s — explicit Dark mode */\n", s.Label)
	b.WriteString(scopeSyntax(dark, q(`[data-scheme=%q][data-mode="dark"]`)))
	fmt.Fprintf(&b, "/* %s — System mode following the OS */\n@media (prefers-color-scheme: dark) {\n", s.Label)
	b.WriteString(scopeSyntax(dark, q(`[data-scheme=%q]:not([data-mode="light"])`)))
	b.WriteString("\n}\n")
	return b.String()
}

// styleCSS renders a chroma style's class-based CSS, or "" if the style is
// unknown or fails to render.
func styleCSS(name string) string {
	st := styles.Get(name)
	if st == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := chromaFormatter.WriteCSS(&buf, st); err != nil {
		return ""
	}
	return buf.String()
}

// scopeSyntax prefixes every chroma rule in a WriteCSS dump with `prefix` so a
// second style's tokens override the default unscoped block only when the scope
// selector matches the <html> mode attributes. Chroma's token classes (.k, .s,
// …) are style-independent, so light and dark collide on the same selectors —
// scoping is what lets both live in one sheet. The standalone `.bg` rule chroma
// emits for its background <div> is dropped: PreventSurroundingPre means our
// markup only ever carries class="chroma", never class="bg".
func scopeSyntax(css, prefix string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(css, "\n") {
		brace := strings.Index(line, "{")
		if brace < 0 {
			continue // blank lines between rules — chroma puts one rule per line
		}
		head := line[:brace]
		comment := ""
		if i := strings.LastIndex(head, "*/"); i >= 0 {
			comment, head = head[:i+2]+" ", head[i+2:]
		}
		sel := strings.TrimSpace(head)
		if strings.HasPrefix(sel, ".bg") {
			continue // unused background-div rule
		}
		b.WriteString(comment + prefix + " " + sel + " " + line[brace:] + "\n")
	}
	return b.String()
}

// lexerFor returns a Coalesce-wrapped lexer matched against `filename`,
// caching the result so repeat calls for the same path skip the
// pattern-match + wrap overhead.
//
// Hand-rolled overrides for extensions where chroma's defaults are
// wrong or missing for our use case:
//   - .tmpl → "Go HTML Template". Chroma's Match() picks the Cheetah
//     lexer (Python templating) for *.tmpl, which mangles Go's
//     `{{}}` syntax; force the Go HTML Template lexer instead. The
//     override fires BEFORE Match() — if it ran after, Cheetah would
//     win and the override would never trigger.
func lexerFor(filename string) chroma.Lexer {
	if v, ok := lexerCache.Load(filename); ok {
		return v.(chroma.Lexer)
	}
	var lx chroma.Lexer
	switch {
	case strings.HasSuffix(filename, ".tmpl"):
		lx = lexers.Get("Go HTML Template")
	}
	if lx == nil {
		lx = lexers.Match(filename)
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
		// Pathologically long lines (minified bundles, one-liner config,
		// the giant template-loop line in prereview.tmpl) tokenise into
		// hundreds of spans. That span-soup is the dominant cost in both
		// HTML weight and client paint. Past maxHighlightLineChars,
		// render the line as a single escaped string — still readable,
		// no color, but the page stays light.
		if len(contents[i]) > maxHighlightLineChars {
			out[i] = template.HTML(htmlpkg.EscapeString(contents[i]))
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

// maxHighlightLineChars caps per-line highlighting. A single line over
// this length skips chroma and renders escaped-plain. Tuned so normal
// source (even long ones) keeps color while minified/one-liner lines
// don't blow up the span count.
const maxHighlightLineChars = 1000

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
