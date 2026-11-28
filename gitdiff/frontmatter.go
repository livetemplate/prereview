package gitdiff

import (
	"bytes"
	"html"
	"html/template"
	"strings"
)

// frontMatter is a Markdown file's leading YAML front matter: the lines from
// an opening `---` on line 1 to the closing `---` (or `...`).
type frontMatter struct {
	end     int       // the closing delimiter's 1-based line
	entries []fmEntry // one per top-level key, in order
	blank   []byte    // src with every front matter line emptied, newlines kept
}

// fmEntry is one top-level key with the lines its value spans.
type fmEntry struct {
	key, value string
	start, end int // 1-based source lines
}

// parseFrontMatter finds src's front matter. goldmark has no front matter
// extension, so without this the opening `---` renders as a rule and the
// closing one turns the keys into a setext heading. ok is false when src
// doesn't open with `---` or never closes it.
func parseFrontMatter(src []byte) (fm frontMatter, ok bool) {
	lines := bytes.SplitAfter(src, []byte("\n"))
	delim := func(l []byte) string { return strings.TrimSpace(string(bytes.TrimPrefix(l, []byte("\xef\xbb\xbf")))) }
	if len(lines) < 2 || delim(lines[0]) != "---" {
		return fm, false
	}
	for i := 1; i < len(lines); i++ {
		if d := delim(lines[i]); d == "---" || d == "..." {
			fm.end = i + 1
			break
		}
	}
	if fm.end == 0 {
		return fm, false
	}
	for i := 1; i < fm.end-1; i++ {
		line := strings.TrimRight(string(lines[i]), "\r\n")
		key, value, isKey := strings.Cut(line, ":")
		if isKey && key != "" && key == strings.TrimSpace(key) && !strings.HasPrefix(key, "#") && !strings.HasPrefix(key, "-") {
			fm.entries = append(fm.entries, fmEntry{key: key, value: unquote(strings.TrimSpace(value)), start: i + 1, end: i + 1})
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// A nested map, a list item or a folded string belongs to the key
		// above it. Any other line means this isn't YAML: a Markdown rule,
		// prose, then another rule.
		nested := line != strings.TrimLeft(line, " \t") || strings.HasPrefix(line, "- ")
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		n := len(fm.entries)
		if !nested || n == 0 {
			return frontMatter{}, false
		}
		e := &fm.entries[n-1]
		e.value = strings.TrimSpace(e.value + "\n" + trimmed)
		e.end = i + 1
	}
	if len(fm.entries) == 0 {
		return frontMatter{}, false
	}
	var b bytes.Buffer
	for i, l := range lines {
		if i < fm.end && bytes.HasSuffix(l, []byte("\n")) {
			b.WriteByte('\n')
		} else if i >= fm.end {
			b.Write(l)
		}
	}
	fm.blank = b.Bytes()
	return fm, true
}

// unquote drops one pair of matching quotes around a scalar.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// blocks renders the front matter as the preview's first blocks: a header on
// the opening `---`, then one row per key on that key's lines, so a reviewer
// can comment on a single key. They share the per-row table markup GFM
// tables use (md-solo-table).
func (fm frontMatter) blocks() []MarkdownBlock {
	out := []MarkdownBlock{{
		HTML:      `<table class="md-solo-table md-frontmatter"><thead><tr><th colspan="2">Front matter</th></tr></thead></table>`,
		StartLine: 1,
		EndLine:   1,
	}}
	for _, e := range fm.entries {
		out = append(out, MarkdownBlock{
			HTML: template.HTML(`<table class="md-solo-table md-frontmatter"><tbody><tr><th scope="row">` + html.EscapeString(e.key) + //nolint:gosec // key and value are escaped
				`</th><td>` + html.EscapeString(e.value) + `</td></tr></tbody></table>`),
			StartLine: e.start,
			EndLine:   e.end,
		})
	}
	return out
}

// withoutFrontMatter returns src with its front matter lines emptied (line
// numbers kept), or src unchanged when it has none.
func withoutFrontMatter(src []byte) []byte {
	if fm, ok := parseFrontMatter(src); ok {
		return fm.blank
	}
	return src
}
