package checks

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/livetemplate/prereview/gitdiff"
)

// Finding is one span a check flags. Lines are 1-based; columns are 0-based,
// half-open RUNE offsets — the coordinates prereview's text comments use, so a
// finding can become a text comment unchanged. A span may cross lines (a long
// sentence): FromCol is on Line, ToCol on EndLine.
type Finding struct {
	Line     int    `json:"line"`
	EndLine  int    `json:"end_line"`
	FromCol  int    `json:"from_col"`
	ToCol    int    `json:"to_col"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Match    string `json:"match,omitempty"`
}

// Result statuses.
const (
	StatusPass    = "pass"
	StatusFail    = "fail"
	StatusError   = "error"
	StatusRunning = "running"
)

// Result is one check's outcome on one file.
type Result struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Type        string    `json:"type"`
	Level       string    `json:"level"`
	Source      string    `json:"source"`
	Path        string    `json:"path,omitempty"`
	Status      string    `json:"status"`
	Err         string    `json:"error,omitempty"`
	Findings    []Finding `json:"findings,omitempty"`
}

// newResult starts a Result carrying the rule's identity.
func newResult(r *Rule) Result {
	return Result{ID: r.ID, Name: r.Name, Description: r.Description, Type: r.Type,
		Level: r.Level, Source: r.Source, Path: r.Path}
}

// finish sets Status from Err and Findings.
func (res Result) finish() Result {
	switch {
	case res.Err != "":
		res.Status = StatusError
	case len(res.Findings) > 0:
		res.Status = StatusFail
	default:
		res.Status = StatusPass
	}
	return res
}

// ErrorResult is a rule that couldn't run (a parse error, a failed command).
func ErrorResult(r *Rule, msg string) Result {
	res := newResult(r)
	res.Err = msg
	return res.finish()
}

// RunningResult is a placeholder for an async rule whose result hasn't arrived.
func RunningResult(r *Rule) Result {
	res := newResult(r)
	res.Status = StatusRunning
	return res
}

// RunInline runs a data rule (existence, substitution, occurrence, vocab) over a
// prepared Doc. Pure and fast: Go's regexp is RE2, linear in the input, so a rule
// from any source is safe to run on the render path. Prepare the Doc once per
// file and run every rule against it.
func RunInline(r *Rule, d *Doc) Result {
	if r.Err != "" {
		return ErrorResult(r, r.Err)
	}
	res := newResult(r)
	switch r.Type {
	case TypeExistence, TypeSubstitution, TypeVocab:
		res.Findings = d.matchPatterns(r)
	case TypeOccurrence:
		res.Findings = d.occurrences(r)
	}
	return res.finish()
}

// Doc is a file's text prepared for the inline rules: the lines joined by '\n'
// with Markdown code masked to spaces, so regexes see prose only and every rune
// keeps its original column.
type Doc struct {
	text      string  // masked text
	byteRune  []int32 // byte offset in text → rune index
	lineStart []int   // rune index where each 0-based line starts
}

// NewDoc prepares path's lines (Markdown is masked by extension).
func NewDoc(path string, lines []string) *Doc {
	markdown := gitdiff.IsMarkdownPath(path)
	d := &Doc{}
	masked := make([][]rune, len(lines))
	for i, l := range lines {
		masked[i] = []rune(l)
	}
	if markdown {
		maskMarkdown(masked)
	}
	var b strings.Builder
	pos := 0
	for i, l := range masked {
		if i > 0 {
			b.WriteByte('\n')
			pos++
		}
		d.lineStart = append(d.lineStart, pos)
		b.WriteString(string(l))
		pos += len(l)
	}
	d.text = b.String()
	d.byteRune = make([]int32, len(d.text)+1)
	var r int32
	for i := 0; i < len(d.text); {
		_, sz := utf8.DecodeRuneInString(d.text[i:])
		for k := 0; k < sz; k++ {
			d.byteRune[i+k] = r
		}
		i += sz
		r++
	}
	d.byteRune[len(d.text)] = r
	return d
}

// pos maps a rune index to (1-based line, 0-based column).
func (d *Doc) pos(ri int) (int, int) {
	li := sort.Search(len(d.lineStart), func(i int) bool { return d.lineStart[i] > ri }) - 1
	if li < 0 {
		li = 0
	}
	return li + 1, ri - d.lineStart[li]
}

// span builds a Finding over byte range [from, to) of d.text.
func (d *Doc) span(from, to int) Finding {
	l1, c1 := d.pos(int(d.byteRune[from]))
	l2, c2 := d.pos(int(d.byteRune[to]))
	if c2 == 0 && l2 > l1 { // a span ending exactly at a newline ends on the line before
		l2--
		c2 = d.lineStart[l2] - 1 - d.lineStart[l2-1] // that line's rune length
	}
	return Finding{Line: l1, EndLine: l2, FromCol: c1, ToCol: c2}
}

func (d *Doc) matchPatterns(r *Rule) []Finding {
	var out []Finding
	seen := map[[2]int]bool{}
	for _, p := range r.patterns {
		for _, m := range p.re.FindAllStringIndex(d.text, -1) {
			match := d.text[m[0]:m[1]]
			if p.want != "" && match == p.want { // already the preferred text
				continue
			}
			key := [2]int{m[0], m[1]}
			if seen[key] {
				continue
			}
			seen[key] = true
			f := d.span(m[0], m[1])
			f.Match = match
			f.Severity = r.Level
			f.Message = d.message(r, p, match)
			out = append(out, f)
		}
	}
	sortFindings(out)
	return out
}

func (d *Doc) message(r *Rule, p pattern, match string) string {
	msg := r.Message
	switch {
	case r.Type == TypeSubstitution:
		if msg == "" {
			msg = "Use '%s' instead of '%s'."
		}
		return formatMessage(msg, p.want, match)
	case r.Type == TypeVocab && p.caseOnly:
		return formatMessage("Use '%s' instead of '%s'.", p.want, match)
	case r.Type == TypeVocab:
		return formatMessage("Avoid '%s'.", match)
	}
	if msg == "" {
		msg = "'%s' is flagged by " + r.Name + "."
	}
	return formatMessage(msg, match)
}

// occurrences flags each scope unit (sentence or line) where token matches more
// than r.Max times, spanning the whole unit.
func (d *Doc) occurrences(r *Rule) []Finding {
	var out []Finding
	for _, u := range d.units(r.Scope) {
		n := len(r.token.FindAllStringIndex(d.text[u[0]:u[1]], r.Max+1)) // stop once over
		if n <= r.Max {
			continue
		}
		f := d.span(u[0], u[1])
		f.Severity = r.Level
		f.Message = r.Message
		if f.Message == "" {
			f.Message = r.Name + ": over the limit."
		}
		out = append(out, f)
	}
	return out
}

var sentenceEnd = regexp.MustCompile(`[.!?]+["')\]]*(\s|$)`)

// units splits d.text into byte ranges, trimmed of surrounding space: lines, or
// sentences within blocks (a blank line or a Markdown block start ends a block).
func (d *Doc) units(scope string) [][2]int {
	var blocks [][2]int
	lineBytes := strings.SplitAfter(d.text, "\n")
	off, start := 0, -1
	flush := func(end int) {
		if start >= 0 {
			blocks = append(blocks, [2]int{start, end})
			start = -1
		}
	}
	for _, lb := range lineBytes {
		line := strings.TrimRight(lb, "\n")
		end := off + len(line)
		switch {
		case strings.TrimSpace(line) == "":
			flush(off)
		case scope == "line":
			blocks = append(blocks, [2]int{off, end})
		case blockStart.MatchString(line):
			flush(off)
			start = off
		default:
			if start < 0 {
				start = off
			}
		}
		off += len(lb)
	}
	flush(len(d.text))
	if scope == "line" {
		return trimUnits(d.text, blocks)
	}
	var out [][2]int
	for _, b := range blocks {
		s := b[0]
		for _, m := range sentenceEnd.FindAllStringIndex(d.text[b[0]:b[1]], -1) {
			out = append(out, [2]int{s, b[0] + m[1]})
			s = b[0] + m[1]
		}
		if s < b[1] {
			out = append(out, [2]int{s, b[1]})
		}
	}
	return trimUnits(d.text, out)
}

// blockStart matches a line that begins a new Markdown block: heading, list item,
// quote or table row.
var blockStart = regexp.MustCompile(`^\s*(#{1,6}\s|[-*+]\s|\d+[.)]\s|>|\|)`)

func trimUnits(text string, us [][2]int) [][2]int {
	out := us[:0]
	for _, u := range us {
		a, b := u[0], u[1]
		for a < b && asciiSpace(text[a]) {
			a++
		}
		for b > a && asciiSpace(text[b-1]) {
			b--
		}
		if b > a {
			out = append(out, [2]int{a, b})
		}
	}
	return out
}

// asciiSpace tests one byte. Byte-wise on purpose: a UTF-8 continuation byte such as
// 0xA0 must never read as a space (unicode.IsSpace(0xA0) is NBSP).
func asciiSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		return fs[i].FromCol < fs[j].FromCol
	})
}

// fence matches a Markdown code fence opener/closer.
var fence = regexp.MustCompile("^\\s{0,3}(```+|~~~+)")

// maskMarkdown blanks what Vale skips in Markdown: front matter, fenced code,
// inline code spans, link destinations and autolinks. Masked runes become spaces
// so columns don't shift.
func maskMarkdown(lines [][]rune) {
	blank := func(l []rune, a, b int) {
		for i := a; i < b && i < len(l); i++ {
			l[i] = ' '
		}
	}
	i := 0
	if len(lines) > 0 && strings.TrimSpace(string(lines[0])) == "---" {
		for j := 1; j < len(lines); j++ {
			if strings.TrimSpace(string(lines[j])) == "---" {
				for k := 0; k <= j; k++ {
					blank(lines[k], 0, len(lines[k]))
				}
				i = j + 1
				break
			}
		}
	}
	inFence, marker := false, ""
	for ; i < len(lines); i++ {
		l := lines[i]
		if m := fence.FindStringSubmatch(string(l)); m != nil {
			if !inFence {
				inFence, marker = true, m[1][:3]
			} else if strings.HasPrefix(m[1], marker) {
				inFence = false
			}
			blank(l, 0, len(l))
			continue
		}
		if inFence {
			blank(l, 0, len(l))
			continue
		}
		maskInline(l, blank)
	}
}

// maskInline blanks `code` spans, ](link targets) and <autolinks> on one line.
func maskInline(l []rune, blank func([]rune, int, int)) {
	for i := 0; i < len(l); i++ {
		switch {
		case l[i] == '`':
			n := 0
			for i+n < len(l) && l[i+n] == '`' {
				n++
			}
			if end := findRun(l, i+n, '`', n); end >= 0 {
				blank(l, i, end+n)
				i = end + n - 1
			} else {
				i += n - 1
			}
		case l[i] == ']' && i+1 < len(l) && l[i+1] == '(':
			if end := indexRune(l, i+2, ')'); end >= 0 {
				blank(l, i+1, end+1)
				i = end
			}
		case l[i] == '<' && hasPrefixRunes(l[i+1:], "http"):
			if end := indexRune(l, i+1, '>'); end >= 0 {
				blank(l, i, end+1)
				i = end
			}
		}
	}
}

func findRun(l []rune, from int, r rune, n int) int {
	for i := from; i+n <= len(l); i++ {
		ok := true
		for k := 0; k < n; k++ {
			if l[i+k] != r {
				ok = false
				break
			}
		}
		if ok && (i+n == len(l) || l[i+n] != r) {
			return i
		}
	}
	return -1
}

func indexRune(l []rune, from int, r rune) int {
	for i := from; i < len(l); i++ {
		if l[i] == r {
			return i
		}
	}
	return -1
}

func hasPrefixRunes(l []rune, p string) bool {
	return strings.HasPrefix(string(l[:min(len(l), len(p))]), p)
}
